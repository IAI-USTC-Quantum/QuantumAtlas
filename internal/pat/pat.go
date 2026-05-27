// Package pat implements QuantumAtlas Personal Access Tokens (PATs) —
// long-lived bearer tokens that any signed-in user can mint for use in
// CLI / CI / scripted callers.
//
// The PocketBase user JWT issued by GitHub OAuth login (the one the SPA
// pastes onto its /token page) defaults to a 14-day lifetime. That is
// fine for the browser but painful for nightly jobs and GH Actions
// secrets. PATs solve that by giving each user an authentik-style "User
// Settings → Tokens" UX:
//
//   - Browser visits /pat, hits "New token", fills in name / optional
//     expiry, the server hands back a one-time plaintext (prefixed with
//     "qat_") and stores only the bcrypt hash.
//   - CLI callers stuff the plaintext into QATLAS_TOKEN (or pass
//     --token=...); the same Authorization: Bearer <plaintext> header is
//     accepted by every write endpoint, in parallel with PocketBase user
//     session tokens. The two formats coexist; authGuard tells them
//     apart by the "qat_" prefix.
//   - Revoking = deleting the pat_tokens record.
//
// This file owns the pure-token mechanics: generation, hashing, lookup,
// last-used bookkeeping. PocketBase wiring (collection migration, REST
// handlers, authGuard hook) lives in sibling files / packages.
package pat

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"golang.org/x/crypto/bcrypt"
)

// CollectionName is the PocketBase collection that stores PAT records.
const CollectionName = "pat_tokens"

// TokenPrefix is the literal prefix every QuantumAtlas PAT plaintext
// starts with. It exists to give authGuard a cheap O(1) check before
// paying for a bcrypt scan and to avoid collisions with PocketBase JWTs
// (which start with "eyJ"). It is also stored verbatim as part of the
// prefix display column shown in the SPA list view.
const TokenPrefix = "qat_"

// PrefixDisplayLen is how many leading characters of the plaintext
// (including TokenPrefix) we record in the "prefix" field for UI
// display: enough to disambiguate but nowhere near enough to brute-
// force or recover the secret. With base62 chars, 12 visible characters
// after "qat_" gives an 8-char window of randomness shown to the user.
const PrefixDisplayLen = 12

// secretBodyLen is the number of random base62 characters appended
// after TokenPrefix. 24 chars × log2(62) ≈ 143 bits of entropy, well
// past any feasible brute-force horizon and comfortably more than the
// 128-bit floor recommended for long-lived API tokens.
const secretBodyLen = 24

// Sentinel errors callers can check with errors.Is.
var (
	// ErrNotFound is returned by Lookup when no stored record matches
	// the supplied plaintext. authGuard converts this to a flat 401.
	ErrNotFound = errors.New("pat: no matching token")

	// ErrExpired is returned by Lookup when a record was found but its
	// expires_at field is in the past. authGuard converts this to 401
	// too — we don't leak the distinction to anonymous callers.
	ErrExpired = errors.New("pat: token expired")
)

// base62Alphabet is the character set used for the random body of a
// PAT. Letters + digits, URL-safe, shell-safe, no ambiguous lookalikes
// removed (the entropy budget already absorbs occasional 0/O confusion
// and copy-pasting is the expected delivery channel).
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Generate mints a brand-new PAT. The plaintext is returned exactly
// once — the caller is expected to ship it to the user immediately and
// then forget it. Only `prefix` and `hash` should ever be persisted.
//
// The returned prefix is the first PrefixDisplayLen characters of the
// plaintext (so it always begins with TokenPrefix), suitable for both
// the indexed `prefix` column and the SPA list display.
func Generate() (plaintext, prefix, hash string, err error) {
	body, err := randomBase62(secretBodyLen)
	if err != nil {
		return "", "", "", fmt.Errorf("pat: random: %w", err)
	}

	plaintext = TokenPrefix + body
	prefix = PrefixOf(plaintext)

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", "", "", fmt.Errorf("pat: bcrypt: %w", err)
	}
	hash = string(hashBytes)

	return plaintext, prefix, hash, nil
}

// Looks is a cheap shape check that authGuard can call on every
// incoming Authorization header without touching the database. It only
// confirms the candidate "could be" one of our PATs — false negatives
// here always mean "definitely not ours; let the PocketBase JWT path
// handle it" and never block a legitimate request.
//
// The minimum length is TokenPrefix + at least one body character.
// PocketBase JWTs start with "eyJ" and contain dots, so they always
// return false here.
func Looks(token string) bool {
	if len(token) <= len(TokenPrefix) {
		return false
	}
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	return true
}

// PrefixOf derives the stored display-prefix for a given plaintext.
// Used both at Generate time (to persist) and at Lookup time (to scope
// the bcrypt scan to records that share the same plaintext prefix).
//
// Returns the full plaintext if it is shorter than PrefixDisplayLen,
// which only happens when Looks(plaintext) is already false.
func PrefixOf(plaintext string) string {
	if len(plaintext) < PrefixDisplayLen {
		return plaintext
	}
	return plaintext[:PrefixDisplayLen]
}

// Lookup finds the pat_tokens record matching the supplied plaintext
// and returns it along with the associated user record.
//
// Optimization: we narrow by the indexed `prefix` column first
// (PrefixDisplayLen characters of plaintext, including the "qat_"
// literal). At 8 base62 chars of randomness in that visible window
// (~47 bits), a prefix collision across a single deployment is
// vanishingly unlikely, so the bcrypt-loop almost always runs over
// exactly one row. We still loop defensively so a once-in-a-trillion
// duplicate doesn't lock anyone out.
//
// Returns:
//   - record:     the pat_tokens record (caller may call MarkUsed)
//   - userRecord: the linked users record (caller assigns to re.Auth)
//   - err:        ErrNotFound / ErrExpired / wrapped DB error
//
// Caller MUST treat ErrNotFound and ErrExpired identically (flat 401);
// the distinction exists only so internal callers (tests, future
// /api/pat/whoami) can show a useful message.
func Lookup(app core.App, plaintext string) (*core.Record, *core.Record, error) {
	if !Looks(plaintext) {
		return nil, nil, ErrNotFound
	}

	records, err := app.FindAllRecords(
		CollectionName,
		dbx.HashExp{"prefix": PrefixOf(plaintext)},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("pat: load %s: %w", CollectionName, err)
	}

	plaintextBytes := []byte(plaintext)
	for _, rec := range records {
		hash := rec.GetString("token_hash")
		if hash == "" {
			continue
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), plaintextBytes); err != nil {
			continue
		}

		// Found it. Check expiry next so an expired-but-correct token
		// still surfaces the more specific ErrExpired sentinel for
		// tests / future logging.
		expires := rec.GetDateTime("expires_at").Time()
		if !expires.IsZero() && time.Now().After(expires) {
			return rec, nil, ErrExpired
		}

		userID := rec.GetString("user")
		if userID == "" {
			return rec, nil, fmt.Errorf("pat: record %s has empty user link", rec.Id)
		}
		userRec, err := app.FindRecordById("users", userID)
		if err != nil {
			return rec, nil, fmt.Errorf("pat: load linked user %s: %w", userID, err)
		}
		return rec, userRec, nil
	}

	return nil, nil, ErrNotFound
}

// MarkUsed bumps the last_used_at column on the record. It is fire-and-
// forget bookkeeping; failures are logged by the caller but must not
// fail the actual request (a write endpoint should accept a valid PAT
// even if we can't update the audit column for some reason).
//
// We use types.NowDateTime() so the value passes the same validation
// any other DateField write would.
func MarkUsed(app core.App, rec *core.Record) error {
	if rec == nil {
		return errors.New("pat: nil record")
	}
	rec.Set("last_used_at", types.NowDateTime())
	if err := app.Save(rec); err != nil {
		return fmt.Errorf("pat: save last_used_at: %w", err)
	}
	return nil
}

// randomBase62 returns n cryptographically-random base62 characters.
//
// We oversample by 2× into a byte buffer and take `byte % 62` to map
// onto the alphabet. The naive modulo introduces a tiny bias (values
// 256 mod 62 = 8, so bytes 248..255 are slightly more represented),
// but at 143 bits of entropy the bias is statistically irrelevant for
// token uniqueness or guessability. The simpler code is the right call
// here; if you ever change this for a different cost/uniqueness
// regime, switch to rejection sampling.
func randomBase62(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("pat: randomBase62 length must be > 0")
	}
	buf := make([]byte, n*2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = base62Alphabet[int(buf[i])%len(base62Alphabet)]
	}
	return string(out), nil
}
