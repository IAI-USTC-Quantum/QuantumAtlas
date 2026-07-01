package openalex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchWorkRecord_OpenAlexID: an openalex ref hits /works/W… (no "doi:"
// prefix) and returns the raw record + parsed work.
func TestFetchWorkRecord_OpenAlexID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	raw, work, err := r.FetchWorkRecord(context.Background(), "openalex", "W12345")
	if err != nil {
		t.Fatalf("FetchWorkRecord: %v", err)
	}
	if gotPath != "/works/W12345" {
		t.Errorf("request path = %q, want /works/W12345", gotPath)
	}
	if !strings.Contains(string(raw), "W12345") {
		t.Errorf("raw record missing id: %s", raw)
	}
	if shortID(work.ID) != "W12345" {
		t.Errorf("parsed work id = %q, want W12345", shortID(work.ID))
	}
}

// TestFetchWorkRecord_DOI: a doi ref hits /works/doi:<doi> (doi lower-cased
// by normalizeDOI, "doi:" kept as an unescaped path prefix).
func TestFetchWorkRecord_DOI(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	_, _, err := r.FetchWorkRecord(context.Background(), "doi", "10.1103/PhysRevLett.103.150502")
	if err != nil {
		t.Fatalf("FetchWorkRecord: %v", err)
	}
	if gotPath != "/works/doi:10.1103/physrevlett.103.150502" {
		t.Errorf("request path = %q, want /works/doi:10.1103/physrevlett.103.150502", gotPath)
	}
}

// TestFetchWorkRecord_NotConfigured: no mailto → fail fast, no HTTP.
func TestFetchWorkRecord_NotConfigured(t *testing.T) {
	r := New(Config{})
	if _, _, err := r.FetchWorkRecord(context.Background(), "openalex", "W1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

// TestFetchWorkRecord_ArxivUnsupported: arxiv is not a /works/{id} key.
func TestFetchWorkRecord_ArxivUnsupported(t *testing.T) {
	r := New(Config{Mailto: "ops@example.com"})
	if _, _, err := r.FetchWorkRecord(context.Background(), "arxiv", "2401.00001"); !errors.Is(err, ErrDOINotFound) {
		t.Errorf("err = %v, want ErrDOINotFound (arxiv unsupported)", err)
	}
}

// TestFetchWorkRecord_NotFound: upstream 404 → ErrDOINotFound.
func TestFetchWorkRecord_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	if _, _, err := r.FetchWorkRecord(context.Background(), "openalex", "W404"); !errors.Is(err, ErrDOINotFound) {
		t.Errorf("err = %v, want ErrDOINotFound", err)
	}
}
