package routes

// Live end-to-end tests for the DOI fetch pipeline (plan §A). Gated by
// QATLAS_TEST_LIVE=1 — they hit the REAL OpenAlex API and the REAL
// arxiv.org / publisher PDF hosts. MinerU is the only stubbed leg
// (httptest), because burning real MinerU quota in CI is not the point;
// the fetch chain is.
//
//	QATLAS_TEST_LIVE=1 go test ./internal/routes/ -run TestLiveDOI -v
//
// Optional: QATLAS_OPENALEX_MAILTO to use a real polite-pool contact.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

const (
	// liveDOIArxivTwin is the HHL paper — OpenAlex links it to
	// arXiv:0811.3171 (verified against the live API when this test was
	// written).
	liveDOIArxivTwin = "10.1103/PhysRevLett.103.150502"
	// liveDOIPublisherOA is an npj Quantum Information review with NO
	// arxiv twin in OpenAlex and a direct OA publisher PDF at
	// best_oa_location.pdf_url (verified live: is_oa=true, pdf serves
	// %PDF- bytes).
	liveDOIPublisherOA = "10.1038/s41534-017-0025-3"
)

func liveSkipUnlessEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("QATLAS_TEST_LIVE") != "1" {
		t.Skip("set QATLAS_TEST_LIVE=1 to run live OpenAlex/PDF-fetch tests")
	}
}

func liveResolver(t *testing.T) *openalex.Resolver {
	t.Helper()
	mailto := os.Getenv("QATLAS_OPENALEX_MAILTO")
	if mailto == "" {
		mailto = "qatlas-live-test@example.org"
	}
	return openalex.New(openalex.Config{Mailto: mailto})
}

func liveFetcher(t *testing.T) *arxiv.Fetcher {
	t.Helper()
	f, err := arxiv.New(arxiv.Config{
		UserAgent: arxiv.BuildUserAgent("0.0.0-live-test", "qatlas-live-test@example.org"),
	})
	if err != nil {
		t.Fatalf("arxiv.New: %v", err)
	}
	return f
}

func liveConverter(t *testing.T, store *doiFlowStore) *mineru.Converter {
	t.Helper()
	stub := newDOIMinerUStub(t)
	return mineru.NewConverter(mineru.ConverterConfig{
		PaperAccessEnabled:      true,
		MinerUAPITokens:         []string{"live-test-token"},
		MinerUAPIBaseURL:        stub.server.URL,
		MinerUModelVersion:      "vlm",
		MinerUPollInterval:      20 * time.Millisecond,
		MinerUTimeout:           4 * time.Minute,
		MinerUMaxConcurrentJobs: 1,
		Fetcher:                 liveFetcher(t),
		ArxivFetchConcurrent:    1,
	}, store, nil, nil)
}

func waitDOIJobDone(t *testing.T, c *mineru.Converter, doi string) *mineru.Job {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if job, ok := c.LookupDOI(doi); ok {
			switch job.State {
			case mineru.JobStateDone:
				return job
			case mineru.JobStateFailed:
				t.Fatalf("DOI job failed: %+v", job)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("DOI job did not finish within 4 minutes")
	return nil
}

// TestLiveDOIWithArxivTwin: a system-external DOI that OpenAlex links
// to an arXiv preprint resolves through the arxiv path — the real
// arxiv.org PDF is fetched and (stub-)converted.
func TestLiveDOIWithArxivTwin(t *testing.T) {
	liveSkipUnlessEnabled(t)
	ctx := context.Background()

	res, err := liveResolver(t).ResolveDOI(ctx, liveDOIArxivTwin)
	if err != nil {
		t.Fatalf("ResolveDOI: %v", err)
	}
	if res.ArxivID != "0811.3171" {
		t.Fatalf("ArxivID = %q, want 0811.3171 (OpenAlex record changed?)", res.ArxivID)
	}
	t.Logf("resolved %s → arxiv %s", liveDOIArxivTwin, res.ArxivID)

	// Mirror the dispatcher: bare id → latest version → Ensure.
	versioned, err := resolveBareToVersioned(ctx, liveFetcher(t), res.ArxivID)
	if err != nil {
		t.Fatalf("resolveBareToVersioned: %v", err)
	}
	t.Logf("latest version: %s", versioned)

	store := newDOIFlowStore()
	converter := liveConverter(t, store)
	job := converter.Ensure(ctx, versioned)
	if job.State == mineru.JobStateFailed {
		t.Fatalf("Ensure failed immediately: %v", job.Err)
	}
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if j, ok := converter.Lookup(versioned); ok {
			switch j.State {
			case mineru.JobStateDone:
				goto done
			case mineru.JobStateFailed:
				t.Fatalf("arxiv job failed: %+v", j)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("arxiv job did not finish within 4 minutes")
done:
	mdKey := paperassets.AssetKey("markdown", versioned)
	if _, ok, _ := store.Stat(ctx, mdKey); !ok {
		t.Fatalf("markdown not stored at %q after live fetch+convert", mdKey)
	}
	t.Logf("live arxiv-twin chain OK: %s → %s → markdown stored", liveDOIArxivTwin, versioned)
}

// TestLiveDOIPublisherOpenAccess: a DOI with NO arxiv twin resolves to
// an OA PDF URL; EnsureByDOI fetches the real publisher PDF and stores
// DOI-keyed pdf + markdown assets.
func TestLiveDOIPublisherOpenAccess(t *testing.T) {
	liveSkipUnlessEnabled(t)
	ctx := context.Background()

	res, err := liveResolver(t).ResolveDOI(ctx, liveDOIPublisherOA)
	if err != nil {
		t.Fatalf("ResolveDOI: %v (want success via OA PDF, not ErrDOINotFound)", err)
	}
	if res.ArxivID != "" {
		t.Fatalf("ArxivID = %q, want empty — pick another DOI for this fixture", res.ArxivID)
	}
	if res.OAPdfURL == "" {
		t.Fatal("OAPdfURL empty — OpenAlex record changed?")
	}
	t.Logf("resolved %s → OA pdf %s", liveDOIPublisherOA, res.OAPdfURL)

	store := newDOIFlowStore()
	converter := liveConverter(t, store)
	converter.EnsureByDOI(ctx, liveDOIPublisherOA, res.OAPdfURL)
	job := waitDOIJobDone(t, converter, liveDOIPublisherOA)
	if job.Fetch == nil || job.Fetch.Sha256 == "" {
		t.Errorf("Fetch progress missing sha256: %+v", job.Fetch)
	}

	for _, kind := range []string{"pdf", "markdown"} {
		key := paperassets.DOIAssetKey(kind, liveDOIPublisherOA)
		info, ok, _ := store.Stat(ctx, key)
		if !ok {
			t.Fatalf("%s not stored at %q after live OA fetch+convert", kind, key)
		}
		t.Logf("stored %s (%d bytes) at %s", kind, info.Size, key)
	}
}

// TestLiveDOIInvalid: malformed DOI → ErrInvalidDOI; well-formed but
// nonexistent DOI → ErrDOINotFound (real OpenAlex 404).
func TestLiveDOIInvalid(t *testing.T) {
	liveSkipUnlessEnabled(t)
	ctx := context.Background()
	r := liveResolver(t)

	if _, err := r.ResolveDOI(ctx, "not-a-doi"); !errors.Is(err, openalex.ErrInvalidDOI) {
		t.Errorf("ResolveDOI(not-a-doi) = %v, want ErrInvalidDOI", err)
	}
	if _, err := r.ResolveDOI(ctx, "10.9999/qatlas-definitely-nonexistent-doi"); !errors.Is(err, openalex.ErrDOINotFound) {
		t.Errorf("ResolveDOI(bogus) = %v, want ErrDOINotFound", err)
	}

	// Sanity: the invalid-DOI 400 mapping at the handler level is
	// unit-tested (TestGetMarkdownByDOI_InvalidDOI400); here just make
	// sure the bogus DOI is not accidentally treated as having an OA
	// source by checking the error string stays honest.
	_, err := r.ResolveDOI(ctx, "10.9999/qatlas-definitely-nonexistent-doi")
	if err != nil && strings.Contains(err.Error(), "oa") && !errors.Is(err, openalex.ErrDOINotFound) {
		t.Errorf("unexpected error shape: %v", err)
	}
}
