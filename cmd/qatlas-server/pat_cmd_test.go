// Tests for the `pat` cobra subcommand group exposed by pat_cmd.go.
//
// These exercise the same business logic the production main exposes
// (NewPATCommand → app.RootCmd.AddCommand) against a fresh PocketBase
// test app. Each test drives the cobra command via cmd.Execute() and
// asserts on (a) exit status, (b) printed stdout/stderr, and (c) the
// resulting DB state — mirroring how an operator would interact via
// shell.
//
// Why exercise the cobra surface (not just the helper functions)?
// Because the operator UX *is* the contract — flag names, required
// flags, exit codes, where the plaintext lands (stdout vs stderr).
// Regressing any of those would silently break operator workflows
// even if the underlying DB writes still happen correctly.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	// Import internal/pat (and via it, internal/pat/migrations.go's
	// init()) so the pat_tokens collection migration is registered
	// before tests.NewTestApp() applies it.
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// newTestAppWithPATCollection spins up a PocketBase test app and
// returns it together with the (already-migrated) pat_tokens
// collection. Caller is responsible for app.Cleanup().
func newTestAppWithPATCollection(t *testing.T) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	// Sanity: collection exists after migration.
	if _, err := app.FindCollectionByNameOrId(pat.CollectionName); err != nil {
		app.Cleanup()
		t.Fatalf("pat_tokens collection missing after RunAllMigrations: %v", err)
	}
	return app
}

// runPATCmd builds a fresh root pat command, captures stdout/stderr,
// and runs with the given argv. Returns captured buffers and the
// Execute error. Cobra writes "Error: ..." to stderr on RunE errors,
// so even error paths populate stderrBuf.
func runPATCmd(t *testing.T, app core.App, argv ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewPATCommand(app)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(argv)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// seedUser returns the email of the canonical seeded test user.
// PocketBase's test data dir ships with test@example.com on the users
// collection — using it avoids having to wire OAuth or password hash
// setup just to land a parent record.
func seedUserEmail(t *testing.T, app core.App) string {
	t.Helper()
	rec, err := app.FindAuthRecordByEmail(auth.UsersCollection, "test@example.com")
	if err != nil {
		t.Fatalf("seed user lookup failed: %v", err)
	}
	return rec.GetString("email")
}

// ---------------------------------------------------------------------------
// mint
// ---------------------------------------------------------------------------

func TestPATMint_HappyPath(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()

	email := seedUserEmail(t, app)
	stdout, stderr, err := runPATCmd(t, app,
		"mint",
		"--user", email,
		"--name", "ci-nightly",
		"--scopes", "shares:write",
		"--expires-in-days", "365",
		"--description", "for the nightly smoke",
	)
	if err != nil {
		t.Fatalf("mint failed: %v\nstderr: %s", err, stderr)
	}

	// stdout: exactly one line, a qat_-prefixed plaintext.
	plaintext := strings.TrimSpace(stdout)
	if strings.Count(plaintext, "\n") != 0 {
		t.Errorf("stdout should be exactly one line, got %d newlines", strings.Count(stdout, "\n"))
	}
	if !strings.HasPrefix(plaintext, pat.TokenPrefix) {
		t.Errorf("stdout plaintext %q lacks %q prefix", plaintext, pat.TokenPrefix)
	}
	if got, want := len(plaintext), len(pat.TokenPrefix)+24; got != want {
		t.Errorf("stdout plaintext length = %d, want %d", got, want)
	}

	// stderr: summary line. Should NOT contain the plaintext (the
	// machine-friendly contract is "stdout only" so SECRET=$(...)
	// doesn't accidentally also redirect the human chatter).
	if strings.Contains(stderr, plaintext) {
		t.Errorf("stderr leaked the plaintext token; stderr=%q", stderr)
	}
	for _, mustHave := range []string{"minted PAT", "id=", "prefix=", "ci-nightly", "shares:write", email} {
		if !strings.Contains(stderr, mustHave) {
			t.Errorf("stderr summary missing %q; got %q", mustHave, stderr)
		}
	}

	// DB state: exactly one record, scopes JSON-encoded as ["shares:write"].
	records, err := app.FindAllRecords(pat.CollectionName)
	if err != nil {
		t.Fatalf("list pat_tokens: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 pat_tokens record, got %d", len(records))
	}
	rec := records[0]
	if rec.GetString("name") != "ci-nightly" {
		t.Errorf("name = %q, want %q", rec.GetString("name"), "ci-nightly")
	}
	if rec.GetString("description") != "for the nightly smoke" {
		t.Errorf("description = %q, want %q", rec.GetString("description"), "for the nightly smoke")
	}
	if !strings.HasPrefix(rec.GetString("prefix"), pat.TokenPrefix) {
		t.Errorf("stored prefix should begin with %q", pat.TokenPrefix)
	}
	if rec.GetString("token_hash") == "" {
		t.Error("token_hash is empty — bcrypt hash not persisted?")
	}
	var savedScopes []string
	if err := json.Unmarshal([]byte(rec.GetString("scopes")), &savedScopes); err != nil {
		t.Fatalf("scopes column not valid JSON: %v", err)
	}
	if len(savedScopes) != 1 || savedScopes[0] != "shares:write" {
		t.Errorf("scopes = %v, want [shares:write]", savedScopes)
	}
	if rec.GetDateTime("expires_at").Time().IsZero() {
		t.Error("expires_at not set — would render the PAT a perpetual one")
	}

	// Round-trip via Lookup: the plaintext minted by the CLI must
	// be accepted by the same Lookup that authGuard uses on real
	// requests.
	patRec, userRec, lookErr := pat.Lookup(app, plaintext)
	if lookErr != nil {
		t.Fatalf("pat.Lookup(plaintext): %v", lookErr)
	}
	if patRec.Id != rec.Id {
		t.Errorf("Lookup returned record %s, want %s", patRec.Id, rec.Id)
	}
	if userRec.GetString("email") != email {
		t.Errorf("Lookup user email = %q, want %q", userRec.GetString("email"), email)
	}
}

// TestPATMint_RejectsPerpetual is the CLI mirror of the HTTP P14
// contract: --expires-in-days is mandatory (cannot be zero / negative
// / over the 365-day cap). Each branch returns a non-zero exit and
// no record is created.
func TestPATMint_RejectsPerpetual(t *testing.T) {
	cases := []struct {
		name       string
		expiryFlag string
		wantInErr  string
	}{
		{"missing", "0", "expires-in-days"},        // default of 0 trips "required"
		{"negative", "-1", "expires-in-days"},
		{"over max", "999", "365"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestAppWithPATCollection(t)
			defer app.Cleanup()
			email := seedUserEmail(t, app)

			_, stderr, err := runPATCmd(t, app,
				"mint",
				"--user", email,
				"--name", "should-not-exist",
				"--scopes", "shares:write",
				"--expires-in-days", tc.expiryFlag,
			)
			if err == nil {
				t.Fatalf("expected error for --expires-in-days=%s, got nil", tc.expiryFlag)
			}
			if !strings.Contains(stderr, tc.wantInErr) && !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error should mention %q; err=%v stderr=%q", tc.wantInErr, err, stderr)
			}

			// No record persisted on the rejection path.
			records, _ := app.FindAllRecords(pat.CollectionName)
			if len(records) != 0 {
				t.Errorf("rejection path persisted %d records", len(records))
			}
		})
	}
}

func TestPATMint_RejectsUnknownScope(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()
	email := seedUserEmail(t, app)

	_, _, err := runPATCmd(t, app,
		"mint",
		"--user", email,
		"--name", "bogus",
		"--scopes", "definitely:not:a:scope",
		"--expires-in-days", "30",
	)
	if err == nil {
		t.Fatal("expected error for unknown scope, got nil")
	}
	if !strings.Contains(err.Error(), "unknown scope") {
		t.Errorf("error should mention 'unknown scope'; got %v", err)
	}
}

// TestPATMint_RejectsWildcardScope mirrors pat.ValidateScopes' refusal
// to grant the master scope through user input. Operators with shell
// access COULD edit the DB directly to grant "*" — but the CLI path
// must not be that shortcut, so the parallel contract holds.
func TestPATMint_RejectsWildcardScope(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()
	email := seedUserEmail(t, app)

	_, _, err := runPATCmd(t, app,
		"mint",
		"--user", email,
		"--name", "wildcard-attempt",
		"--scopes", "*",
		"--expires-in-days", "30",
	)
	if err == nil {
		t.Fatal("expected error for wildcard scope, got nil")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("error should mention 'wildcard'; got %v", err)
	}
}

func TestPATMint_RejectsUnknownUser(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()

	_, _, err := runPATCmd(t, app,
		"mint",
		"--user", "nobody@example.invalid",
		"--name", "x",
		"--scopes", "shares:write",
		"--expires-in-days", "30",
	)
	if err == nil {
		t.Fatal("expected error for unknown user, got nil")
	}
	if !strings.Contains(err.Error(), "no users record") {
		t.Errorf("error should mention 'no users record'; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func TestPATList_EmptyTable(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()

	stdout, _, err := runPATCmd(t, app, "list")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	// Header row only.
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "SCOPES") {
		t.Errorf("list should print a header row; got %q", stdout)
	}
	// No data rows (would contain qat_).
	if strings.Contains(stdout, pat.TokenPrefix) {
		t.Errorf("empty list should not contain any %q prefix; got %q", pat.TokenPrefix, stdout)
	}
}

func TestPATList_AfterMint(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()
	email := seedUserEmail(t, app)

	if _, _, err := runPATCmd(t, app,
		"mint", "--user", email, "--name", "alpha",
		"--scopes", "shares:write", "--expires-in-days", "30",
	); err != nil {
		t.Fatalf("mint alpha: %v", err)
	}
	if _, _, err := runPATCmd(t, app,
		"mint", "--user", email, "--name", "beta",
		"--scopes", "papers:write", "--expires-in-days", "30",
	); err != nil {
		t.Fatalf("mint beta: %v", err)
	}

	stdout, _, err := runPATCmd(t, app, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"alpha", "beta", "shares:write", "papers:write", email} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q; got %q", want, stdout)
		}
	}
}

func TestPATList_JSONShape(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()
	email := seedUserEmail(t, app)
	if _, _, err := runPATCmd(t, app,
		"mint", "--user", email, "--name", "j",
		"--scopes", "shares:write", "--expires-in-days", "30",
	); err != nil {
		t.Fatalf("mint: %v", err)
	}

	stdout, _, err := runPATCmd(t, app, "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	for _, key := range []string{"id", "user_id", "user_email", "name", "prefix", "scopes", "expires_at", "created"} {
		if _, ok := e[key]; !ok {
			t.Errorf("JSON entry missing key %q", key)
		}
	}
	// Critical: the JSON output MUST NOT include token_hash or any
	// plaintext-derived field beyond the safe display prefix. A
	// regression that pipes the bcrypt hash through `list --json`
	// would still be a confidentiality leak (offline crack target).
	if _, leaked := e["token_hash"]; leaked {
		t.Error("list --json leaked token_hash column")
	}
	if _, leaked := e["plaintext"]; leaked {
		t.Error("list --json leaked plaintext field")
	}
}

// ---------------------------------------------------------------------------
// revoke
// ---------------------------------------------------------------------------

func TestPATRevoke_DropsRecordAndAuth(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()
	email := seedUserEmail(t, app)

	stdoutMint, _, err := runPATCmd(t, app,
		"mint", "--user", email, "--name", "to-revoke",
		"--scopes", "shares:write", "--expires-in-days", "30",
	)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	plaintext := strings.TrimSpace(stdoutMint)

	// PAT works before revocation.
	if _, _, lookErr := pat.Lookup(app, plaintext); lookErr != nil {
		t.Fatalf("pre-revoke Lookup failed: %v", lookErr)
	}

	// Need the record id to revoke. Grab it from the DB.
	records, _ := app.FindAllRecords(pat.CollectionName)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	id := records[0].Id

	stdoutRev, _, err := runPATCmd(t, app, "revoke", id)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !strings.Contains(stdoutRev, "revoked") || !strings.Contains(stdoutRev, id) {
		t.Errorf("revoke output should confirm and name the id; got %q", stdoutRev)
	}

	// PAT no longer works.
	if _, _, lookErr := pat.Lookup(app, plaintext); lookErr == nil {
		t.Fatal("post-revoke Lookup should fail, but succeeded")
	}

	// DB row gone.
	after, _ := app.FindAllRecords(pat.CollectionName)
	if len(after) != 0 {
		t.Errorf("expected 0 records after revoke, got %d", len(after))
	}
}

func TestPATRevoke_UnknownIDIsError(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()

	_, _, err := runPATCmd(t, app, "revoke", "nonexistent_id")
	if err == nil {
		t.Fatal("expected error for unknown id, got nil")
	}
}

// ---------------------------------------------------------------------------
// scopes
// ---------------------------------------------------------------------------

func TestPATScopes_PrintsCanonicalVocabulary(t *testing.T) {
	app := newTestAppWithPATCollection(t)
	defer app.Cleanup()

	stdout, _, err := runPATCmd(t, app, "scopes")
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	// Every scope from AllScopes must appear in the output so an
	// operator running `pat scopes` learns the full vocabulary.
	for _, scope := range pat.AllScopes {
		if !strings.Contains(stdout, scope) {
			t.Errorf("scopes output missing %q; got %q", scope, stdout)
		}
	}
	// The max-expires-in-days hint is the operator's reminder that
	// PATs are not perpetual — its presence is the contract.
	if !strings.Contains(stdout, "max expires-in-days") {
		t.Errorf("scopes output should mention the expiry cap; got %q", stdout)
	}
}
