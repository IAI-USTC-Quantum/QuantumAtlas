// Command-line management surface for QuantumAtlas Personal Access Tokens
// (PATs). Mounted on the Go server binary's root cobra command, so the
// same `qatlasd` (or `qatlasd`) executable that runs `serve`
// also exposes `pat mint`, `pat list`, `pat revoke`, and `pat scopes`.
//
// Why a server-side CLI when /api/pat already exists?
//
//   1. /api/pat is gated by sessionGuard (PAT-auth not accepted). The
//      browser-OAuth-only path is fine for end users but painful for
//      service-account / CI / nightly bootstrap, where the operator
//      already has shell access to the server box but no browser
//      session.
//
//   2. The CLI bypasses the HTTP stack entirely and writes records
//      directly through PocketBase's app.Save. It reuses pat.Generate
//      and pat.ValidateScopes so the on-disk record is byte-for-byte
//      identical to what the HTTP handler produces — no parallel
//      validation code to drift.
//
//   3. Operators with shell access to the SQLite DB are higher-trust
//      than HTTP callers (they could just edit the DB directly); a
//      CLI is just a convenience for that audience.
//
// Output contract for `mint`:
//   * stdout: the plaintext token, exactly once, terminated by '\n'.
//     Designed for `SECRET=$(qatlasd pat mint ...)` capture.
//   * stderr: human-friendly summary (id, prefix, scopes, expiry).
//     Goes through fmt.Fprintln so `2>/dev/null` cleanly silences it.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/spf13/cobra"
)

// MaxPATExpiryDaysCLI mirrors routes.MaxPATExpiryDays. Duplicated
// rather than imported so this command file has zero dependency on
// internal/routes (which pulls in HTTP framework deps the CLI does
// not need). The two values MUST stay in lockstep — if MaxPATExpiryDays
// ever changes, update this constant too.
const MaxPATExpiryDaysCLI = 365

// NewPATCommand wires the `pat` subcommand group onto the PocketBase
// root cobra command. Call once after pocketbase.New() and before
// app.Start().
func NewPATCommand(app core.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "pat",
		Short: "Manage QuantumAtlas Personal Access Tokens (PATs) directly on the server",
		Long: `Mint, list, and revoke QuantumAtlas Personal Access Tokens (PATs) without going
through the /api/pat HTTP surface.

This is intended for service-account / CI / nightly bootstrap on the box
that owns the PocketBase DB. The HTTP /api/pat surface is gated by
sessionGuard (browser-session JWT only — PAT auth is not accepted there)
which is the right policy for end users but inconvenient when you just
need to seed a long-lived credential for an unattended caller.

Records written by this CLI are byte-for-byte equivalent to records
created via POST /api/pat — same Generate, same validators, same
collection — so they share every contract (scope enforcement, expiry,
last_used_at bookkeeping) at request time.`,
	}

	root.AddCommand(patMintCommand(app))
	root.AddCommand(patListCommand(app))
	root.AddCommand(patRevokeCommand(app))
	root.AddCommand(patScopesCommand())

	return root
}

// patMintCommand creates a PAT for the given user, prints the
// plaintext to stdout (once) and the metadata summary to stderr.
func patMintCommand(app core.App) *cobra.Command {
	var (
		userRef     string
		name        string
		description string
		scopesCSV   string
		expiresDays int
	)
	cmd := &cobra.Command{
		Use:   "mint",
		Short: "Mint a new PAT for the given user",
		Example: `  # Mint a 365-day shares:write PAT for a CI nightly job
  qatlasd pat mint --user me@example.com --name nightly-ci \
      --scopes shares:write --expires-in-days 365

  # Capture the plaintext directly into a variable
  SECRET=$(qatlasd pat mint --user me@example.com --name x \
      --scopes shares:write --expires-in-days 30)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			userRef = strings.TrimSpace(userRef)
			name = strings.TrimSpace(name)
			description = strings.TrimSpace(description)

			if userRef == "" {
				return errors.New("--user is required (email or users record id)\n" +
					"hint: run `qatlasd users list` to see who's registered on this edge")
			}
			if name == "" {
				return errors.New("--name is required")
			}
			if len(name) > 80 {
				return errors.New("--name exceeds 80 characters")
			}
			if len(description) > 200 {
				return errors.New("--description exceeds 200 characters")
			}
			if expiresDays <= 0 {
				return errors.New("--expires-in-days is required and must be > 0 (no perpetual PATs)")
			}
			if expiresDays > MaxPATExpiryDaysCLI {
				return fmt.Errorf("--expires-in-days exceeds maximum of %d", MaxPATExpiryDaysCLI)
			}

			scopes := parseScopesCSV(scopesCSV)
			if err := pat.ValidateScopes(scopes); err != nil {
				return err
			}

			userRec, err := lookupUser(app, userRef)
			if err != nil {
				return err
			}

			plaintext, prefix, hash, err := pat.Generate()
			if err != nil {
				return fmt.Errorf("generate token: %w", err)
			}

			collection, err := app.FindCollectionByNameOrId(pat.CollectionName)
			if err != nil {
				return fmt.Errorf("find %s collection: %w", pat.CollectionName, err)
			}
			rec := core.NewRecord(collection)
			rec.Set("user", userRec.Id)
			rec.Set("name", name)
			rec.Set("prefix", prefix)
			rec.Set("token_hash", hash)
			if description != "" {
				rec.Set("description", description)
			}
			scopesJSON, _ := json.Marshal(scopes)
			rec.Set("scopes", string(scopesJSON))
			expires := types.NowDateTime().AddDate(0, 0, expiresDays)
			rec.Set("expires_at", expires)

			if err := app.Save(rec); err != nil {
				return fmt.Errorf("save token: %w", err)
			}

			// Plaintext: stdout, exactly one line.
			fmt.Fprintln(cmd.OutOrStdout(), plaintext)

			// Summary: stderr, so SECRET=$(qatlasd pat mint ...)
			// captures only the plaintext.
			fmt.Fprintf(cmd.ErrOrStderr(),
				"minted PAT id=%s prefix=%s user=%s name=%q scopes=%v expires_at=%s\n",
				rec.Id, prefix, userRec.GetString("email"), name, scopes, expires.String(),
			)
			return nil
		},
	}

	cmd.Flags().StringVar(&userRef, "user", "", "owner of the new PAT (email, or users record id)")
	cmd.Flags().StringVar(&name, "name", "", "human-readable token name (≤80 chars, required)")
	cmd.Flags().StringVar(&description, "description", "", "free-form description (≤200 chars, optional)")
	cmd.Flags().StringVar(&scopesCSV, "scopes", "", "comma-separated scope list (run `pat scopes` to list)")
	cmd.Flags().IntVar(&expiresDays, "expires-in-days", 0, "token lifetime in days (1..365, required)")

	return cmd
}

// patListCommand prints existing PAT records, optionally filtered by user.
func patListCommand(app core.App) *cobra.Command {
	var (
		userRef string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List PAT records (optionally filtered by user)",
		Example: `  # List every PAT on this box
  qatlasd pat list

  # Filter to one user's tokens
  qatlasd pat list --user me@example.com

  # JSON output for tooling
  qatlasd pat list --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var records []*core.Record
			var err error
			if strings.TrimSpace(userRef) != "" {
				userRec, lookErr := lookupUser(app, userRef)
				if lookErr != nil {
					return lookErr
				}
				records, err = app.FindRecordsByFilter(
					pat.CollectionName,
					"user = {:user}",
					"-created",
					0, 0,
					map[string]any{"user": userRec.Id},
				)
			} else {
				records, err = app.FindAllRecords(pat.CollectionName)
			}
			if err != nil {
				return fmt.Errorf("list %s: %w", pat.CollectionName, err)
			}

			if asJSON {
				out := make([]map[string]any, 0, len(records))
				for _, rec := range records {
					out = append(out, summariseRecord(app, rec))
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tUSER\tNAME\tPREFIX\tSCOPES\tEXPIRES_AT\tLAST_USED")
			for _, rec := range records {
				s := summariseRecord(app, rec)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					s["id"], s["user_email"], s["name"], s["prefix"],
					strings.Join(toStringSlice(s["scopes"]), ","),
					s["expires_at"], s["last_used_at"],
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&userRef, "user", "", "filter to one user (email, or users record id)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// patRevokeCommand hard-deletes a PAT record by id. Same effect as
// DELETE /api/pat/{id} on the HTTP surface.
func patRevokeCommand(app core.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "revoke <id>",
		Short:        "Permanently delete (revoke) a PAT by record id",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if id == "" {
				return errors.New("id is required")
			}
			rec, err := app.FindRecordById(pat.CollectionName, id)
			if err != nil {
				return fmt.Errorf("find %s/%s: %w", pat.CollectionName, id, err)
			}
			if err := app.Delete(rec); err != nil {
				return fmt.Errorf("delete %s: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "revoked: %s (prefix %s)\n", id, rec.GetString("prefix"))
			return nil
		},
	}
	return cmd
}

// patScopesCommand prints the canonical scope vocabulary so operators
// can see what to pass to `pat mint --scopes`.
func patScopesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scopes",
		Short: "List the canonical PAT scope vocabulary",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "SCOPE\tDESCRIPTION")
			for _, s := range pat.AllScopes {
				fmt.Fprintf(w, "%s\t%s\n", s, pat.ScopeDescription[s])
			}
			fmt.Fprintf(w, "\nmax expires-in-days: %d\n", MaxPATExpiryDaysCLI)
			return w.Flush()
		},
	}
	return cmd
}

// lookupUser resolves `ref` to a users record. Accepts either:
//   - an email (contains "@")
//   - a users record id (everything else)
//
// Returns a user-friendly error if no record matches.
func lookupUser(app core.App, ref string) (*core.Record, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("user reference is empty")
	}
	if strings.Contains(ref, "@") {
		rec, err := app.FindAuthRecordByEmail(auth.UsersCollection, ref)
		if err != nil {
			return nil, fmt.Errorf("no users record with email %q (run `qatlasd users list` to see candidates)", ref)
		}
		return rec, nil
	}
	rec, err := app.FindRecordById(auth.UsersCollection, ref)
	if err != nil {
		return nil, fmt.Errorf("no users record with id %q (run `qatlasd users list` to see candidates)", ref)
	}
	return rec, nil
}

// parseScopesCSV splits a comma-separated string into a trimmed slice.
// Empty entries are dropped so `--scopes ""` (or unset) → nil.
func parseScopesCSV(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// summariseRecord builds a flat map representation of a PAT record
// suitable for both table and JSON rendering. Mirrors the
// routes.patSummary shape but adds user_email (joined from the linked
// users record) since the CLI list is typically cross-user.
func summariseRecord(app core.App, rec *core.Record) map[string]any {
	scopes := decodeScopesString(rec.GetString("scopes"))
	email := ""
	if userID := rec.GetString("user"); userID != "" {
		if u, err := app.FindRecordById(auth.UsersCollection, userID); err == nil {
			email = u.GetString("email")
		}
	}
	return map[string]any{
		"id":           rec.Id,
		"user_id":      rec.GetString("user"),
		"user_email":   email,
		"name":         rec.GetString("name"),
		"prefix":       rec.GetString("prefix"),
		"description":  rec.GetString("description"),
		"scopes":       scopes,
		"expires_at":   nonZeroDateString(rec.GetDateTime("expires_at")),
		"last_used_at": nonZeroDateString(rec.GetDateTime("last_used_at")),
		"created":      rec.GetDateTime("created").String(),
	}
}

// decodeScopesString parses the JSON-encoded scopes column. Mirrors
// routes/pat.go's decodeScopesField — kept local so this CLI module
// has no dependency on routes (which would pull HTTP framework deps).
func decodeScopesString(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// nonZeroDateString renders a PocketBase DateTime, returning "" for
// the zero value (which PocketBase otherwise serialises as the
// distracting "0001-01-01 00:00:00.000Z").
func nonZeroDateString(dt types.DateTime) string {
	if dt.Time().IsZero() {
		return ""
	}
	return dt.String()
}

// toStringSlice coerces a summariseRecord scopes entry back to a
// []string for table rendering. The map literal above already stores
// the slice, but Go's any-typed map round-trip drops static typing.
func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case nil:
		return nil
	default:
		return []string{fmt.Sprintf("%v", v)}
	}
}
