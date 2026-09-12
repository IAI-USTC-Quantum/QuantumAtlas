package match

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMatchSendsBearerAndPayload(t *testing.T) {
	var gotAuth, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		if r.URL.Path != "/v1/match" {
			t.Errorf("path = %q, want /v1/match", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"input":"2401.12345","kind":"arxiv","matched":true,` +
			`"qatlas_id":"qa_01abc","method":"arxiv_exact","paper":{"paper_id":"qa_01abc","has_md":true}}]}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "sekrit", time.Second)
	resp, err := c.Match(context.Background(), Query{
		Inputs: []string{"2401.12345"},
		Author: "nielsen",
		Year:   2010,
	})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if gotAuth != "Bearer sekrit" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if inputs, _ := gotBody["inputs"].([]any); len(inputs) != 1 || inputs[0] != "2401.12345" {
		t.Errorf("inputs = %v", gotBody["inputs"])
	}
	if gotBody["author"] != "nielsen" || gotBody["year"].(float64) != 2010 {
		t.Errorf("author/year = %v/%v", gotBody["author"], gotBody["year"])
	}
	if len(resp.Results) != 1 || !resp.Results[0].Matched || resp.Results[0].QatlasID != "qa_01abc" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Results[0].Paper == nil || !resp.Results[0].Paper.HasMD {
		t.Errorf("paper = %+v", resp.Results[0].Paper)
	}
}

func TestMatchUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail":"db down"}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "", time.Second)
	if _, err := c.Match(context.Background(), Query{Inputs: []string{"x"}}); err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	if err := NewRemoteClient(srv.URL, "", time.Second).Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz: %v", err)
	}
}

func TestNilClientGuards(t *testing.T) {
	var c *RemoteClient
	if err := c.Healthz(context.Background()); err == nil {
		t.Error("nil Healthz should error")
	}
	if _, err := c.Match(context.Background(), Query{}); err == nil {
		t.Error("nil Match should error")
	}
}
