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

func TestStrictReject(t *testing.T) {
	// strictReject is only invoked when the upload's `?verify=strict`
	// flag is on; we drive it directly with each non-success status to
	// pin the 4xx/5xx mapping (and confirm verified does NOT block).
	if r := strictReject(papers.VerifyDOINotFound); r == nil || r.Status != http.StatusConflict {
		t.Errorf("strict doi-not-found should 409, got %v", r)
	}
	if r := strictReject(papers.VerifyUnavailable); r == nil || r.Status != http.StatusServiceUnavailable {
		t.Errorf("strict unavailable should 503, got %v", r)
	}
	if r := strictReject(papers.VerifyUnconfigured); r == nil || r.Status != http.StatusServiceUnavailable {
		t.Errorf("strict unconfigured should 503, got %v", r)
	}
	if strictReject(papers.VerifyVerified) != nil {
		t.Error("strict verified should proceed")
	}
}

func TestVerificationBody(t *testing.T) {
	t.Run("verified populates fields", func(t *testing.T) {
		got := verificationBody(papers.DOIVerification{
			Status:  papers.VerifyVerified,
			Title:   "Quantum algorithm",
			Authors: []string{"A", "B"},
			ArxivID: "0811.3171",
		})
		if got["status"] != papers.VerifyVerified {
			t.Errorf("status: %v", got["status"])
		}
		if got["title"] != "Quantum algorithm" {
			t.Errorf("title: %v", got["title"])
		}
		if !reflect.DeepEqual(got["authors"], []string{"A", "B"}) {
			t.Errorf("authors: %v", got["authors"])
		}
		if got["arxiv_id"] != "0811.3171" {
			t.Errorf("arxiv_id: %v", got["arxiv_id"])
		}
	})
	t.Run("non-verified leaves fields nil", func(t *testing.T) {
		got := verificationBody(papers.DOIVerification{Status: papers.VerifyUnavailable})
		if got["status"] != papers.VerifyUnavailable {
			t.Errorf("status: %v", got["status"])
		}
		if got["title"] != nil || got["authors"] != nil || got["arxiv_id"] != nil {
			t.Errorf("expected nil fields when nothing fetched, got %+v", got)
		}
	})
}

func stubResolver(t *testing.T, body string, status int, hits *int) *openalex.Resolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
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

// TestVerifyDOIMetadata locks in the post-design-fix contract: title /
// authors / linked arxiv id always come from OpenAlex (never from the
// contributor), so verifyDOIMetadata only needs (ctx, resolver, doi).
// The status drives strict-mode policy; the populated fields are written
// to the catalog when (and only when) the resolution succeeded.
func TestVerifyDOIMetadata(t *testing.T) {
	ctx := context.Background()
	doi := "10.1103/PhysRevLett.103.150502"

	t.Run("unconfigured when resolver disabled", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, openalex.New(openalex.Config{}), doi)
		if v.Status != papers.VerifyUnconfigured {
			t.Errorf("got %q", v.Status)
		}
		if v.Title != "" || len(v.Authors) != 0 || v.ArxivID != "" {
			t.Errorf("unconfigured must leave fields empty: %+v", v)
		}
	})
	t.Run("unconfigured when resolver is nil", func(t *testing.T) {
		if v := verifyDOIMetadata(ctx, nil, doi); v.Status != papers.VerifyUnconfigured {
			t.Errorf("got %q", v.Status)
		}
	})
	t.Run("verified when OpenAlex returns a record", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, hhlBody, 200, nil), doi)
		if v.Status != papers.VerifyVerified {
			t.Errorf("got %q, want verified", v.Status)
		}
		if v.Title != "Quantum algorithm for linear systems of equations" {
			t.Errorf("title: %q", v.Title)
		}
		want := []string{"Aram W. Harrow", "Avinatan Hassidim", "Seth Lloyd"}
		if !reflect.DeepEqual(v.Authors, want) {
			t.Errorf("authors: %v", v.Authors)
		}
		if v.ArxivID != "0811.3171" {
			t.Errorf("arxiv_id: %q", v.ArxivID)
		}
	})
	t.Run("doi-not-found on 404", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, "", http.StatusNotFound, nil), doi)
		if v.Status != papers.VerifyDOINotFound {
			t.Errorf("got %q", v.Status)
		}
		if v.Title != "" || len(v.Authors) != 0 {
			t.Errorf("not-found must leave fields empty: %+v", v)
		}
	})
	t.Run("unavailable on upstream error", func(t *testing.T) {
		v := verifyDOIMetadata(ctx, stubResolver(t, "", http.StatusInternalServerError, nil), doi)
		if v.Status != papers.VerifyUnavailable {
			t.Errorf("got %q", v.Status)
		}
		if v.Title != "" || len(v.Authors) != 0 {
			t.Errorf("unavailable must leave fields empty: %+v", v)
		}
	})
	t.Run("calls OpenAlex exactly once per upload", func(t *testing.T) {
		// Catches a regression where verifyDOIMetadata might short-
		// circuit (saving a round-trip) but at the cost of leaving
		// the catalog without the canonical title/authors. Always
		// call when the resolver is configured.
		var hits int
		r := stubResolver(t, hhlBody, 200, &hits)
		_ = verifyDOIMetadata(ctx, r, doi)
		if hits != 1 {
			t.Errorf("OpenAlex hit count = %d, want 1", hits)
		}
	})
}

// TestDOIVerificationRejectBody locks in that the strict-mode 409/503
// body never echoes a contributor-supplied "expected_*" field — the
// contributor cannot supply title/authors at all in the new design, so
// the body only carries the DOI, status, and (when populated) the
// OpenAlex-fetched title/authors.
func TestDOIVerificationRejectBody(t *testing.T) {
	rej := &uploadError{Status: http.StatusConflict, Detail: "DOI not found"}
	v := papers.DOIVerification{Status: papers.VerifyDOINotFound}
	got := doiVerificationRejectBody(rej, "10.1103/x", v)
	if _, has := got["expected_title"]; has {
		t.Error("response must not carry expected_title — contributor never supplies it")
	}
	if _, has := got["expected_authors"]; has {
		t.Error("response must not carry expected_authors — contributor never supplies it")
	}
	if got["doi"] != "10.1103/x" {
		t.Errorf("doi: %v", got["doi"])
	}
	if got["verification_status"] != papers.VerifyDOINotFound {
		t.Errorf("status: %v", got["verification_status"])
	}
}
