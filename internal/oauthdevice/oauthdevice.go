// Package oauthdevice implements the QuantumAtlas server side of the
// OAuth 2.0 Device Authorization Grant (RFC 8628), wired to PAT
// minting so that headless CLIs (e.g. `qatlas auth login --device`)
// can obtain a Personal Access Token without ever needing a local
// browser or a copy-paste step.
//
// # Flow
//
//	+-------+                                  +---------------+
//	|       |                                  |               |
//	|  CLI  |  POST /api/oauth/device/code     |  qatlasd      |
//	|       | -------------------------------> |  (auth-less)  |
//	|       |       device_code, user_code     |               |
//	|       | <------------------------------- |               |
//	|       |                                  |               |
//	|       |  open browser → /<lang>/device   |               |
//	|       |  user enters user_code, signs    |               |
//	|       |  in via existing GitHub OAuth,   |               |
//	|       |  clicks Approve                  |               |
//	|       |                                  |               |
//	|       |  POST /api/oauth/device/token    |               |
//	|       |  (poll every `interval`)         |               |
//	|       | -------------------------------> |               |
//	|       |  authorization_pending | slow_down|              |
//	|       | <------------------------------- |               |
//	|       |  ... eventually ...              |               |
//	|       |  PAT plaintext (one time)        |               |
//	|       | <------------------------------- |               |
//	+-------+                                  +---------------+
//
// # Design notes that aren't obvious from the RFC
//
//   - device_code is STORED HASHED (sha256). The plaintext lives only
//     in the response to /code and in the CLI's memory. Anyone with
//     read access to the SQLite file therefore can't poll on behalf of
//     a pending CLI. We use sha256 rather than bcrypt because the
//     plaintext is already 384 bits of crypto/rand entropy — brute-
//     force is infeasible without a hash function bottleneck.
//
//   - user_code is STORED PLAINTEXT. Users have to type it, and a
//     bcrypt scan per submitted code would be both slow and pointless
//     (the value is shown to the user in the CLI's terminal).
//
//   - Status transitions are STRICTLY ATOMIC. Every mutation is a
//     conditional UPDATE with WHERE current-status = expected. If the
//     row count is zero, the requested transition wasn't legal at the
//     instant we tried it (e.g. another poller raced us, or the user
//     denied, or it expired). This makes the polling endpoint
//     idempotent and side-effect-bounded without needing row locks.
//
//   - PAT plaintext is generated INSIDE the approved→consumed
//     transaction. Storing the plaintext on the device_codes row
//     (even temporarily) would re-introduce the same "secret at rest"
//     problem that pat_tokens explicitly avoids. Instead, the poller
//     that wins the consumed-transition immediately calls
//     pat.Generate() inside the same Tx and returns the plaintext in
//     the HTTP response. Subsequent polls see status=consumed and get
//     invalid_grant — the token can never be returned twice.
package oauthdevice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// CollectionName is the PocketBase collection that stores in-flight
// device authorization requests. Lives in pb_data alongside
// pat_tokens; CRUD is gated entirely by /api/oauth/device/* handlers
// (no public access rules on the collection itself).
const CollectionName = "oauth_device_codes"

// PollIntervalSeconds is the minimum interval, in seconds, between
// successive /api/oauth/device/token polls for one device_code. The
// CLI is expected to honour the value returned in the /code response;
// the server still enforces it via the slow_down error to defend
// against misbehaving clients.
//
// 5 s is the value Google / GitHub Apps use and is the lowest value
// that keeps the per-device QPS comfortably under 1 — anything
// shorter just burns CPU on both sides without delivering a faster
// approval (the human at the browser still needs ~10 s minimum to
// type the code, sign in if needed and click Approve).
const PollIntervalSeconds = 5

// ExpiresInSeconds is the TTL of a freshly-minted device_code. After
// this much wall-clock time has passed since /code returned, the
// device_code is no longer usable — the row is automatically
// transitioned to status=expired the next time anyone polls it, and
// fresh polls return the expired_token error.
//
// 600 s (10 min) is the RFC 8628 recommendation. Long enough that a
// distracted user can recover, short enough that a stolen device_code
// is useless on the next coffee break.
const ExpiresInSeconds = 600

// MaxPollCount caps total polls per device_code. With the 5 s
// interval and 10 min TTL the legitimate polling budget is ~120
// requests; we round up to 150 for clock skew + retried polls. Past
// the cap the row is auto-expired so a leaked-but-not-yet-approved
// device_code can't be hammered indefinitely.
const MaxPollCount = 150

// Status values written to the oauth_device_codes.status column.
// Listed in legal-transition order for documentation purposes; the
// migration's SelectField enforces this same allow-list at write
// time.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusConsumed = "consumed"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
)

// userCodeGroups + userCodeGroupLen describe the human-typable format
// XXXX-XXXX. Total entropy = 8 chars × log2(32) = 40 bits, which is
// more than enough collision-resistance for the ~10-min concurrent
// window the server has to keep unique user_codes around (collision
// risk for N concurrent codes is ~N²/2⁴¹; N=100 still gives <1-in-2⁶ⁱ).
const (
	userCodeGroups   = 2
	userCodeGroupLen = 4
)

// userCodeAlphabet excludes visually-ambiguous characters: 0 vs O,
// 1 vs I/L. Length 32 → exactly 5 bits per character → no modulo
// bias when sampled from a uniform 256-value byte.
const userCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ErrEmpty is returned by NormalizeUserCode when the input contains
// no alphanumeric characters at all. Callers map this to a 400.
var ErrEmpty = errors.New("oauthdevice: empty user code")

// ErrShape is returned by NormalizeUserCode when the canonical form
// doesn't have exactly userCodeGroups × userCodeGroupLen letters.
// Callers map this to a 400 too.
var ErrShape = errors.New("oauthdevice: user code has wrong shape")

// GenerateCodes returns a fresh (device_code, user_code) pair. Both
// values are crypto-random; the caller is expected to ship the pair
// in the /code response, then persist only sha256(device_code) and
// the plaintext user_code on the new oauth_device_codes row.
//
// device_code: 48 bytes from crypto/rand, base64-url encoded → 64
// chars of opaque text with ~384 bits of entropy. The hashed form is
// what becomes the lookup key.
//
// user_code: 8 chars from a 32-char ambiguity-free alphabet, grouped
// XXXX-XXXX. Always uppercase. The dash is purely cosmetic; both
// /lookup and /approve accept the un-dashed lowercase form via
// NormalizeUserCode.
func GenerateCodes() (deviceCode, userCode string, err error) {
	deviceCode, err = randomURLSafe(48)
	if err != nil {
		return "", "", err
	}
	userCode, err = randomUserCode()
	if err != nil {
		return "", "", err
	}
	return deviceCode, userCode, nil
}

// HashDeviceCode returns sha256(deviceCode) hex-encoded. Used both
// when inserting (we never store plaintext) and on every poll (we
// hash the presented device_code and select by the digest).
//
// SHA-256 is fine here despite being a "fast" hash: the plaintext
// already has ~384 bits of crypto/rand entropy, so the attacker's
// best move against the hash is pre-image, not brute-force search.
// A bcrypt-style work factor would just punish legitimate pollers
// (every 5 s, hot path) for no security gain.
func HashDeviceCode(deviceCode string) string {
	sum := sha256.Sum256([]byte(deviceCode))
	return hex.EncodeToString(sum[:])
}

// NormalizeUserCode accepts any of "WDJB-MJHT", "wdjbmjht",
// "wdjb mjht", "WDJB MJHT", "wdjb-mjht" and returns the canonical
// XXXX-XXXX uppercase form. Returns ErrEmpty / ErrShape on invalid
// inputs; callers should treat both as a flat 400 to the user.
//
// The leniency is deliberate: humans transcribe user codes by hand
// and routinely drop dashes / lowercase letters / paste with leading
// whitespace. The cost of accepting these variants is one for-loop;
// the benefit is one less "huh, the code doesn't work" support thread.
func NormalizeUserCode(s string) (string, error) {
	var clean strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			clean.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			clean.WriteRune(r)
		}
	}
	raw := clean.String()
	if raw == "" {
		return "", ErrEmpty
	}
	if len(raw) != userCodeGroups*userCodeGroupLen {
		return "", ErrShape
	}
	var out strings.Builder
	for g := 0; g < userCodeGroups; g++ {
		if g > 0 {
			out.WriteByte('-')
		}
		out.WriteString(raw[g*userCodeGroupLen : (g+1)*userCodeGroupLen])
	}
	return out.String(), nil
}

func randomURLSafe(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomUserCode() (string, error) {
	total := userCodeGroups * userCodeGroupLen
	buf := make([]byte, total)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	alpha := userCodeAlphabet
	out := make([]byte, 0, total+userCodeGroups-1)
	for i := 0; i < total; i++ {
		if i > 0 && i%userCodeGroupLen == 0 {
			out = append(out, '-')
		}
		// len(userCodeAlphabet)=32 divides 256 exactly, so this mod
		// is uniform — no rejection sampling required.
		out = append(out, alpha[int(buf[i])%len(alpha)])
	}
	return string(out), nil
}
