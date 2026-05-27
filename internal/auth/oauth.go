// Package auth wires QuantumAtlas-specific authentication behavior onto
// the embedded PocketBase. It lives entirely on top of PocketBase's own
// users / auth_collection_oauth2 machinery — we never reimplement password
// hashing, JWT issuance, or OAuth callback handling ourselves.
package auth

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

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

// ErrNotConfigured is returned when a feature requires env vars that the
// operator has not provided. Currently unused outside tests; exposed so
// downstream packages can do typed checks if needed.
var ErrNotConfigured = errors.New("feature not configured")
