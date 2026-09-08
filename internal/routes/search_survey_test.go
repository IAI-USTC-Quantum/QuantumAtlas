package routes

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

type fakeSurveyBackend struct {
	fakeMultiBackend
	request search.SurveyRequest
	keys    map[string]string
}

func (f *fakeSurveyBackend) SearchSurvey(_ context.Context, request search.SurveyRequest, keys map[string]string) (search.SurveyResponse, error) {
	f.request = request
	f.keys = keys
	return search.SurveyResponse{Hits: []search.RemoteHit{}, Coverage: map[string]any{"exhaustive": false}}, nil
}
func TestSurveyRejectsCredentialInjectionAndForwardsRules(t *testing.T) {
	backend := &fakeSurveyBackend{}
	h := newMultiHarness(t, backend, nil, nil)
	auth := rawHeader(h.sessionToken())
	status, _, _ := h.do(http.MethodPost, "/api/search/survey", `{"goal":"quantum","sources":["semantic_scholar"],"api_keys":{"semantic_scholar":"forged"}}`, auth)
	if status != 400 {
		t.Fatalf("credential injection: %d", status)
	}
	status, _, _ = h.do(http.MethodPost, "/api/search/survey", `{"goal":"quantum","sources":["semantic_scholar"],"queries":["QSVT"],"rules":{"authors":["Dong An"],"year_from":2025}}`, auth)
	if status != 200 || backend.request.Goal != "quantum" || len(backend.request.Rules) == 0 {
		t.Fatalf("forwarding: %d %+v", status, backend.request)
	}
}

func TestSurveyAgenticRequiresMetering(t *testing.T) {
	backend := &fakeSurveyBackend{}
	h := newMultiHarness(t, backend, nil, nil)
	status, _, _ := h.do(http.MethodPost, "/api/search/survey", `{"goal":"quantum","sources":["semantic_scholar"],"agentic":true}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unmetered planner: %d", status)
	}
	if backend.request.Goal != "" {
		t.Fatal("planner called without metering")
	}
}

// PocketBase v0.38 wraps every request body in a RereadableReadCloser that
// rewinds on EOF. Over real TCP (net/http server bodies return EOF together
// with the final bytes) a trailing-garbage check via a second Decode on the
// raw request body sees the *replayed* body as a second JSON value — mux-direct
// tests never exercise that path. This test pins the real-wire behavior.
func TestSurveyTrailingGarbageCheckOverRealWire(t *testing.T) {
	backend := &fakeSurveyBackend{}
	h := newMultiHarness(t, backend, nil, nil)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	auth := rawHeader(h.sessionToken())

	post := func(body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/search/survey", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range auth {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(raw)
	}

	// Single valid JSON object must NOT be misread as "multiple JSON values".
	status, body := post(`{"goal":"quantum","sources":["semantic_scholar"]}`)
	if status != 200 {
		t.Fatalf("single value over wire: %d %s", status, body)
	}
	// Actual trailing garbage must still be rejected.
	status, body = post(`{"goal":"x","sources":["arxiv"]} {}`)
	if status != 400 {
		t.Fatalf("trailing garbage over wire: %d %s", status, body)
	}
}
