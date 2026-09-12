// HTTP-layer tests for POST /api/papers/match: the harness mirrors
// newMultiHarness (one PB test app, do() helper); the qatlas-match
// microservice is faked so the whole flow runs offline.

package routes

import (
	"context"
	"net/http"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/match"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

type fakeMatchBackend struct {
	lastQuery match.Query
	resp      match.MatchResponse
	err       error
}

func (f *fakeMatchBackend) Match(_ context.Context, q match.Query) (match.MatchResponse, error) {
	f.lastQuery = q
	return f.resp, f.err
}

func newMatchHarness(t testing.TB, backend MatchBackend) *patHarness {
	t.Helper()
	h := &patHarness{t: t}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app

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
		RegisterPaperMatch(e, backend, enforcer)
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

func TestAPI_PaperMatch_RejectsAnonymous(t *testing.T) {
	h := newMatchHarness(t, &fakeMatchBackend{})
	status, _, _ := h.do(http.MethodPost, "/api/papers/match", `{"inputs":["2401.12345"]}`, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestAPI_PaperMatch_NoBackend(t *testing.T) {
	h := newMatchHarness(t, nil)
	status, _, body := h.do(http.MethodPost, "/api/papers/match", `{"inputs":["2401.12345"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%v", status, body)
	}
	if asString(body["detail"]) != "match requires the qatlas-match microservice (match.remote)" {
		t.Errorf("detail = %q", body["detail"])
	}
}

func TestAPI_PaperMatch_EmptyRequest(t *testing.T) {
	h := newMatchHarness(t, &fakeMatchBackend{})
	status, _, _ := h.do(http.MethodPost, "/api/papers/match", `{}`, rawHeader(h.sessionToken()))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestAPI_PaperMatch_ForwardsAndReturns(t *testing.T) {
	fake := &fakeMatchBackend{
		resp: match.MatchResponse{Results: []match.MatchResult{{
			Input:    "2401.12345",
			Kind:     "arxiv",
			Matched:  true,
			QatlasID: "qa_01abc",
			Method:   "arxiv_exact",
			Paper:    &match.MatchPaper{PaperID: "qa_01abc", Title: "A Paper", HasMD: true},
		}}},
	}
	h := newMatchHarness(t, fake)

	body := `{"inputs":[" 2401.12345 ","2401.12345","10.1/x"],"title":"  Some Title ","year":2010}`
	status, _, resp := h.do(http.MethodPost, "/api/papers/match", body, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d; body=%v", status, resp)
	}
	if len(fake.lastQuery.Inputs) != 2 || fake.lastQuery.Inputs[0] != "2401.12345" {
		t.Errorf("forwarded inputs = %v (want trimmed+deduped)", fake.lastQuery.Inputs)
	}
	if fake.lastQuery.Title != "Some Title" || fake.lastQuery.Year != 2010 {
		t.Errorf("forwarded title/year = %q/%d", fake.lastQuery.Title, fake.lastQuery.Year)
	}
	results := resp["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	first := results[0].(map[string]any)
	if first["matched"] != true || first["qatlas_id"] != "qa_01abc" || first["method"] != "arxiv_exact" {
		t.Errorf("first = %v", first)
	}
	paper := first["paper"].(map[string]any)
	if paper["has_md"] != true || paper["title"] != "A Paper" {
		t.Errorf("paper = %v", paper)
	}
}

func TestAPI_PaperMatch_UpstreamFailure(t *testing.T) {
	fake := &fakeMatchBackend{err: context.DeadlineExceeded}
	h := newMatchHarness(t, fake)
	status, _, _ := h.do(http.MethodPost, "/api/papers/match", `{"inputs":["x"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
}
