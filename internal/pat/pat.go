// Package pat implements QuantumAtlas Personal Access Tokens (PATs) —
// long-lived bearer tokens that any signed-in user can mint for use in
// CLI / CI / scripted callers.
//
// The PocketBase user JWT issued by GitHub OAuth login is held by the
// SPA in `pb.authStore` (browser localStorage) and never surfaced as
// copyable text — the deliberate choice is "session token stays in
// the browser, anything else uses a PAT". Session JWTs also default
// to a 14-day lifetime, fine for the browser but painful for nightly
// jobs and GH Actions secrets. PATs solve that by giving each user
// an authentik-style "User Settings → Tokens" UX:
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
	"crypto/sha256"
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
// The minimum length is PrefixDisplayLen because Lookup's prefix-indexed
// scan needs at least that many chars to be useful (a "qat_x"-shaped
// bearer is never a real PAT). PocketBase JWTs start with "eyJ" and
// contain dots, so they always return false here.
func Looks(token string) bool {
	if len(token) < PrefixDisplayLen {
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
// Returns "" if the input is shorter than PrefixDisplayLen — never the
// raw input. This is defense-in-depth: future callers might use this
// helper for audit logging or telemetry, and silently returning the
// full plaintext on a runt input would leak the secret into those
// sinks. Callers MUST treat "" as "no usable prefix, fall through to
// not-found", which is the natural behaviour for a DB lookup since no
// stored prefix is ever empty.
func PrefixOf(plaintext string) string {
	if len(plaintext) < PrefixDisplayLen {
		return ""
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
// Cache fast path: before any DB / bcrypt work we consult the package-
// level verify cache (see cache.go) keyed by sha256(plaintext). On hit,
// we re-fetch the linked user record from the DB (so callers see a
// fresh record with current email / verified flag / etc.) but skip
// both the FindAllRecords prefix scan AND the bcrypt compare. This
// collapses repeat verifies of the same hot PAT (typical CI traffic
// pattern) to a single SQL SELECT per request instead of N×bcrypt.
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

	// Fast path: cache hit. We still need the live pat_tokens record
	// (for MarkUsed) and the live users record (for re.Auth), so we
	// do two FindRecordById calls — both indexed primary-key lookups,
	// no bcrypt — instead of the prefix scan + bcrypt loop. If either
	// disappears we fall through to the slow path and treat as
	// ErrNotFound so a cache entry can't outlive its referent.
	cache := initVerifyCache()
	if entry, ok := cache.Get(plaintext); ok {
		patRec, err := app.FindRecordById(CollectionName, entry.PATRecordID)
		if err == nil && patRec != nil {
			userRec, err := app.FindRecordById("users", entry.UserID)
			if err == nil && userRec != nil {
				return patRec, userRec, nil
			}
		}
		// Stale entry — referenced record disappeared. Drop it and
		// fall through to the authoritative slow path. We can't tell
		// from FindRecordById whether the error is "not found" vs
		// "DB unavailable", but either way the slow path is correct.
		cache.lru.Remove(sha256.Sum256([]byte(plaintext)))
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
		//
		// expires_at is REQUIRED at the handler layer (patCreateHandler
		// rejects zero / negative / >365 days). Treating a zero / past
		// timestamp as expired here is defense-in-depth: if anything
		// ever creates a perpetual record by bypassing the handler
		// (e.g. PocketBase Admin UI direct write, future bulk-import),
		// it still fails the auth path.
		expires := rec.GetDateTime("expires_at").Time()
		if expires.IsZero() || time.Now().After(expires) {
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

		// Populate the cache only on full success. We never cache
		// ErrNotFound (negative results aren't worth the timing-leak
		// risk) or ErrExpired (the slow path is already cheap when
		// the prefix scan finds zero matches, and re-checking expiry
		// every time is the safer default).
		cache.Put(plaintext, verifyCacheEntry{
			PATRecordID:  rec.Id,
			UserID:       userID,
			ScopesRaw:    rec.GetString("scopes"),
			PATExpiresAt: expires,
		})

		return rec, userRec, nil
	}

	return nil, nil, ErrNotFound
}

// MarkUsed bumps the last_used_at column on the record. It is fire-and-
// forget bookkeeping; failures are logged by the caller but must not
// fail the actual request (a write endpoint should accept a valid PAT
// even if we can't update the audit column for some reason).
//
// Implementation note: we issue a single-column UPDATE rather than
// calling app.Save(rec). Reasons:
//
//   1. app.Save() re-serialises the whole record, bumping the
//      auto-managed `updated` timestamp and firing OnRecordUpdate*
//      hooks. Two concurrent requests with the same PAT would race
//      on those side-effects, jittering `updated` and potentially
//      double-firing audit hooks.
//
//   2. UPDATE last_used_at = ? WHERE id = ? is a single atomic SQL
//      statement that any number of concurrent calls can collapse
//      onto without contention beyond the per-row lock SQLite already
//      manages.
//
//   3. Skips the validation cost of Save (which re-runs every field
//      validator on the record) — meaningful for high-traffic CI PATs
//      where MarkUsed fires once per authenticated request.
func MarkUsed(app core.App, rec *core.Record) error {
	if rec == nil {
		return errors.New("pat: nil record")
	}
	_, err := app.DB().
		NewQuery("UPDATE " + CollectionName + " SET last_used_at = {:t} WHERE id = {:id}").
		Bind(dbx.Params{
			"t":  types.NowDateTime(),
			"id": rec.Id,
		}).
		Execute()
	if err != nil {
		return fmt.Errorf("pat: update last_used_at: %w", err)
	}
	return nil
}

// randomBase62 returns n cryptographically-random base62 characters
// using rejection sampling — bytes ≥ 248 (the largest multiple of 62
// that fits in a byte) are discarded so every accepted byte maps onto
// the alphabet with exactly uniform probability. The naive `byte % 62`
// approach would slightly over-represent the first 8 chars (256 % 62
// = 8); irrelevant at 143 bits net entropy but trivial to fix, and
// readers shouldn't have to audit a known-biased PRNG for an
// auth-critical primitive.
//
// Crypto/rand is buffered into 64-byte chunks to amortise syscall
// cost (rejection sampling otherwise issues one syscall per char on a
// rejected byte).
func randomBase62(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("pat: randomBase62 length must be > 0")
	}
	// 256 / 62 = 4 with remainder 8, so 248 is the smallest byte
	// value we must reject. Accepted bytes map cleanly onto the
	// 62-character alphabet.
	const (
		bucket      = 62 * 4 // = 248
		chunkBuffer = 64     // tune for amortised syscall cost
	)
	out := make([]byte, n)
	var buf [chunkBuffer]byte
	bufPos := chunkBuffer // force initial fill
	for i := 0; i < n; {
		if bufPos >= chunkBuffer {
			if _, err := rand.Read(buf[:]); err != nil {
				return "", err
			}
			bufPos = 0
		}
		b := buf[bufPos]
		bufPos++
		if b >= bucket {
			continue
		}
		out[i] = base62Alphabet[b%62]
		i++
	}
	return string(out), nil
}
