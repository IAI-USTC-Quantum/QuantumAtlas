// System PAT — a single, optional, env-configured bearer token that
// authenticates the caller WITHOUT any reference to PocketBase users
// or pb_data. Designed for ops paths where a user record may not
// exist (or may not exist YET): bootstrapping a fresh edge before
// anyone has OAuth'd, scripted disaster recovery after pb_data is
// wiped, CI nightlies that should not depend on a particular human
// account, etc.
//
// # Threat model
//
// Anyone who can read the server's environment (or the .env file
// loaded into it) can authenticate as the system PAT. This is the
// same blast radius as the existing S3 / GitHub OAuth / Neo4j
// credentials already living in the .env: a process with read
// access to those secrets can already do most of what the system
// PAT enables. Putting the PAT alongside them adds no new exfil
// surface, and gains us "no DB required for breaking-glass access".
//
// # Storage
//
// Loaded once at server startup from QATLAS_SYSTEM_PAT. Held as
// raw bytes for constant-time comparison. Never written to disk by
// the server, never logged in plaintext, never echoed back over the
// wire. The plaintext only lives in (1) the operator's env source
// of truth and (2) this process's memory.
//
// # Scope semantics
//
// QATLAS_SYSTEM_PAT_SCOPES (optional CSV) determines what the
// system PAT can call. Defaults to ScopeMaster ("*") which mirrors
// a browser session: scopeGuard short-circuits and the PAT can hit
// every gated endpoint. Operators who want a less-privileged ops
// token (e.g. "ci can read but not write") set the env explicitly:
//
//	QATLAS_SYSTEM_PAT_SCOPES=wiki:read,papers:read,graph:read
//
// Unlike user-minted PATs (which go through the REST API and have
// ScopeMaster forbidden by pat.ValidateScopes), the system PAT may
// include ScopeMaster — the operator who set the env var is already
// trusted to write to the DB directly, so further gatekeeping
// would be theatre.
//
// # Why not a PocketBase record with user=null
//
// Considered; rejected. The point is to keep auth working when
// pb_data is unavailable (post-wipe disaster recovery, fresh edge
// before first migration, etc.). Anything that stores the
// credential in the same SQLite we're trying to recover defeats
// the purpose.

package pat

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"strings"
)

// minSystemPATLength is the floor for QATLAS_SYSTEM_PAT plaintext.
// The number is small enough not to inconvenience reasonable random
// strings (openssl rand -base64 32 ≈ 44 chars, uuidgen ≈ 36 chars,
// hex32 = 32 chars) but large enough to reject obvious placeholders
// ("secret", "changeme", empty-after-trim, accidental leading flag,
// etc.). Tightening it further would push past common defaults that
// users actually pick and produce friction without commensurate
// safety; loosening it would let "hunter2"-class accidents through.
const minSystemPATLength = 16

// systemPATEnv is the env var holding the plaintext token. Empty
// (or unset) means the feature is disabled — authGuard falls
// through to the normal PocketBase PAT / session paths.
const systemPATEnv = "QATLAS_SYSTEM_PAT"

// systemPATScopesEnv is the optional CSV of scopes granted to the
// system PAT. Unset → default to ScopeMaster (one-line breaking-
// glass setup). When set, the value is validated via
// ValidateScopesIncludingMaster — same vocabulary as user PATs,
// plus the master wildcard.
const systemPATScopesEnv = "QATLAS_SYSTEM_PAT_SCOPES"

// SystemPAT is the in-memory representation of the env-loaded
// bearer token. Construct via LoadSystemPAT; a nil receiver behaves
// as "feature disabled" so callers can hand around the pointer
// without nil-checking everywhere.
type SystemPAT struct {
	secret []byte
	scopes []string
	length int
}

// LoadSystemPAT reads QATLAS_SYSTEM_PAT and (optionally)
// QATLAS_SYSTEM_PAT_SCOPES from the process environment and returns
// the corresponding SystemPAT. Three outcomes:
//
//   - env unset / empty → (nil, nil): feature disabled. The caller
//     should still call routes.UseSystemPAT(nil) (or skip it) so
//     the runtime path treats the absence consistently.
//   - env set but too short or scopes malformed → (nil, error):
//     caller MUST treat as fatal (log.Fatal). A misconfigured
//     system PAT shouldn't silently fall through to "no token",
//     because the operator's intent was clearly to enable it.
//   - env set and valid → (*SystemPAT, nil): mounted on routes
//     via UseSystemPAT.
func LoadSystemPAT() (*SystemPAT, error) {
	secret := strings.TrimSpace(os.Getenv(systemPATEnv))
	if secret == "" {
		return nil, nil
	}
	if len(secret) < minSystemPATLength {
		return nil, fmt.Errorf(
			"%s is too short (%d chars; minimum %d). Set a stronger value, "+
				"e.g. `openssl rand -base64 32`",
			systemPATEnv, len(secret), minSystemPATLength,
		)
	}

	scopes, err := parseSystemPATScopes(os.Getenv(systemPATScopesEnv))
	if err != nil {
		return nil, err
	}

	return &SystemPAT{
		secret: []byte(secret),
		scopes: scopes,
		length: len(secret),
	}, nil
}

// parseSystemPATScopes resolves the scopes field. Empty / unset
// defaults to []{ScopeMaster}, mirroring "the system PAT acts
// like a session by default". Any other value is split on commas,
// trimmed, dropped if empty, then validated against
// ValidateScopesIncludingMaster.
func parseSystemPATScopes(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{ScopeMaster}, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, errors.New(systemPATScopesEnv + " is set but parses to zero scopes")
	}
	if err := ValidateScopesIncludingMaster(out); err != nil {
		return nil, fmt.Errorf("%s: %w", systemPATScopesEnv, err)
	}
	return out, nil
}

// Match performs a constant-time comparison of bearer against the
// stored secret. A nil receiver always returns (nil, false) so
// "feature disabled" callers can match-and-fall-through safely.
//
// On success the returned scope slice is a fresh copy — callers
// may stash it under a request key without worrying that a later
// caller (or scopeGuard mutating its input) corrupts the canonical
// value held on the SystemPAT.
func (s *SystemPAT) Match(bearer string) ([]string, bool) {
	if s == nil || len(bearer) == 0 {
		return nil, false
	}
	// ConstantTimeCompare returns 0 if lengths differ, but it does
	// the length check itself first which is observable through
	// timing. To keep the leak surface minimal we compare equal-
	// length byte slices: pad the bearer to the secret's length
	// when it's shorter, then check both length equality and the
	// byte compare result.
	bb := []byte(bearer)
	if len(bb) != len(s.secret) {
		// Still run a constant-time compare on a same-length slice
		// so a timing attacker can't distinguish "wrong length"
		// from "right length, wrong bytes" by latency alone.
		_ = subtle.ConstantTimeCompare(s.secret, s.secret)
		return nil, false
	}
	if subtle.ConstantTimeCompare(bb, s.secret) != 1 {
		return nil, false
	}
	scopesCopy := make([]string, len(s.scopes))
	copy(scopesCopy, s.scopes)
	return scopesCopy, true
}

// Length returns the byte length of the stored secret, or 0 if the
// receiver is nil. Exposed so the server can log "system PAT
// enabled (length=N)" at startup without leaking the secret
// itself.
func (s *SystemPAT) Length() int {
	if s == nil {
		return 0
	}
	return s.length
}

// Scopes returns a copy of the granted scope slice (or nil for a
// nil receiver). Exposed so the startup log can render the scope
// list alongside the length for operator visibility.
func (s *SystemPAT) Scopes() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.scopes))
	copy(out, s.scopes)
	return out
}

// Enabled is a tiny convenience for "this receiver represents an
// active feature". Equivalent to `s != nil`, kept as a named
// method so caller intent reads cleanly.
func (s *SystemPAT) Enabled() bool {
	return s != nil
}
