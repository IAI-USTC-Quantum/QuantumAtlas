// Package auth wires QuantumAtlas-specific authentication behavior onto
// the embedded PocketBase. It lives entirely on top of PocketBase's own
// users / auth_collection_oauth2 machinery — we never reimplement password
// hashing, JWT issuance, or OAuth callback handling ourselves.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/auth"
)

// UsersCollection is the PocketBase default auth collection name we attach
// OAuth providers to. Kept as a constant so other modules (route handlers,
// migrations) can reference it without hardcoding "users" everywhere.
const UsersCollection = "users"

// Register installs all QuantumAtlas auth-related lifecycle hooks on the
// given PocketBase app. Call exactly once during main() before app.Start().
func Register(app core.App, cfg *config.Config) {
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if err := syncGitHubProvider(e.App, cfg); err != nil {
			// Don't fail bootstrap — log loudly and keep the server up so
			// the operator can fix env vars without losing the rest of
			// PocketBase. OAuth login will simply 4xx until resolved.
			slog.Warn("github oauth provider sync failed", "error", err)
		}
		return nil
	})

	// Active-active OAuth user record convergence — see the rationale
	// in syncStableUserID below. Installed on the "users" collection tag
	// so it only fires for end-user OAuth (the superusers collection
	// rejects OAuth2 sign-up unconditionally, but we belt-and-brace).
	app.OnRecordAuthWithOAuth2Request(UsersCollection).BindFunc(func(e *core.RecordAuthWithOAuth2RequestEvent) error {
		// Gate FIRST: a non-allowlisted GitHub account must be rejected
		// before any users record is minted or session token issued.
		// Returning an error here aborts the OAuth2 request so a blocked
		// account leaves no trace and gets a clean 403.
		if err := enforceLoginAllowlist(e, cfg); err != nil {
			return err
		}
		if err := syncStableUserID(e); err != nil {
			// Logged-not-fatal: if our id injection fails (e.g. PB has
			// already pre-populated CreateData[id] differently), let
			// the OAuth flow fall back to PB's random-id default rather
			// than blocking sign-in. The user just won't be cross-node-
			// stable; they can re-link manually later.
			slog.Warn("oauth: failed to inject stable user id", "error", err)
		}
		return e.Next()
	})
}

// enforceLoginAllowlist rejects OAuth sign-in for any GitHub account whose
// login is not on the configured allowlist (Config.IsGitHubLoginAllowed).
//
// This is the membership gate that turns the read-locked knowledge base
// into a members-only one: scopeGuard on every data endpoint makes an
// unauthenticated caller get 401, and this hook ensures only vetted GitHub
// accounts can obtain an authenticated session in the first place.
//
// Fail-closed: an empty allowlist blocks everyone (see IsGitHubLoginAllowed).
// A nil OAuth2User (no identity to vet) yields an empty login, which is also
// rejected — we never let an unidentified caller through.
func enforceLoginAllowlist(e *core.RecordAuthWithOAuth2RequestEvent, cfg *config.Config) error {
	if e == nil {
		return nil
	}
	login := ""
	if e.OAuth2User != nil {
		login = e.OAuth2User.Username // GitHub provider maps `login` here
	}
	if cfg.IsGitHubLoginAllowed(login) {
		return nil
	}
	slog.Warn("oauth: blocked sign-in for non-allowlisted github account",
		"login", login,
		"provider", e.ProviderName,
	)
	return apis.NewForbiddenError("This GitHub account is not authorized to sign in to QuantumAtlas.", nil)
}

// syncGitHubProvider makes the users collection's OAuth2 settings reflect
// the GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET env vars. Idempotent: safe
// to call on every boot.
//
// Behavior matrix:
//
//	creds set,  not configured  -> insert provider, enable OAuth2
//	creds set,  already present -> overwrite clientId/clientSecret in place
//	creds empty, configured     -> leave existing config alone (operator
//	                               manually disabled the env var; respect
//	                               whatever they last saved in the admin UI)
//	creds empty, not configured -> no-op
func syncGitHubProvider(app core.App, cfg *config.Config) error {
	if cfg.GitHubClientID == "" || cfg.GitHubClientSecret == "" {
		slog.Debug("github oauth env vars empty; skipping provider sync")
		return nil
	}

	collection, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return fmt.Errorf("find %s collection: %w", UsersCollection, err)
	}
	if !collection.IsAuth() {
		return fmt.Errorf("%s collection is not an auth collection", UsersCollection)
	}

	desired := core.OAuth2ProviderConfig{
		Name:         auth.NameGithub,
		ClientId:     cfg.GitHubClientID,
		ClientSecret: cfg.GitHubClientSecret,
	}

	replaced := false
	for i, existing := range collection.OAuth2.Providers {
		if existing.Name == auth.NameGithub {
			// Preserve any operator-tuned fields (DisplayName, Extra, etc.)
			// while pushing the env-driven secret pair through.
			merged := existing
			merged.ClientId = desired.ClientId
			merged.ClientSecret = desired.ClientSecret
			collection.OAuth2.Providers[i] = merged
			replaced = true
			break
		}
	}
	if !replaced {
		collection.OAuth2.Providers = append(collection.OAuth2.Providers, desired)
	}
	collection.OAuth2.Enabled = true

	if err := app.Save(collection); err != nil {
		return fmt.Errorf("save %s collection: %w", UsersCollection, err)
	}

	action := "inserted"
	if replaced {
		action = "updated"
	}
	slog.Info("github oauth provider synced",
		"action", action,
		"client_id_suffix", lastChars(cfg.GitHubClientID, 4),
	)
	return nil
}

// lastChars returns the trailing n characters of s, or s itself when
// shorter. Useful for logging credentials without leaking the full value.
func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// StableUserIDPrefix is the leading character of every deterministic
// users.id we mint. "g" was chosen because it is not generated by
// PocketBase's own random-id RNG (which uses crypto/rand bytes mapped
// onto [a-zA-Z0-9] — every position uses the full alphabet, so the
// odds of a random id starting with "g" are 1/62 — non-zero but rare).
// A unique prefix lets ops grep for the deterministic ids in audit
// logs and tells migration tooling which records came from this hook
// vs. legacy random-id records.
const StableUserIDPrefix = "g"

// stableUserIDLength matches PocketBase's default id field length (15
// characters). Diverging would force a collection schema migration
// across every node, which is a bigger change than the convergence
// benefit warrants.
const stableUserIDLength = 15

// syncStableUserID derives a deterministic users.id from the OAuth2
// provider's user id, so the same GitHub account ends up with the
// same PocketBase record id across all edge nodes. Without this, each
// edge node's auto-generated random id for the same GitHub user
// diverges, which complicates cross-node reconciliation (PATs are
// still per-node, but at least the user identity is portable).
//
// # When this fires
//
// PocketBase's OAuth2 flow runs (1) lookup-by-externalAuths, then
// (2) lookup-by-email, then (3) fires this hook, then (4) creates a
// new record if e.Record is still nil. We hook between (3) and (4):
//
//   - e.Record non-nil: PB found an existing user by email or by an
//     existing externalAuths link. We do NOT rename them — PB has no
//     in-place id-change API, and the linked PATs would all dangle.
//     Log a warning if the existing id doesn't match what we would
//     have picked (operator can manually reconcile later via
//     pb_data migration). This path is rare in active-active because
//     both nodes have independent DBs.
//
//   - e.Record nil + an existing user with our deterministic id is
//     already present: assign that record to e.Record so PB links
//     the new externalAuths row to it instead of creating yet another
//     user. This is the active-active convergence case: the user
//     signed up on node A first, then this hook fires on node B, and
//     we steer node B's externalAuths link onto a record with the
//     same id as A's (or onto an existing record on B that was
//     pre-created by this same hook in a previous login on B).
//
//   - e.Record nil + no existing match: inject our deterministic id
//     into e.CreateData so PB creates the new user with our chosen
//     id. PB's create flow honors payload["id"] when it satisfies
//     the id field validators (alphanumeric, 15 chars by default —
//     both of which our scheme satisfies).
//
// # ID scheme
//
// desiredID = "g" + first 14 hex chars of sha256(provider + ":" + user_id)
//
//   - 14 hex chars = 56 bits of entropy. SHA-256 collisions in the
//     14-char prefix would require ~2^28 GitHub users on the same
//     provider — orders of magnitude past anything we'll see.
//   - Provider prefix in the hash defends against a hypothetical
//     cross-provider id collision (Google user "12345" vs GitHub
//     user "12345" would otherwise both hash to the same digest).
//   - "g" leading char makes deterministic ids greppable in audit
//     logs. Random PB ids will occasionally start with "g" (~1.6%
//     chance) but never end up exactly equal to our scheme.
func syncStableUserID(e *core.RecordAuthWithOAuth2RequestEvent) error {
	if e == nil || e.OAuth2User == nil {
		return nil
	}
	providerUserID := strings.TrimSpace(e.OAuth2User.Id)
	if providerUserID == "" || e.ProviderName == "" {
		return nil
	}
	if e.Collection == nil || e.Collection.Name != UsersCollection {
		return nil
	}

	desiredID := deriveStableUserID(e.ProviderName, providerUserID)

	// Case 1: PB found an existing record via externalAuths or email.
	// Leave them alone but log if the id doesn't match what we'd
	// pick — useful for operators planning a cross-node merge later.
	if e.Record != nil {
		if e.Record.Id != desiredID {
			slog.Info("oauth: existing user has legacy non-stable id (cross-node convergence will be partial)",
				"provider", e.ProviderName,
				"user_id", e.Record.Id,
				"stable_id_would_be", desiredID,
				"provider_user_id_suffix", lastChars(providerUserID, 6),
			)
		}
		return nil
	}

	// Case 2: PB has not located an existing record yet, but one with
	// our deterministic id might already exist on this node (e.g. the
	// user logged in before, the externalAuths link got dropped, but
	// the users record survived). Link the OAuth flow to that
	// existing record so we don't accidentally orphan it.
	if existing, err := e.App.FindRecordById(UsersCollection, desiredID); err == nil && existing != nil {
		e.Record = existing
		slog.Info("oauth: re-linked OAuth2 to existing stable-id user",
			"provider", e.ProviderName,
			"user_id", desiredID,
		)
		return nil
	}

	// Case 3: Fresh sign-up. Inject our deterministic id so PB's
	// create flow uses it instead of a random one. Allocate CreateData
	// on the fly if PB hasn't already.
	if e.CreateData == nil {
		e.CreateData = map[string]any{}
	}
	// Respect explicit caller-provided id (a test or admin call that
	// overrides via the OAuth2 form's createData might intentionally
	// pick a specific id). Never overwrite.
	if _, ok := e.CreateData["id"]; ok {
		return nil
	}
	e.CreateData["id"] = desiredID
	slog.Info("oauth: minting new user with stable id",
		"provider", e.ProviderName,
		"user_id", desiredID,
		"provider_user_id_suffix", lastChars(providerUserID, 6),
	)
	return nil
}

// deriveStableUserID computes the deterministic 15-char alphanumeric
// id documented in syncStableUserID. Pure / deterministic / testable.
func deriveStableUserID(provider, providerUserID string) string {
	h := sha256.Sum256([]byte(provider + ":" + providerUserID))
	// 7 bytes → 14 hex chars; prepend "g" for a 15-char id matching
	// PocketBase's default id field length.
	return StableUserIDPrefix + hex.EncodeToString(h[:7])
}

// ErrNotConfigured is returned when a feature requires env vars that the
// operator has not provided. Currently unused outside tests; exposed so
// downstream packages can do typed checks if needed.
var ErrNotConfigured = errors.New("feature not configured")
