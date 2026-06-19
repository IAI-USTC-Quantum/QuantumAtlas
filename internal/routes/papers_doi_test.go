package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
)

func TestParseAuthorsForm(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"Seth Lloyd", []string{"Seth Lloyd"}},
		{"Harrow; Hassidim; Lloyd", []string{"Harrow", "Hassidim", "Lloyd"}},
		{"Lloyd, Seth\nHarrow, Aram", []string{"Lloyd, Seth", "Harrow, Aram"}},
		{" A ;; B ", []string{"A", "B"}},
	}
	for _, c := range cases {
		if got := parseAuthorsForm(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseAuthorsForm(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTitlesMatch(t *testing.T) {
	if !titlesMatch("Quantum Algorithm for Linear Systems", "quantum algorithm for linear systems") {
		t.Error("case/space-insensitive title should match")
	}
	if !titlesMatch("Quantum Algorithm", "Quantum Algorithm: A Review") {
		t.Error("containment should match (subtitle tolerated)")
	}
	if titlesMatch("Quantum Algorithm", "Classical Methods in Optics") {
		t.Error("unrelated titles should not match")
	}
	if titlesMatch("", "anything") {
		t.Error("empty expected should not match")
	}
}

func TestAuthorsMatch(t *testing.T) {
	actual := []string{"Aram W. Harrow", "Avinatan Hassidim", "Seth Lloyd"}
	if !authorsMatch([]string{"Harrow"}, actual) {
		t.Error("surname-only should match")
	}
	if !authorsMatch([]string{"A. W. Harrow", "Lloyd, Seth"}, actual) {
		t.Error("mixed formats should match on surname")
	}
	if authorsMatch([]string{"Einstein"}, actual) {
		t.Error("absent author should not match")
	}
	if authorsMatch([]string{"Harrow"}, nil) {
		t.Error("no actual authors should not match")
	}
	if authorsMatch([]string{"   ", "\t"}, actual) {
		t.Error("all-empty-surname expected should not match (no real check happened)")
	}
}

func TestStrictReject(t *testing.T) {
	// Warn-mode rejection lives at the call site; strictReject itself
	// only sees statuses from already-gated strict callers.
	if r := strictReject(papers.VerifyMismatch); r == nil || r.Status != http.StatusConflict {
		t.Errorf("strict mismatch should 409, got %v", r)
	}
	if r := strictReject(papers.VerifyDOINotFound); r == nil || r.Status != http.StatusConflict {
		t.Errorf("strict doi-not-found should 409, got %v", r)
	}
	if r := strictReject(papers.VerifyUnavailable); r == nil || r.Status != http.StatusServiceUnavailable {
		t.Errorf("strict unavailable should 503, got %v", r)
	}
	if strictReject(papers.VerifyVerified) != nil {
		t.Error("strict verified should proceed")
	}
	if strictReject(papers.VerifyRecorded) != nil {
		t.Error("strict recorded should proceed")
	}
}

func stubResolver(t *testing.T, body string, status int) *openalex.Resolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return openalex.New(openalex.Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
}

const hhlBody = `{
  "id": "https://openalex.org/W12345",
  "title": "Quantum algorithm for linear systems of equations",
  "authorships": [
    {"author": {"display_name": "Aram W. Harrow"}},
    {"author": {"display_name": "Avinatan Hassidim"}},
    {"author": {"display_name": "Seth Lloyd"}}
  ],
  "locations": [{"landing_page_url": "https://arxiv.org/abs/0811.3171", "pdf_url": ""}]
}`

func TestVerifyDOIMetadata(t *testing.T) {
	ctx := context.Background()
	doi := "10.1103/PhysRevLett.103.150502"

	t.Run("unconfigured when resolver disabled", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, openalex.New(openalex.Config{}), doi, "x", nil)
		if v.Status != papers.VerifyUnconfigured {
			t.Errorf("got %q", v.Status)
		}
	})
	t.Run("unconfigured when nil", func(t *testing.T) {
		if v := verifyDOIMetadata(ctx, nil, doi, "x", nil); v.Status != papers.VerifyUnconfigured {
			t.Errorf("got %q", v.Status)
		}
	})
	t.Run("recorded when no expected provided (no OpenAlex call)", func(t *testing.T) {
		// resolver must NOT be hit when nothing was supplied to cross-
		// check against — verifyDOIMetadata returns Recorded without
		// fetching metadata, keeping Title/Authors empty (the honest
		// signal that we never populated them).
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		r := openalex.New(openalex.Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
		v := verifyDOIMetadata(ctx, r, doi, "", nil)
		if v.Status != papers.VerifyRecorded {
			t.Errorf("got %q", v.Status)
		}
		if hits != 0 {
			t.Errorf("OpenAlex called %d times; want 0 (no claims, no lookup)", hits)
		}
		if v.Title != "" || len(v.Authors) != 0 || v.ArxivID != "" {
			t.Errorf("metadata should be empty when not fetched: %+v", v)
		}
	})
	t.Run("verified on title+author match", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, hhlBody, 200), doi,
			"Quantum algorithm for linear systems of equations", []string{"Harrow", "Lloyd"})
		if v.Status != papers.VerifyVerified {
			t.Errorf("got %q, want verified", v.Status)
		}
	})
	t.Run("mismatch on wrong title", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, hhlBody, 200), doi, "Totally different paper", nil)
		if v.Status != papers.VerifyMismatch {
			t.Errorf("got %q, want mismatch", v.Status)
		}
	})
	t.Run("mismatch on wrong author", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, hhlBody, 200), doi, "", []string{"Einstein"})
		if v.Status != papers.VerifyMismatch {
			t.Errorf("got %q, want mismatch", v.Status)
		}
	})
	t.Run("doi-not-found on 404", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, "", http.StatusNotFound), doi, "x", nil)
		if v.Status != papers.VerifyDOINotFound {
			t.Errorf("got %q", v.Status)
		}
	})
	t.Run("unavailable on upstream error", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, "", http.StatusInternalServerError), doi, "x", nil)
		if v.Status != papers.VerifyUnavailable {
			t.Errorf("got %q", v.Status)
		}
	})
}
