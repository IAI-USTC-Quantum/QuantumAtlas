package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

const scorerJSON = `{"language":"qatlas-expr-v1","filter":"true","score":"lexical"}`
const rankedJSON = `{"text":"quantum","sources":["arxiv"],"max_results":10,"scorer":` + scorerJSON + `,"explain":true}`
const generateJSON = `{"query":"quantum","requirements":"recent papers"}`

type fakeScoringBackend struct {
	genCalls, rankedCalls int
	genReq                search.ScorerGenerateRequest
	rankedReq             search.RankedSearchRequest
	keys                  map[string]string
	genErr, rankedErr     error
	genResp               search.ScorerGenerateResponse
	rankedResp            search.RankedSearchResponse
	onGenerate            func()
}

func (f *fakeScoringBackend) ScoringCapabilities(context.Context) (map[string]any, error) {
	return map[string]any{"language": "qatlas-expr-v1", "generation_available": true}, nil
}
func (f *fakeScoringBackend) GenerateScorer(_ context.Context, r search.ScorerGenerateRequest) (search.ScorerGenerateResponse, error) {
	f.genCalls++
	f.genReq = r
	if f.onGenerate != nil {
		f.onGenerate()
	}
	return f.genResp, f.genErr
}
func (f *fakeScoringBackend) SearchRanked(_ context.Context, r search.RankedSearchRequest, keys map[string]string) (search.RankedSearchResponse, error) {
	f.rankedCalls++
	f.rankedReq = r
	f.keys = keys
	return f.rankedResp, f.rankedErr
}

type fakeScoringMeter struct {
	limit                int
	allowed              bool
	reserves, refunds    int
	tokens               int64
	refundContextErr     error
	tokenContext         context.Context
	sharedCleanupContext bool
}

func (f *fakeScoringMeter) EffectiveLimit(context.Context, string, int) (int, error) {
	return f.limit, nil
}
func (f *fakeScoringMeter) CheckAndReserve(context.Context, string, string, int) (int, bool, error) {
	f.reserves++
	return 3, f.allowed, nil
}
func (f *fakeScoringMeter) Refund(ctx context.Context, _ string, _ string) error {
	f.refunds++
	f.refundContextErr = ctx.Err()
	f.sharedCleanupContext = ctx == f.tokenContext
	return nil
}
func (f *fakeScoringMeter) RecordTokens(ctx context.Context, _ string, _ string, tokens int64) error {
	f.tokenContext = ctx
	f.tokens += tokens
	return nil
}

func newScoringHarness(t testing.TB, backend ScoringBackend, meter ScoringMeter) *multiHarness {
	t.Helper()
	h := &multiHarness{patHarness: &patHarness{t: t}}
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app
	h.keys = userkeys.NewStore(app, "scoring-test-secret")
	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	se := &core.ServeEvent{App: app, Router: router}
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterSearchScoring(e, &config.Config{}, h.keys, backend, meter, search.NewEngine(nil, nil), nil, enforcer)
		RegisterMeSearchKeys(e, h.keys)
		h.mux, err = e.Router.BuildMux()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestScoringAuthAndCapabilities(t *testing.T) {
	backend := &fakeScoringBackend{}
	h := newScoringHarness(t, backend, nil)
	for _, path := range []string{"/api/search/scoring/generate", "/api/search/ranked"} {
		status, _, _ := h.do("POST", path, `{}`, nil)
		if status != 401 {
			t.Fatalf("anonymous %s: %d", path, status)
		}
	}
	status, _, body := h.do("GET", "/api/search/scoring/capabilities", "", rawHeader(h.sessionToken()))
	if status != 200 || body["generation_available"] != false {
		t.Fatalf("caps without meter %d %v", status, body)
	}
	status, _, _ = h.do("POST", "/api/search/scoring/generate", generateJSON, rawHeader(h.sessionToken()))
	if status != 503 || backend.genCalls != 0 {
		t.Fatalf("meter guard: %d", status)
	}
	disabled := newScoringHarness(t, nil, nil)
	status, _, _ = disabled.do("POST", "/api/search/ranked", rankedJSON, rawHeader(disabled.sessionToken()))
	if status != 503 {
		t.Fatalf("disabled %d", status)
	}
	const secret = "scoring-system-pat-test-secret"
	system, err := pat.LoadSystemPAT(secret, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	UseSystemPAT(system)
	t.Cleanup(func() { UseSystemPAT(nil) })
	status, _, _ = h.do("POST", "/api/search/scoring/generate", generateJSON, bearerHeader(secret))
	if status != 403 {
		t.Fatalf("system PAT generation: %d", status)
	}
}

func TestScoringGenerationMetering(t *testing.T) {
	backend := &fakeScoringBackend{genResp: search.ScorerGenerateResponse{Scorer: json.RawMessage(scorerJSON), Summary: "relevance", ScorerHash: "abc", FeatureVersion: "v1", Usage: search.RemoteUsage{LLMTokens: 42}}}
	meter := &fakeScoringMeter{limit: 10, allowed: true}
	h := newScoringHarness(t, backend, meter)
	status, _, body := h.do("POST", "/api/search/scoring/generate", generateJSON, rawHeader(h.sessionToken()))
	if status != 200 || meter.reserves != 1 || meter.refunds != 0 || meter.tokens != 42 || backend.genCalls != 1 {
		t.Fatalf("generate: %d %v %+v", status, body, meter)
	}
	u := body["usage"].(map[string]any)
	if u["today"] != float64(3) || u["limit"] != float64(10) || u["llm_tokens"] != float64(42) {
		t.Fatal(u)
	}
	if body["warnings"] == nil {
		t.Fatal("null warnings")
	}
	backend.genResp = search.ScorerGenerateResponse{}
	backend.genErr = &search.ScoringRemoteError{Status: 422, Detail: search.ScoringErrorDetail{Code: "generation_failed", Message: "Cannot generate a valid scorer"}, Usage: search.RemoteUsage{LLMTokens: 11}}
	status, _, body = h.do("POST", "/api/search/scoring/generate", generateJSON, rawHeader(h.sessionToken()))
	if status != 422 || meter.refunds != 1 || meter.tokens != 53 || meter.sharedCleanupContext || body["usage"].(map[string]any)["today"] != float64(2) {
		t.Fatalf("failure refund %d %v %+v", status, body, meter)
	}
	meter.allowed = false
	status, _, _ = h.do("POST", "/api/search/scoring/generate", generateJSON, rawHeader(h.sessionToken()))
	if status != 429 || backend.genCalls != 2 {
		t.Fatalf("quota %d calls %d", status, backend.genCalls)
	}
	meter.limit = 0
	prior := meter.reserves
	status, _, _ = h.do("POST", "/api/search/scoring/generate", generateJSON, rawHeader(h.sessionToken()))
	if status != 429 || meter.reserves != prior {
		t.Fatal("zero quota reserved")
	}
}

func TestScoringGenerationRefundSurvivesDisconnect(t *testing.T) {
	backend := &fakeScoringBackend{genErr: errors.New("transport failed")}
	meter := &fakeScoringMeter{limit: 10, allowed: true}
	h := newScoringHarness(t, backend, meter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend.onGenerate = cancel
	req := httptest.NewRequest("POST", "/api/search/scoring/generate", strings.NewReader(generateJSON)).WithContext(ctx)
	for k, v := range rawHeader(h.sessionToken()) {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if meter.refunds != 1 || meter.refundContextErr != nil {
		t.Fatalf("refund failed with cancelled ctx: %+v", meter)
	}
}

func TestScoringRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"generate", `{"query":"x","requirements":"x","api_keys":{"x":"secret"}}`, 400},
		{"generate", `{"query":"","requirements":"x"}`, 400},
		{"generate", generateJSON + ` {}`, 400},
		{"generate", `{"query":"` + strings.Repeat("x", 4001) + `","requirements":"x"}`, 400},
		{"generate", strings.Repeat(" ", maxScoringBody+1), 413},
		{"ranked", `{"text":"x","sources":["arxiv"],"scorer":null}`, 422},
		{"ranked", strings.Replace(rankedJSON, `"explain":true`, `"explain":null`, 1), 422},
		{"ranked", `{"text":"x","sources":["arxiv"],"scorer":` + scorerJSON + `,"api_keys":{"x":"secret"}}`, 400},
		{"ranked", `{"text":"x","sources":["arxiv"],"scorer":` + scorerJSON + `,"max_results":101}`, 400},
		{"ranked", `{"text":"x","sources":[],"scorer":` + scorerJSON + `}`, 400},
	} {
		t.Run(tc.path+tc.body[:min(len(tc.body), 30)], func(t *testing.T) {
			backend := &fakeScoringBackend{}
			meter := &fakeScoringMeter{limit: 10, allowed: true}
			h := newScoringHarness(t, backend, meter)
			path := "/api/search/ranked"
			if tc.path == "generate" {
				path = "/api/search/scoring/generate"
			}
			status, _, body := h.do("POST", path, tc.body, rawHeader(h.sessionToken()))
			if status != tc.status || backend.genCalls+backend.rankedCalls != 0 || meter.reserves != 0 {
				t.Fatalf("%d %v %+v", status, body, meter)
			}
		})
	}
}

func TestRankedPreservesUnifiedOrderAndKeys(t *testing.T) {
	backend := &fakeScoringBackend{rankedResp: search.RankedSearchResponse{Ranking: json.RawMessage(`{"source":"scorer"}`)}}
	// More than the legacy MaxCandidates: none may disappear or be reordered.
	for i := 0; i < 8; i++ {
		hit := search.RemoteHit{Title: "Title only", Score: float64(4 - i), Source: "arxiv", ScoreExplanation: json.RawMessage(`{"value":2}`), ScoreDetail: json.RawMessage(`null`)}
		if i == 3 {
			hit.DOI = "10.1234/test"
		}
		backend.rankedResp.Hits = append(backend.rankedResp.Hits, hit)
	}
	meter := &fakeScoringMeter{limit: 10, allowed: true}
	h := newScoringHarness(t, backend, meter)
	auth := rawHeader(h.sessionToken())
	status, _, _ := h.do("PUT", "/api/me/search-keys/ieee", `{"key":"key-ieee"}`, auth)
	if status != 200 {
		t.Fatalf("seed keys %d", status)
	}
	status, _, body := h.do("POST", "/api/search/ranked", strings.ReplaceAll(rankedJSON, "arxiv", "ieee"), auth)
	if status != 200 {
		t.Fatalf("ranked %d %v", status, body)
	}
	hits := body["hits"].([]any)
	if len(hits) != 8 || body["remote"] != true || meter.reserves != 0 {
		t.Fatalf("lost candidates/metered: %v", body)
	}
	for i, item := range hits {
		hit := item.(map[string]any)
		if hit["score"] != float64(4-i) || hit["score_explanation"] == nil {
			t.Fatal(hit)
		}
	}
	if backend.keys["ieee"] != "key-ieee" || !backend.rankedReq.Explain || string(backend.rankedReq.Scorer) != scorerJSON {
		t.Fatalf("bad forwarding %+v", backend)
	}
	if strings.Contains(asJSON(body), "key-ieee") {
		t.Fatal("key leaked")
	}
	backend.rankedErr = &search.ScoringRemoteError{Status: 422, Detail: search.ScoringErrorDetail{Code: "type", Message: "Expected number", Field: "score"}}
	status, _, body = h.do("POST", "/api/search/ranked", rankedJSON, auth)
	if status != 422 || body["detail"].(map[string]any)["code"] != "type" {
		t.Fatal(body)
	}
}

func asJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func TestScoringAdmissionBounded(t *testing.T) {
	gate := &scoringAdmission{totalLimit: 2, userLimit: 1}
	release, ok := gate.acquire("a")
	if !ok {
		t.Fatal("first admission failed")
	}
	if _, ok := gate.acquire("a"); ok {
		t.Fatal("user limit bypassed")
	}
	releaseB, ok := gate.acquire("b")
	if !ok {
		t.Fatal("second user blocked")
	}
	if _, ok := gate.acquire("c"); ok {
		t.Fatal("global limit bypassed")
	}
	release()
	releaseB()
	if gate.active != 0 || len(gate.users) != 0 {
		t.Fatalf("gate leaked: %+v", gate)
	}
}

func TestScoringRateWindowsAreBounded(t *testing.T) {
	gate := &scoringAdmission{totalLimit: 2, userLimit: 1, perMinute: 2}
	for i := 0; i < 2; i++ {
		release, ok := gate.acquire("a")
		if !ok {
			t.Fatal("early rate limit")
		}
		release()
	}
	if _, ok := gate.acquire("a"); ok {
		t.Fatal("sequential calls bypass minute limit")
	}
	if len(gate.users) != 0 || gate.active != 0 {
		t.Fatal("active state leaked")
	}
	// Capacity must not grow with arbitrary authenticated user identifiers.
	for i := 0; i < 4096; i++ {
		gate.windows[string(rune(i))+"other"] = scoringRateWindow{start: time.Now(), count: 1}
	}
	if _, ok := gate.acquire("unseen"); ok {
		t.Fatal("rate window capacity bypassed")
	}
	for id := range gate.windows {
		gate.windows[id] = scoringRateWindow{start: time.Now().Add(-2 * time.Minute), count: 1}
	}
	release, ok := gate.acquire("unseen")
	if !ok {
		t.Fatal("expired windows not collected")
	}
	release()
	if len(gate.windows) != 1 {
		t.Fatalf("expired window leak: %d", len(gate.windows))
	}
}

func TestScoringJSONOverRealTCP(t *testing.T) {
	backend := &fakeScoringBackend{rankedResp: search.RankedSearchResponse{Hits: []search.RemoteHit{}, Ranking: json.RawMessage(`{"source":"scorer"}`)}}
	h := newScoringHarness(t, backend, nil)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	for _, body := range []string{rankedJSON, rankedJSON + ` {}`} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/search/ranked", strings.NewReader(body))
		for k, v := range rawHeader(h.sessionToken()) {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 200
		if body != rankedJSON {
			want = 400
		}
		if resp.StatusCode != want {
			t.Fatalf("TCP status %d want %d", resp.StatusCode, want)
		}
	}
}
