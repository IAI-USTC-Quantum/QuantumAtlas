// HTTP-layer tests for the /api/admin surface and the adminGuard gate.
//
// What's exercised here:
//
//   - adminGuard: anonymous → 401, non-admin session → 403
//     {"detail":"admin only"}, admin session → through to the handler
//     (503 on the schema endpoint because the test pool is nil — the
//     registry-unavailable contract)
//   - sessionGuard semantics underneath: a user PAT minted by an
//     admin-listed account is STILL rejected (403, "browser session
//     token") — admin surfaces are for humans, a leaked PAT must not
//     reach them
//   - /api/admin/whoami: any session gets {login, is_admin}; non-admin
//     gets is_admin:false rather than a 403
//   - fetchDBSchema: live-PostgreSQL test gated on QATLAS_TEST_PG_DSN
//     (same skip convention as internal/registry integration tests)
//
// The harness mirrors patHarness (see pat_test.go): one mux built from
// OnServe, a do() helper for repeated requests.
package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// adminTestLogin is the GitHub login the test config allowlists.
const adminTestLogin = "Agony5757"

// adminHarness is patHarness with the /api/admin routes mounted behind
// a cfg that lists adminTestLogin as the sole admin. The schema route
// gets a nil pool, so an admin-cleared request exercises the 503
// registry-unavailable path without needing a live Postgres. The mineru
// routes get a real scheduler over a DISABLED converter (paper access
// off), so admin-cleared requests exercise the started=false /
// converter_disabled path without MinerU traffic.
type adminHarness struct {
	*patHarness
	cfg   *config.Config
	sched *mineru.Scheduler
}

func newAdminHarness(t testing.TB) *adminHarness {
	t.Helper()
	return newAdminHarnessWith(t, nil, nil)
}

// newAdminHarnessWith is newAdminHarness with an injectable plugin
// registry and remote-search provider for the /api/admin/plugins
// surface (see admin_plugins_test.go). Nil values exercise the
// degraded paths (empty plugin list, 503 proxy).
func newAdminHarnessWith(t testing.TB, pluginRegistry *qplugin.Registry, remote *search.RemoteProvider) *adminHarness {
	t.Helper()
	h := &adminHarness{
		patHarness: &patHarness{t: t},
		cfg:        &config.Config{AdminGitHubLogins: []string{adminTestLogin}},
	}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app

	// Real scheduler, disabled converter: Enabled() is false, so RunNow
	// refuses with "converter_disabled" and never touches the (nil)
	// queue or MinerU.
	disabledConv := mineru.NewConverter(mineru.ConverterConfig{PaperAccessEnabled: false}, nil, nil, nil)
	h.sched = mineru.NewScheduler(disabledConv, nil, nil)
	t.Cleanup(h.sched.Stop)

	// The auth package migration must have added github_login to the
	// users collection, otherwise the whole admin design is broken.
	usersCol, err := app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		t.Fatalf("users collection missing after migrations: %v", err)
	}
	if usersCol.Fields.GetByName(auth.GitHubLoginField) == nil {
		t.Fatalf("users collection missing %q field after migrations", auth.GitHubLoginField)
	}

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterAdmin(e, h.cfg, app, nil, h.sched, usage.NewStore(nil), pluginRegistry, remote)
		m, mErr := e.Router.BuildMux()
		if mErr != nil {
			return mErr
		}
		built = m
		return nil
	})
	if err != nil {
		t.Fatalf("OnServe trigger: %v", err)
	}
	if built == nil {
		t.Fatal("mux not built by OnServe trigger")
	}
	h.mux = built
	return h
}

// adminSessionToken creates a users record with the allowlisted GitHub
// login stamped on github_login and returns a fresh session token for
// it. This is exactly the state the OAuth hook produces for an admin
// after sign-in (see stampGitHubLogin).
func (h *adminHarness) adminSessionToken() string {
	h.t.Helper()
	col, err := h.app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		h.t.Fatalf("find users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail("admin@example.com")
	rec.SetPassword("admin-test-password")
	rec.Set(auth.GitHubLoginField, adminTestLogin)
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save admin user: %v", err)
	}
	token, err := rec.NewAuthToken()
	if err != nil {
		h.t.Fatalf("NewAuthToken: %v", err)
	}
	return token
}

// mintPATForAuth creates a pat_tokens row directly (bypassing the
// session-gated /api/pat handler) bound to the record that owns the
// given session token, and returns the plaintext. Used to prove that
// even an admin-listed account's PAT is rejected by the admin gate.
func (h *adminHarness) mintPATForAuth(sessionTok string) string {
	h.t.Helper()
	user, err := h.app.FindAuthRecordByToken(sessionTok, core.TokenTypeAuth)
	if err != nil {
		h.t.Fatalf("resolve session token owner: %v", err)
	}
	col, err := h.app.FindCollectionByNameOrId(pat.CollectionName)
	if err != nil {
		h.t.Fatalf("find pat_tokens collection: %v", err)
	}
	plaintext, prefix, hash, err := pat.Generate()
	if err != nil {
		h.t.Fatalf("pat.Generate: %v", err)
	}
	rec := core.NewRecord(col)
	rec.Set("user", user.Id)
	rec.Set("name", "admin-pat")
	rec.Set("prefix", prefix)
	rec.Set("token_hash", hash)
	rec.Set("scopes", `["papers:write"]`)
	rec.Set("expires_at", types.NowDateTime().AddDate(0, 0, 30))
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save pat record: %v", err)
	}
	return plaintext
}

// ---------------------------------------------------------------------------
// Gate matrix
// ---------------------------------------------------------------------------

func TestAPI_Admin_RejectsAnonymous(t *testing.T) {
	h := newAdminHarness(t)
	for _, url := range []string{"/api/admin/whoami", "/api/admin/db/schema"} {
		status, _, body := h.do(http.MethodGet, url, "", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401; body=%v", url, status, body)
		}
	}
}

func TestAPI_Admin_WhoamiNonAdminSession(t *testing.T) {
	h := newAdminHarness(t)
	// Seeded test@example.com user has no github_login stamped.
	status, _, body := h.do(http.MethodGet, "/api/admin/whoami", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if body["is_admin"] != false {
		t.Errorf("is_admin = %v, want false", body["is_admin"])
	}
	if login := asString(body["login"]); login != "" {
		t.Errorf("login = %q, want empty (no github_login on record)", login)
	}
}

func TestAPI_Admin_WhoamiAdminSession(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/whoami", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if body["is_admin"] != true {
		t.Errorf("is_admin = %v, want true", body)
	}
	if login := asString(body["login"]); login != adminTestLogin {
		t.Errorf("login = %q, want %q", login, adminTestLogin)
	}
}

func TestAPI_Admin_SchemaRejectsNonAdminSession(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/db/schema", "", rawHeader(h.sessionToken()))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
	if asString(body["detail"]) != "admin only" {
		t.Errorf("detail = %q, want %q", body["detail"], "admin only")
	}
}

func TestAPI_Admin_SchemaAdminSessionReachesHandler(t *testing.T) {
	h := newAdminHarness(t)
	// Admin clears the gate; the handler itself reports 503 because the
	// harness mounts a nil pool (QATLAS_POSTGRES_DSN unset equivalent).
	status, _, body := h.do(http.MethodGet, "/api/admin/db/schema", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nil pool); body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "QATLAS_POSTGRES_DSN") {
		t.Errorf("detail should mention QATLAS_POSTGRES_DSN; got %q", body["detail"])
	}
}

// TestAPI_Admin_RejectsPATAuthEvenForAdmin is the admin analogue of the
// PAT sessionGuard contract: a PAT owned by an admin-listed account must
// NOT reach admin endpoints. 403 from sessionGuard (not "admin only" —
// the session check runs first).
func TestAPI_Admin_RejectsPATAuthEvenForAdmin(t *testing.T) {
	h := newAdminHarness(t)
	sessionTok := h.adminSessionToken()
	plaintext := h.mintPATForAuth(sessionTok)

	for _, url := range []string{"/api/admin/whoami", "/api/admin/db/schema"} {
		status, _, body := h.do(http.MethodGet, url, "", bearerHeader(plaintext))
		if status != http.StatusForbidden {
			t.Errorf("GET %s: status = %d, want 403; body=%v", url, status, body)
		}
		if !strings.Contains(asString(body["detail"]), "browser session token") {
			t.Errorf("GET %s: detail should mention 'browser session token'; got %q", url, body["detail"])
		}
	}
}

// ---------------------------------------------------------------------------
// fetchDBSchema against a live PostgreSQL (DSN-gated)
// ---------------------------------------------------------------------------

func TestFetchDBSchema_LivePostgres(t *testing.T) {
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping live-PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// A throwaway table exercising every reported feature: pk column,
	// non-null default, secondary index, CHECK + UNIQUE constraints.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS admin_schema_probe (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			title text NOT NULL DEFAULT 'untitled',
			note text,
			UNIQUE (title),
			CHECK (length(title) > 0)
		);
		CREATE INDEX IF NOT EXISTS idx_admin_schema_probe_note ON admin_schema_probe (note)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS admin_schema_probe"); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
	})

	schema, err := fetchDBSchema(ctx, pool)
	if err != nil {
		t.Fatalf("fetchDBSchema: %v", err)
	}
	if schema.Database == "" {
		t.Error("database name is empty")
	}

	var probe *adminSchemaTable
	for i := range schema.Tables {
		if schema.Tables[i].Name == "admin_schema_probe" {
			probe = &schema.Tables[i]
			break
		}
	}
	if probe == nil {
		t.Fatalf("probe table not found in schema report (%d tables)", len(schema.Tables))
	}
	if probe.TotalSize == "" {
		t.Error("total_size is empty")
	}

	cols := map[string]adminSchemaColumn{}
	for _, c := range probe.Columns {
		cols[c.Name] = c
	}
	id, ok := cols["id"]
	if !ok {
		t.Fatalf("id column missing; got %v", cols)
	}
	if !id.IsPK {
		t.Error("id.is_pk = false, want true")
	}
	if id.Nullable {
		t.Error("id.nullable = true, want false")
	}
	title, ok := cols["title"]
	if !ok {
		t.Fatalf("title column missing; got %v", cols)
	}
	if title.DataType != "text" {
		t.Errorf("title.data_type = %q, want text", title.DataType)
	}
	if title.Nullable {
		t.Error("title.nullable = true, want false")
	}
	if title.Default == nil || *title.Default == "" {
		t.Errorf("title.default missing, want 'untitled'::text-ish; got %v", title.Default)
	}
	if note, ok := cols["note"]; !ok || !note.Nullable || note.IsPK {
		t.Errorf("note column wrong: %+v (ok=%v)", note, ok)
	}

	foundIdx := false
	for _, idx := range probe.Indexes {
		if idx.Name == "idx_admin_schema_probe_note" {
			foundIdx = true
			if idx.Definition == "" {
				t.Error("index definition empty")
			}
		}
	}
	if !foundIdx {
		t.Errorf("secondary index missing from report: %+v", probe.Indexes)
	}

	kinds := map[string]bool{}
	for _, c := range probe.Constraints {
		kinds[c.Kind] = true
		if c.Name == "" || c.Definition == "" {
			t.Errorf("constraint with empty name/definition: %+v", c)
		}
	}
	for _, want := range []string{"PRIMARY KEY", "UNIQUE", "CHECK"} {
		if !kinds[want] {
			t.Errorf("constraint kind %s missing; got %v", want, kinds)
		}
	}

	// Alphabetical ordering contract.
	for i := 1; i < len(schema.Tables); i++ {
		if schema.Tables[i-1].Name > schema.Tables[i].Name {
			t.Fatalf("tables not ordered: %q before %q", schema.Tables[i-1].Name, schema.Tables[i].Name)
		}
	}
}

// jsonRoundTrip guards against accidental contract drift in the response
// types (e.g. a renamed json tag would fail the frontend later).
func TestAdminDBSchemaResponse_JSONShape(t *testing.T) {
	resp := adminDBSchemaResponse{
		Database: "qatlas",
		Tables: []adminSchemaTable{{
			Name:        "papers",
			RowEstimate: 42,
			TotalSize:   "16 kB",
			Columns:     []adminSchemaColumn{{Name: "id", DataType: "text", Nullable: false, Default: nil, IsPK: true}},
			Indexes:     []adminSchemaIndex{{Name: "papers_pkey", Definition: "CREATE UNIQUE INDEX ..."}},
			Constraints: []adminSchemaConstraint{{Name: "papers_pkey", Kind: "PRIMARY KEY", Definition: "PRIMARY KEY (id)"}},
		}},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["database"] != "qatlas" {
		t.Errorf("database key wrong: %v", decoded)
	}
	table := decoded["tables"].([]any)[0].(map[string]any)
	for _, key := range []string{"name", "row_estimate", "total_size", "columns", "indexes", "constraints"} {
		if _, ok := table[key]; !ok {
			t.Errorf("table object missing key %q", key)
		}
	}
	col := table["columns"].([]any)[0].(map[string]any)
	for _, key := range []string{"name", "data_type", "nullable", "default", "is_pk"} {
		if _, ok := col[key]; !ok {
			t.Errorf("column object missing key %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// /api/admin/mineru/* — scheduler trigger + status
// ---------------------------------------------------------------------------

// TestAPI_Admin_MineruRunGate: the manual trigger follows the same
// adminGuard contract as db/schema — anonymous 401, non-admin session
// 403 "admin only".
func TestAPI_Admin_MineruRunGate(t *testing.T) {
	h := newAdminHarness(t)

	status, _, _ := h.do(http.MethodPost, "/api/admin/mineru/run", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", status)
	}

	status, _, body := h.do(http.MethodPost, "/api/admin/mineru/run", "", rawHeader(h.sessionToken()))
	if status != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want 403; body=%v", status, body)
	}
	if asString(body["detail"]) != "admin only" {
		t.Errorf("detail = %q, want %q", body["detail"], "admin only")
	}
}

// TestAPI_Admin_MineruStatusGate: same gate matrix for the status
// endpoint.
func TestAPI_Admin_MineruStatusGate(t *testing.T) {
	h := newAdminHarness(t)

	status, _, _ := h.do(http.MethodGet, "/api/admin/mineru/status", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", status)
	}

	status, _, body := h.do(http.MethodGet, "/api/admin/mineru/status", "", rawHeader(h.sessionToken()))
	if status != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want 403; body=%v", status, body)
	}
	if asString(body["detail"]) != "admin only" {
		t.Errorf("detail = %q, want %q", body["detail"], "admin only")
	}
}

// TestAPI_Admin_MineruRunDisabledConverter: an admin clears the gate;
// with the harness's disabled converter the trigger reports
// started=false, reason=converter_disabled, and embeds a snapshot.
func TestAPI_Admin_MineruRunDisabledConverter(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodPost, "/api/admin/mineru/run", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if body["started"] != false {
		t.Errorf("started = %v, want false (converter disabled)", body["started"])
	}
	if asString(body["reason"]) != "converter_disabled" {
		t.Errorf("reason = %q, want %q", body["reason"], "converter_disabled")
	}
	snap, ok := body["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot missing or wrong type: %v", body["snapshot"])
	}
	if snap["running"] != false {
		t.Errorf("snapshot.running = %v, want false", snap["running"])
	}
	if snap["daily_cap"] != float64(mineru.DefaultDailyCap) {
		t.Errorf("snapshot.daily_cap = %v, want %d", snap["daily_cap"], mineru.DefaultDailyCap)
	}
}

// TestAPI_Admin_MineruStatus: admin gets the raw scheduler snapshot.
func TestAPI_Admin_MineruStatus(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/mineru/status", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if body["running"] != false {
		t.Errorf("running = %v, want false", body["running"])
	}
	if body["daily_cap"] != float64(mineru.DefaultDailyCap) {
		t.Errorf("daily_cap = %v, want %d", body["daily_cap"], mineru.DefaultDailyCap)
	}
	if body["converted_today"] != float64(0) {
		t.Errorf("converted_today = %v, want 0", body["converted_today"])
	}
	if asString(body["cap_day"]) == "" {
		t.Error("cap_day is empty, want a YYYY-MM-DD date")
	}
}

// ---------------------------------------------------------------------------
// /api/admin/usage|plans|quotas — registration smoke + gate matrix
// ---------------------------------------------------------------------------

// TestAPI_Admin_UsageSurfaceGate: the metering endpoints follow the same
// adminGuard contract as db/schema — anonymous 401, non-admin session
// 403 "admin only". Registration itself must not panic (the harness
// mounting proves it).
func TestAPI_Admin_UsageSurfaceGate(t *testing.T) {
	h := newAdminHarness(t)

	endpoints := []struct {
		method, url string
	}{
		{http.MethodGet, "/api/admin/usage"},
		{http.MethodGet, "/api/admin/plans"},
		{http.MethodPut, "/api/admin/plans/pro"},
		{http.MethodPut, "/api/admin/quotas/some-user-id"},
	}
	for _, ep := range endpoints {
		status, _, _ := h.do(ep.method, ep.url, "", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous: status = %d, want 401", ep.method, ep.url, status)
		}
		status, _, body := h.do(ep.method, ep.url, `{"daily_agentic_search_limit":1}`, rawHeader(h.sessionToken()))
		if status != http.StatusForbidden {
			t.Errorf("%s %s non-admin: status = %d, want 403; body=%v", ep.method, ep.url, status, body)
		}
	}
}

// TestAPI_Admin_UsageNilPool: an admin clears the gate; with the
// harness's nil-pool usage store the endpoints report the
// catalog-unavailable 503 convention.
func TestAPI_Admin_UsageNilPool(t *testing.T) {
	h := newAdminHarness(t)
	tok := rawHeader(h.adminSessionToken())

	status, _, body := h.do(http.MethodGet, "/api/admin/usage", "", tok)
	if status != http.StatusServiceUnavailable {
		t.Errorf("GET usage: status = %d, want 503; body=%v", status, body)
	}
	status, _, body = h.do(http.MethodGet, "/api/admin/plans", "", tok)
	if status != http.StatusServiceUnavailable {
		t.Errorf("GET plans: status = %d, want 503; body=%v", status, body)
	}
}

// TestAPI_Admin_UsageBadDay: the day query param is validated before the
// store is touched, so even the nil-pool harness can exercise the 400.
func TestAPI_Admin_UsageBadDay(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/usage?day=not-a-date", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "YYYY-MM-DD") {
		t.Errorf("detail = %q, want mention of YYYY-MM-DD", body["detail"])
	}
}

// TestAPI_Admin_PlanValidation: limit validation happens before the
// store is touched (nil pool tolerated).
func TestAPI_Admin_PlanValidation(t *testing.T) {
	h := newAdminHarness(t)
	tok := rawHeader(h.adminSessionToken())

	status, _, body := h.do(http.MethodPut, "/api/admin/plans/pro", `{"daily_agentic_search_limit":-1}`, tok)
	if status != http.StatusBadRequest {
		t.Errorf("negative limit: status = %d, want 400; body=%v", status, body)
	}
	status, _, body = h.do(http.MethodPut, "/api/admin/plans/pro", `{"description":"no limit field"}`, tok)
	if status != http.StatusBadRequest {
		t.Errorf("missing limit: status = %d, want 400; body=%v", status, body)
	}
}

// TestResolveUserLogins: logins resolve from the users collection;
// unknown ids are left empty.
func TestResolveUserLogins(t *testing.T) {
	h := newAdminHarness(t)
	_ = h.adminSessionToken() // seeds an admin user with github_login stamped

	rows := []usage.UserUsage{{UserID: "no-such-user-id"}}
	logins := resolveUserLogins(h.app, rows)
	if logins["no-such-user-id"] != "" {
		t.Errorf("unknown user login = %q, want empty", logins["no-such-user-id"])
	}
}
