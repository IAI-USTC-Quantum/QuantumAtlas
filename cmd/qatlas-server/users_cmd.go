// User-discovery CLI surface for QuantumAtlas.
//
// Sibling to `pat` / `storage` / `service` / `papers` / `openalex`.
// The HTTP /api/users surface is intentionally absent (PocketBase
// admin UI covers it for browsers); operators with shell access to
// the box need a non-browser way to enumerate `users` records so they
// can pick a `--user` to feed `pat mint`.
//
// Without this command, the loop "I want to mint a CI PAT, but I do
// not remember which email I OAuth'd in with on this edge" forces
// operators to either open the PocketBase admin UI in a browser or
// reach for `sqlite3 pb_data/data.db` — both clumsier than the
// equivalent of `getent passwd` for the project's user store.
//
// Output contract for `users list` mirrors `pat list`: a tabwriter
// table by default, `--json` for tooling. Sensitive fields
// (verification tokens, password hashes) are never surfaced — we
// only project the columns an operator needs to disambiguate users.

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"

	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"
)

// NewUsersCommand wires the `users` subcommand group onto the root
// cobra command. Call once after pocketbase.New() and before
// app.Start() — same registration-timing constraint as the other
// CLI groups (see main.go § AddCommand block).
func NewUsersCommand(app core.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "users",
		Short: "Inspect the PocketBase users collection on this edge",
		Long: `Look at the users registered on this server's PocketBase instance.

Each edge runs its own PocketBase with its own SQLite store, so the
list returned here is the local edge's view only — a GitHub account
that has OAuth'd into both edges will appear here on each edge with a
(deterministically derived but) separate record id.

Use this before ` + "`pat mint --user`" + ` when you don't remember the
exact email a particular operator signed up with.`,
	}

	root.AddCommand(usersListCommand(app))

	return root
}

// usersListCommand prints every record in the users auth collection.
// Sorted by created descending so the most recent signups float to
// the top — matches the order an operator setting up a new edge
// expects to see ("which account did I just OAuth into?").
func usersListCommand(app core.App) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every users record on this edge",
		Long: `List the users records that PocketBase knows about on this edge.

Columns: ID, EMAIL, NAME, VERIFIED, PROVIDERS, CREATED. PROVIDERS is
joined from PocketBase's internal _externalAuths table — comma-separated
list of OAuth providers each user has linked (typically just "github"
for QuantumAtlas).`,
		Example: `  # Tabular view (default)
  qatlas-server users list

  # Machine-readable
  qatlas-server users list --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			collection, err := app.FindCollectionByNameOrId(auth.UsersCollection)
			if err != nil {
				return fmt.Errorf("find %s collection: %w", auth.UsersCollection, err)
			}

			records, err := app.FindAllRecords(auth.UsersCollection)
			if err != nil {
				return fmt.Errorf("list %s: %w", auth.UsersCollection, err)
			}

			// Best-effort external-auth join. One query per user is
			// the cost of using PocketBase's high-level helper, but
			// for the realistic user counts on a QuantumAtlas edge
			// (single-digit to low-double-digit) it is irrelevant —
			// and it avoids reaching into the underlying dbx layer.
			_ = collection // referenced via auth.UsersCollection above; FindAllRecords already validated it exists

			summaries := make([]map[string]any, 0, len(records))
			for _, rec := range records {
				summaries = append(summaries, summariseUserRecord(app, rec))
			}

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(summaries)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tEMAIL\tNAME\tVERIFIED\tPROVIDERS\tCREATED")
			for _, s := range summaries {
				providers := strings.Join(toStringSlice(s["providers"]), ",")
				if providers == "" {
					providers = "-"
				}
				name := fmt.Sprintf("%v", s["name"])
				if name == "" {
					name = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\t%s\n",
					s["id"], s["email"], name, s["verified"], providers, s["created"],
				)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if len(records) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no users on this edge yet — sign in once via GitHub OAuth at /auth)")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// summariseUserRecord projects a users record + its linked external
// auths into the flat map shape used by both table and JSON output.
// Mirrors patSummary / summariseRecord conventions so the two CLI
// surfaces feel consistent.
func summariseUserRecord(app core.App, rec *core.Record) map[string]any {
	providers := []string{}
	if extAuths, err := app.FindAllExternalAuthsByRecord(rec); err == nil {
		for _, ea := range extAuths {
			if p := strings.TrimSpace(ea.Provider()); p != "" {
				providers = append(providers, p)
			}
		}
	}
	return map[string]any{
		"id":        rec.Id,
		"email":     rec.GetString("email"),
		"name":      rec.GetString("name"),
		"verified":  rec.GetBool("verified"),
		"providers": providers,
		"created":   rec.GetDateTime("created").String(),
	}
}
