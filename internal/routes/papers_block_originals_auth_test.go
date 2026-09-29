package routes

// papers_block_originals_auth_test.go: the Q1 block-originals endpoints
// ride the /api/papers GET catch-all, so the auth contract is the
// package-wide one — proven here against the real middleware stack:
//
//	anonymous GET (and Range GET)  → 401
//	PAT without papers scope       → 403
//	PAT with papers:read           → passes the gate (503 catalog-unavailable
//	                                 proves the handler was reached)
//	read PAT attempting a write    → 403 (POST catch-all is papers:write)
//
// The mux harness mirrors pat_test.go: build the PocketBase router once
// with RegisterPapers attached and drive it with net/http/httptest.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/pocketbase/pocketbase/tools/types"
)

// blockOriginalsHarness is patHarness's shape with RegisterPapers
// instead of RegisterPAT (same builder, different routes).
type blockOriginalsHarness struct {
	t   testing.TB
	app *tests.TestApp
	mux http.Handler
}

func newBlockOriginalsHarness(t testing.TB) *blockOriginalsHarness {
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

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		// No PostgreSQL pool and no raw store in this harness: auth
		// failures (401/403) happen before any catalog access, and a
		// passed gate lands on the handler's honest 503.
		RegisterPapers(e, &config.Config{}, nil, registry.NewStore(nil), nil, enforcer, nil, nil, nil, nil)
		e.Router.Bind(&hook.Handler[*core.RequestEvent]{
			Func:     func(re *core.RequestEvent) error { return re.Next() },
			Priority: -9999,
		})
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
	_ = router.Router[*core.RequestEvent]{}
	return &blockOriginalsHarness{t: t, app: app, mux: built}
}

func (h *blockOriginalsHarness) do(method, url string, headers map[string]string) (int, []byte, map[string]any) {
	h.t.Helper()
	req := httptest.NewRequest(method, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Result().Body)
	rec.Result().Body.Close()
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return rec.Code, raw, decoded
}

// mintPAT inserts a real pat_tokens record bound to the seeded test
// user (same field shape as patCreateHandler) and returns the plaintext.
func (h *blockOriginalsHarness) mintPAT(t *testing.T, scopes []string) string {
	t.Helper()
	plaintext, prefix, hash, err := pat.Generate()
	if err != nil {
		t.Fatalf("pat.Generate: %v", err)
	}
	user, err := h.app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatalf("seed user lookup: %v", err)
	}
	collection, err := h.app.FindCollectionByNameOrId(pat.CollectionName)
	if err != nil {
		t.Fatalf("pat collection lookup: %v", err)
	}
	rec := core.NewRecord(collection)
	rec.Set("user", user.Id)
	rec.Set("name", "block-originals-test")
	rec.Set("prefix", prefix)
	rec.Set("token_hash", hash)
	scopesJSON, _ := json.Marshal(scopes)
	rec.Set("scopes", string(scopesJSON))
	rec.Set("expires_at", types.NowDateTime().AddDate(0, 0, 7))
	if err := h.app.Save(rec); err != nil {
		t.Fatalf("save pat record: %v", err)
	}
	return plaintext
}

func (h *blockOriginalsHarness) mintScopelessPAT(t *testing.T) string {
	t.Helper()
	return h.mintPAT(t, nil)
}

func (h *blockOriginalsHarness) mintReadPAT(t *testing.T) string {
	t.Helper()
	return h.mintPAT(t, []string{"papers:read"})
}

func TestBlockOriginalsAnonymousGets401(t *testing.T) {
	h := newBlockOriginalsHarness(t)
	for _, path := range []string{
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/sources",
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/parses",
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/parses/pr_x/json",
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/parses/pr_x/blocks",
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/parses/pr_x/blocks/0/1",
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/parses/pr_x/blocks/0/1/image",
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/sources/src_arxiv_v2/pdf",
	} {
		status, _, body := h.do(http.MethodGet, path, nil)
		if status != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401 (body=%v)", path, status, body)
		}
	}
}

func TestBlockOriginalsRangeEquallyAuthenticated(t *testing.T) {
	h := newBlockOriginalsHarness(t)
	status, _, body := h.do(http.MethodGet,
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/sources/src_arxiv_v2/pdf",
		map[string]string{"Range": "bytes=0-99"})
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous Range GET = %d, want 401 (Range 同样鉴权); body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "authentication required") {
		t.Errorf("detail = %v", body["detail"])
	}
}

func TestBlockOriginalsScopelessPATGets403(t *testing.T) {
	h := newBlockOriginalsHarness(t)
	plaintext := h.mintScopelessPAT(t)
	status, _, body := h.do(http.MethodGet,
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/sources",
		map[string]string{"Authorization": "Bearer " + plaintext})
	if status != http.StatusForbidden {
		t.Fatalf("scopeless PAT = %d, want 403; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "papers:read") {
		t.Errorf("detail should name papers:read; got %v", body["detail"])
	}
}

func TestBlockOriginalsReadPATPassesGate(t *testing.T) {
	h := newBlockOriginalsHarness(t)
	plaintext := h.mintReadPAT(t)
	status, _, body := h.do(http.MethodGet,
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/sources",
		map[string]string{"Authorization": "Bearer " + plaintext})
	// No PostgreSQL pool configured → the handler answers its honest
	// 503; anything OTHER than 401/403 proves the papers:read gate
	// opened for the original-asset surface.
	if status != http.StatusServiceUnavailable {
		t.Fatalf("papers:read PAT = %d, want 503 (gate passed, catalog down); body=%v", status, body)
	}
}

func TestBlockOriginalsReadPATCannotWrite(t *testing.T) {
	h := newBlockOriginalsHarness(t)
	plaintext := h.mintReadPAT(t)
	// Any POST under /api/papers is papers:write-guarded; the read PAT
	// must see 403 BEFORE any routing decision.
	status, _, body := h.do(http.MethodPost,
		"/api/papers/qa_01J5SYNTHETICFIXTURE0001/sources",
		map[string]string{"Authorization": "Bearer " + plaintext})
	if status != http.StatusForbidden {
		t.Fatalf("read PAT POST = %d, want 403; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "papers:write") {
		t.Errorf("detail should name papers:write; got %v", body["detail"])
	}
}
