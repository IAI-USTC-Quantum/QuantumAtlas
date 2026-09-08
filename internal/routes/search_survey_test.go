package routes

import (
	"context"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"net/http"
	"testing"
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
