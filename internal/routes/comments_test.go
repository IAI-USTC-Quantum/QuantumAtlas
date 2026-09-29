// comments_test.go — Q2 acceptance suite (plan §8 Q2 验收清单) driven
// over the real mux with the in-memory comments store and the Q0
// synthetic fixture anchors:
//
//   - A creates a discussion, B replies; A cannot edit B's body, C
//     cannot change status; the root author and a platform admin can.
//   - Retracting one discussion never touches another on the same block.
//   - Idempotent create/reply replay; same key + different body → 409.
//   - If-Match CAS edits: stale revision → 409.
//   - System PAT writes → 403 (reads allowed).
//   - Anonymous → 401; PAT without comments:write → 403.
//   - Anchor misses (§12.5 golden anchors) → 404, never nearest-block.
//   - Body char limit → 413; request size limit → 413.
package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/comments"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// fixturePaper / fixture revisions are the Q0 synthetic anchor world
// (plan §12.5): rev-a has non-contiguous blocks 1,2,5 on page 0 and
// page 1 only on rev-a; rev-b lacks page 1 entirely.
const (
	fixturePaper     = "qa_01J5SYNTHETICFIXTURE0001"
	fixtureRevisionA = "rev-fixture-a"
	fixtureRevisionB = "rev-fixture-b"
)

type commentsHarness struct {
	t        testing.TB
	app      *tests.TestApp
	mux      http.Handler
	store    *comments.MemStore
	idem     *comments.Idempotency
	enforcer *casbin.Enforcer
	cfg      *config.Config
}

func newCommentsHarness(t testing.TB) *commentsHarness {
	t.Helper()
	return newCommentsHarnessCfg(t, &config.Config{})
}

func newCommentsHarnessCfg(t testing.TB, cfg *config.Config) *commentsHarness {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	dir := filepath.Join("..", "..", "tests", "fixtures", "blockcomments")
	load := func(name string) []byte {
		raw, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			t.Fatalf("read fixture %s: %v", name, rerr)
		}
		return raw
	}
	anchors, err := comments.NewFixtureValidator(map[string][]byte{
		fixtureRevisionA: load("parse-a.middle.json"),
		fixtureRevisionB: load("parse-b.middle.json"),
	})
	if err != nil {
		t.Fatalf("fixture anchors: %v", err)
	}

	store := comments.NewMemStore()
	idem := comments.NewIdempotency(128)

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterComments(e, cfg, store, idem, anchors, enforcer)
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
		t.Fatal("mux not built")
	}
	return &commentsHarness{t: t, app: app, mux: built, store: store, idem: idem, enforcer: enforcer, cfg: cfg}
}

func (h *commentsHarness) do(method, url, body string, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	h.t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, url, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Result().Body)
	rec.Result().Body.Close()
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return rec, decoded
}

// userWithPAT creates a users record (optionally admin) and returns
// (user id, PAT plaintext) with the given scopes.
func (h *commentsHarness) userWithPAT(login string, admin bool, scopes string) (string, string) {
	h.t.Helper()
	col, err := h.app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		h.t.Fatalf("find users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail(login + "@example.com")
	rec.SetPassword("comments-test-password")
	rec.Set(auth.GitHubLoginField, login)
	rec.Set(auth.IsAdminField, admin)
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save user %s: %v", login, err)
	}
	plaintext, prefix, hash, err := pat.Generate()
	if err != nil {
		h.t.Fatalf("pat.Generate: %v", err)
	}
	patCol, err := h.app.FindCollectionByNameOrId(pat.CollectionName)
	if err != nil {
		h.t.Fatalf("find pat_tokens collection: %v", err)
	}
	patRec := core.NewRecord(patCol)
	patRec.Set("user", rec.Id)
	patRec.Set("name", "comments-test")
	patRec.Set("prefix", prefix)
	patRec.Set("token_hash", hash)
	patRec.Set("scopes", scopes)
	patRec.Set("expires_at", types.NowDateTime().AddDate(1, 0, 0))
	if err := h.app.Save(patRec); err != nil {
		h.t.Fatalf("save pat: %v", err)
	}
	return rec.Id, plaintext
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func mergeHeaders(m map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

const createBodyA = `{
	"parse_revision": "` + fixtureRevisionA + `",
	"page_idx": 0,
	"block_index": 2,
	"type": "transcription_error",
	"scope": "public",
	"status": "pending",
	"reason": "sqrt range looks off",
	"body": "The equation transcription misses the overline.",
	"model": "test-model"
}`

// TestQ2_CreateReplyPermissionsAndStatus walks the core §8 Q2
// acceptance: A creates, B replies, ownership matrix on body edits and
// status changes, admin override, and independence of two discussions
// anchored on the same block.
func TestQ2_CreateReplyPermissionsAndStatus(t *testing.T) {
	h := newCommentsHarness(t)
	userA, patA := h.userWithPAT("alice", false, `["papers:read","comments:write"]`)
	userB, patB := h.userWithPAT("bob", false, `["comments:write"]`)
	_, patC := h.userWithPAT("carol", false, `["papers:read","comments:write"]`)
	_, patAdmin := h.userWithPAT("dana-admin", true, `["comments:write"]`)
	_, _, _ = userA, userB, patC

	// A creates discussion 1 (with an initial status + reason).
	rec, body := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", createBodyA, bearer(patA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201, body=%v", rec.Code, body)
	}
	d1 := body["discussion_id"].(string)
	if body["created_by"] != userA {
		t.Fatalf("created_by = %v, want server-stamped %s", body["created_by"], userA)
	}
	if body["status"] != "pending" || body["type"] != "transcription_error" {
		t.Fatalf("created discussion shape wrong: %v", body)
	}
	if rev, _ := body["revision"].(float64); rev != 1 {
		t.Fatalf("revision = %v, want 1", body["revision"])
	}

	// B replies to A's discussion.
	rec, replyBody := h.do(http.MethodPost, "/api/discussions/"+d1+"/replies",
		`{"body":"Checked against the original page: it is a misread.","model":"bob-model"}`, bearer(patB))
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply status = %d, want 201, body=%v", rec.Code, replyBody)
	}
	replyID := replyBody["reply_id"].(string)
	if replyBody["created_by"] != userB {
		t.Fatalf("reply created_by = %v, want %s", replyBody["created_by"], userB)
	}

	// A cannot edit B's reply body (author-only, §12.1.2).
	rec, resp := h.do(http.MethodPatch, "/api/discussions/"+d1+"/replies/"+replyID+"/body",
		`{"body":"A tampers with B"}`,
		mergeHeaders(bearer(patA), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("A editing B's reply: status = %d, want 403", rec.Code)
	}

	// A edits own body with fresh CAS.
	rec, resp = h.do(http.MethodPatch, "/api/discussions/"+d1+"/body",
		`{"body":"The equation transcription misses the overline (edited)."}`,
		mergeHeaders(bearer(patA), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("A editing own body: status = %d, want 200, body=%v", rec.Code, resp)
	}
	if resp["body"] != "The equation transcription misses the overline (edited)." || resp["revision"].(float64) != 2 {
		t.Fatalf("edited body wrong: %v", resp)
	}
	if etag := rec.Header().Get("ETag"); etag != "2" {
		t.Fatalf("ETag = %q, want 2", etag)
	}

	// Stale CAS on the next edit → 409.
	rec, _ = h.do(http.MethodPatch, "/api/discussions/"+d1+"/body",
		`{"body":"stale write"}`,
		mergeHeaders(bearer(patA), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale CAS: status = %d, want 409", rec.Code)
	}

	// C (not author, not admin) cannot change status.
	rec, _ = h.do(http.MethodPatch, "/api/discussions/"+d1+"/status",
		`{"status":"retracted","reason":"carol says so"}`, bearer(patC))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("C status change: status = %d, want 403", rec.Code)
	}
	// Status without reason → 400 even for the author.
	rec, _ = h.do(http.MethodPatch, "/api/discussions/"+d1+"/status",
		`{"status":"confirmed"}`, bearer(patA))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status without reason: %d, want 400", rec.Code)
	}

	// A (root author) retracts discussion 1.
	rec, resp = h.do(http.MethodPatch, "/api/discussions/"+d1+"/status",
		`{"status":"retracted","reason":"reply checked the original; misread"}`, bearer(patA))
	if rec.Code != http.StatusOK || resp["status"] != "retracted" {
		t.Fatalf("A retraction: status=%d body=%v", rec.Code, resp)
	}

	// A creates a second discussion on the SAME block; it must be
	// completely independent of the first.
	rec, resp = h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`",
		"page_idx": 0, "block_index": 2,
		"type": "normal", "scope": "lean",
		"body": "A note about the适用范围 of this equation."
	}`, bearer(patA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create 2: status=%d body=%v", rec.Code, resp)
	}
	d2 := resp["discussion_id"].(string)

	// Admin changes status via a USER PAT (role flags on the users
	// record — NOT adminGuard, which would reject the PAT): reopen d1.
	rec, resp = h.do(http.MethodPatch, "/api/discussions/"+d1+"/status",
		`{"status":"pending","reason":"admin reopened pending new evidence"}`, bearer(patAdmin))
	if rec.Code != http.StatusOK || resp["status"] != "pending" {
		t.Fatalf("admin reopen: status=%d body=%v", rec.Code, resp)
	}

	// d2 remains statusless and untouched by d1's churn.
	rec, detail := h.do(http.MethodGet, "/api/discussions/"+d2, "", bearer(patB))
	if rec.Code != http.StatusOK || detail["status"] != nil {
		t.Fatalf("d2 after churn: status=%d body=%v", rec.Code, detail["status"])
	}
	// And d1 kept both status events in order.
	rec, hist := h.do(http.MethodGet, "/api/discussions/"+d1+"/revisions", "", bearer(patA))
	if rec.Code != http.StatusOK {
		t.Fatalf("revisions: %d", rec.Code)
	}
	events := hist["status_events"].([]any)
	if len(events) != 3 { // initial pending + retracted + reopened pending
		t.Fatalf("status events = %d, want 3: %v", len(events), events)
	}
	last := events[2].(map[string]any)
	if last["to"] != "pending" || last["actor"] == "" || last["reason"] == "" {
		t.Fatalf("last event shape wrong: %v", last)
	}
	// Body revisions include the root edit.
	revs := hist["body_revisions"].([]any)
	if len(revs) != 1 {
		t.Fatalf("body revisions = %d, want 1", len(revs))
	}
	rv := revs[0].(map[string]any)
	if rv["target"] != "discussion" || rv["old_body"] == "" || rv["new_body"] == "" {
		t.Fatalf("body revision shape wrong: %v", rv)
	}
}

// TestQ2_SystemPATWriteForbidden: system PATs may read comments but
// every write path 403s (plan §12.1.3).
func TestQ2_SystemPATWriteForbidden(t *testing.T) {
	h := newCommentsHarness(t)
	sysSecret := "system-pat-secret-value-long-enough-1234"
	sysPAT, err := pat.LoadSystemPAT(sysSecret, []string{"*"})
	if err != nil {
		t.Fatalf("LoadSystemPAT: %v", err)
	}
	UseSystemPAT(sysPAT)
	t.Cleanup(func() { UseSystemPAT(nil) })
	authSys := bearer(sysSecret)

	rec, body := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", createBodyA, authSys)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("system PAT create: status = %d, want 403, body=%v", rec.Code, body)
	}
	if !strings.Contains(fmt.Sprint(body["detail"]), "read-only for comments") {
		t.Errorf("detail should explain the system-PAT read-only policy: %v", body["detail"])
	}
	rec, _ = h.do(http.MethodPatch, "/api/discussions/cd_x/status",
		`{"status":"confirmed","reason":"x"}`, authSys)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("system PAT status: %d, want 403", rec.Code)
	}
	rec, _ = h.do(http.MethodPost, "/api/discussions/cd_x/replies", `{"body":"x"}`, authSys)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("system PAT reply: %d, want 403", rec.Code)
	}
	rec, _ = h.do(http.MethodPatch, "/api/discussions/cd_x/body", `{"body":"x"}`, authSys)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("system PAT body edit: %d, want 403", rec.Code)
	}

	// Reads are fine with the system PAT.
	rec, body = h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions", "", authSys)
	if rec.Code != http.StatusOK {
		t.Fatalf("system PAT read: %d, want 200, body=%v", rec.Code, body)
	}
}

// TestQ2_AnonymousAndScopeGuards: 401 anonymous, 403 without
// comments:write (write) / without either read scope (read).
func TestQ2_AnonymousAndScopeGuards(t *testing.T) {
	h := newCommentsHarness(t)
	_, patReadOnly := h.userWithPAT("reader", false, `["papers:read"]`)
	_, patCommentsWrite := h.userWithPAT("writer2", false, `["comments:write"]`)

	rec, _ := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", createBodyA, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d, want 401", rec.Code)
	}
	rec, _ = h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d, want 401", rec.Code)
	}

	// papers:read can read but not write.
	rec, _ = h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions", "", bearer(patReadOnly))
	if rec.Code != http.StatusOK {
		t.Fatalf("papers:read list: %d, want 200", rec.Code)
	}
	rec, _ = h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", createBodyA, bearer(patReadOnly))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("papers:read create: %d, want 403", rec.Code)
	}

	// comments:write implies comments:read for the paper-scoped list.
	rec, _ = h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions", "", bearer(patCommentsWrite))
	if rec.Code != http.StatusOK {
		t.Fatalf("comments:write list: %d, want 200", rec.Code)
	}
}

// TestQ2_IdempotencyAndAnchors: replay semantics + golden-anchor
// misses.
func TestQ2_IdempotencyAndAnchors(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("idem-a", false, `["comments:write"]`)
	hdr := mergeHeaders(bearer(patA), map[string]string{"Idempotency-Key": "key-1"})

	rec1, body1 := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", createBodyA, hdr)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec1.Code, body1)
	}
	// Same key + same request → replay of the original 201 result.
	rec2, body2 := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", createBodyA, hdr)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay: %d, want 201", rec2.Code)
	}
	if body1["discussion_id"] != body2["discussion_id"] {
		t.Fatalf("replay created a second discussion: %v vs %v", body1["discussion_id"], body2["discussion_id"])
	}
	if rec2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay should be flagged via Idempotency-Replayed")
	}
	// Same key + different body → 409.
	other := strings.Replace(createBodyA, "overline", "underline", 1)
	rec3, _ := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", other, hdr)
	if rec3.Code != http.StatusConflict {
		t.Fatalf("key reuse with different body: %d, want 409", rec3.Code)
	}

	// Exactly one discussion exists.
	rec, list := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions", "", bearer(patA))
	if rec.Code != http.StatusOK || len(list["items"].([]any)) != 1 {
		t.Fatalf("list after idempotent retries: %d items=%v", rec.Code, list["items"])
	}

	// Golden-anchor misses → 404 (never a nearest-block fallback):
	// block 3 does not exist in parse-A page 1 (indexes 1,2,5).
	rec, resp := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 3, "body": "x"}`, bearer(patA))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("anchor miss block 3: %d, want 404 (%v)", rec.Code, resp)
	}
	// parse-B lacks page 2 entirely.
	rec, _ = h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionB+`", "page_idx": 1, "block_index": 1, "body": "x"}`, bearer(patA))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("anchor miss page 2 on parse B: %d, want 404", rec.Code)
	}
	// Unknown revision → 404 (Q2 fixture-first semantics).
	rec, _ = h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "rev-unknown", "page_idx": 0, "block_index": 1, "body": "x"}`, bearer(patA))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown revision: %d, want 404", rec.Code)
	}
}

// TestQ2_BodyLimits: character budget → 413, request-size budget → 413.
func TestQ2_BodyLimits(t *testing.T) {
	h := newCommentsHarnessCfg(t, &config.Config{CommentsMaxBodyChars: 10, CommentsMaxRequestBytes: 512})
	_, patA := h.userWithPAT("limit-a", false, `["comments:write"]`)

	// Body over the 10-char budget.
	rec, resp := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 1,
		"body": "this body is way longer than ten characters"}`, bearer(patA))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: %d, want 413 (%v)", rec.Code, resp)
	}

	// Request body over the byte budget (a JSON doc > 512 bytes with
	// each field under the char budget).
	big := strings.Repeat("a", 470)
	rec, _ = h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions",
		`{"parse_revision":"`+fixtureRevisionA+`","page_idx":0,"block_index":1,"model":"`+big+`","body":"short"}`, bearer(patA))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize request: %d, want 413", rec.Code)
	}
}

// TestQ2_ListFiltersAndPagination: scope/status/type/anchor filters,
// lean-includes-public semantics, keyset cursor, per_page clamp.
func TestQ2_ListFiltersAndPagination(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("filter-a", false, `["papers:read","comments:write"]`)

	mk := func(ty, sc, body string) string {
		rec, resp := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
			"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 2,
			"type": "`+ty+`", "scope": "`+sc+`", "body": "`+body+`"}`, bearer(patA))
		if rec.Code != http.StatusCreated {
			t.Fatalf("mk %s/%s: %d %v", ty, sc, rec.Code, resp)
		}
		return resp["discussion_id"].(string)
	}
	mk("normal", "public", "one")
	mk("typo_in_original", "lean", "two")
	mk("transcription_error", "public", "three")

	list := func(q string) []any {
		rec, resp := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions"+q, "", bearer(patA))
		if rec.Code != http.StatusOK {
			t.Fatalf("list %q: %d %v", q, rec.Code, resp)
		}
		return resp["items"].([]any)
	}

	all := list("")
	if len(all) != 3 {
		t.Fatalf("no filter: %d items, want 3", len(all))
	}
	lean := list("?scope=lean")
	if len(lean) != 3 {
		t.Fatalf("scope=lean should include public+lean: %d items", len(lean))
	}
	pub := list("?scope=public")
	if len(pub) != 2 {
		t.Fatalf("scope=public: %d items, want 2", len(pub))
	}
	if got := list("?type=typo_in_original"); len(got) != 1 {
		t.Fatalf("type filter: %d", len(got))
	}
	if got := list("?status=none"); len(got) != 3 {
		t.Fatalf("status=none (all statusless): %d", len(got))
	}
	if got := list("?block_index=2"); len(got) != 3 {
		t.Fatalf("anchor filter: %d", len(got))
	}
	if got := list("?block_index=5"); len(got) != 0 {
		t.Fatalf("anchor filter empty: %d", len(got))
	}
	if _, resp := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?scope=secret", "", bearer(patA)); resp == nil {
		t.Fatal("scope=secret should decode")
	}
	rec, _ := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?scope=secret", "", bearer(patA))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad scope: %d, want 400", rec.Code)
	}
	rec, _ = h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?status=closed", "", bearer(patA))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d, want 400", rec.Code)
	}
	rec, _ = h.do(http.MethodGet, "/api/papers/not-qa/discussions", "", bearer(patA))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad paper id: %d, want 400", rec.Code)
	}

	// Keyset pagination: per_page=2 → first page + cursor, second page
	// returns the remainder, no overlap.
	rec, p1 := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?per_page=2", "", bearer(patA))
	if rec.Code != http.StatusOK || len(p1["items"].([]any)) != 2 {
		t.Fatalf("page 1: %d %v", rec.Code, p1)
	}
	cursor, _ := p1["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("page 1 must return a next_cursor")
	}
	rec, p2 := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?per_page=2&cursor="+cursor, "", bearer(patA))
	if rec.Code != http.StatusOK || len(p2["items"].([]any)) != 1 {
		t.Fatalf("page 2: %d %v", rec.Code, p2)
	}
	if p2["next_cursor"] != nil {
		t.Fatalf("last page next_cursor = %v, want nil", p2["next_cursor"])
	}
	id1 := p1["items"].([]any)[0].(map[string]any)["discussion_id"]
	id2 := p2["items"].([]any)[0].(map[string]any)["discussion_id"]
	if id1 == id2 {
		t.Fatal("pages overlap")
	}
}

// TestQ2_DetailRepliesPagination: detail carries the replies page and
// ETag=revision; replies keyset pagination walks chronologically.
func TestQ2_DetailRepliesPagination(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("det-a", false, `["comments:write"]`)

	rec, d := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 1,
		"body": "root"}`, bearer(patA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec.Code, d)
	}
	id := d["discussion_id"].(string)
	for i := 0; i < 3; i++ {
		rec, _ := h.do(http.MethodPost, "/api/discussions/"+id+"/replies",
			fmt.Sprintf(`{"body":"reply %d"}`, i), bearer(patA))
		if rec.Code != http.StatusCreated {
			t.Fatalf("reply %d: %d", i, rec.Code)
		}
	}

	rec, det := h.do(http.MethodGet, "/api/discussions/"+id, "", bearer(patA))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d", rec.Code)
	}
	if etag := rec.Header().Get("ETag"); etag != "1" {
		t.Fatalf("ETag = %q, want 1", etag)
	}
	if det["reply_count"].(float64) != 3 {
		t.Fatalf("reply_count = %v", det["reply_count"])
	}

	// Replies pagination.
	rec, p1 := h.do(http.MethodGet, "/api/discussions/"+id+"?per_page=2", "", bearer(patA))
	replies1 := p1["replies"].([]any)
	if len(replies1) != 2 {
		t.Fatalf("replies page 1: %d", len(replies1))
	}
	rc, _ := p1["replies_next_cursor"].(string)
	if rc == "" {
		t.Fatal("replies_next_cursor missing")
	}
	rec, p2 := h.do(http.MethodGet, "/api/discussions/"+id+"?per_page=2&cursor="+rc, "", bearer(patA))
	replies2 := p2["replies"].([]any)
	if len(replies2) != 1 {
		t.Fatalf("replies page 2: %d", len(replies2))
	}
	first := replies1[0].(map[string]any)["body"].(string)
	last := replies2[0].(map[string]any)["body"].(string)
	if first != "reply 0" || last != "reply 2" {
		t.Fatalf("chronological order broken: %q … %q", first, last)
	}

	// Missing discussion → 404, not an empty 200.
	rec, _ = h.do(http.MethodGet, "/api/discussions/cd_missing", "", bearer(patA))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing detail: %d, want 404", rec.Code)
	}
	// Reply to a missing discussion → 404.
	rec, _ = h.do(http.MethodPost, "/api/discussions/cd_missing/replies", `{"body":"x"}`, bearer(patA))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reply to missing: %d, want 404", rec.Code)
	}
	// Edit without If-Match → 400.
	rec, _ = h.do(http.MethodPatch, "/api/discussions/"+id+"/body", `{"body":"x"}`, bearer(patA))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("edit without If-Match: %d, want 400", rec.Code)
	}
}

// TestQ2_AdminViaUserPATNotAdminGuard: an admin-flagged USER PAT is
// authorized for status changes (unlike /api/admin where sessionGuard
// rejects every PAT). A plain admin session would work too; the point
// under test is that the permission reads the role flags off the
// caller's users record, not adminGuard's PAT rejection.
func TestQ2_AdminViaUserPATNotAdminGuard(t *testing.T) {
	h := newCommentsHarness(t)
	userA, patA := h.userWithPAT("plain-a", false, `["comments:write"]`)
	_, patAdmin := h.userWithPAT("root-admin", true, `["comments:write"]`)

	rec, d := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 1,
		"body": "root by A"}`, bearer(patA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	id := d["discussion_id"].(string)
	_ = userA

	// Non-author non-admin already covered; admin user PAT here:
	rec, resp := h.do(http.MethodPatch, "/api/discussions/"+id+"/status",
		`{"status":"confirmed","reason":"admin verified against the original"}`, bearer(patAdmin))
	if rec.Code != http.StatusOK || resp["status"] != "confirmed" {
		t.Fatalf("admin user PAT status change: %d %v", rec.Code, resp)
	}

	// The same admin still cannot edit someone else's BODY (§12.1.2:
	// per-body authorship, not role).
	rec, _ = h.do(http.MethodPatch, "/api/discussions/"+id+"/body", `{"body":"admin rewrite"}`,
		mergeHeaders(bearer(patAdmin), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin editing another author's body: %d, want 403", rec.Code)
	}
}

// TestQ2_ReplyAuthorEditsOwnReply: B edits own reply; A's identical
// attempt 403s (already covered above) — here we assert the successful
// path and its revision trail.
func TestQ2_ReplyAuthorEditsOwnReply(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("own-a", false, `["comments:write"]`)
	userB, patB := h.userWithPAT("own-b", false, `["comments:write"]`)

	rec, d := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 1,
		"body": "root"}`, bearer(patA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	id := d["discussion_id"].(string)
	rec, r := h.do(http.MethodPost, "/api/discussions/"+id+"/replies", `{"body":"first take"}`, bearer(patB))
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply: %d", rec.Code)
	}
	replyID := r["reply_id"].(string)

	rec, edited := h.do(http.MethodPatch, "/api/discussions/"+id+"/replies/"+replyID+"/body",
		`{"body":"first take (corrected)"}`,
		mergeHeaders(bearer(patB), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusOK || edited["revision"].(float64) != 2 {
		t.Fatalf("own reply edit: %d %v", rec.Code, edited)
	}
	if edited["created_by"] != userB {
		t.Fatalf("created_by changed by edit: %v", edited["created_by"])
	}

	// History now records the reply revision under the discussion.
	rec, hist := h.do(http.MethodGet, "/api/discussions/"+id+"/revisions", "", bearer(patA))
	revs := hist["body_revisions"].([]any)
	if len(revs) != 1 || revs[0].(map[string]any)["target"] != "reply" {
		t.Fatalf("reply revision missing from history: %v", revs)
	}
}
