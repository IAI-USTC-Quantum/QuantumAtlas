package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

const pipelineTitle = "Verified Quantum Algorithms for Ordered Search"

func pipelinePDF(t *testing.T, size int) []byte {
	t.Helper()
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("published provenance fixtures require pdftotext")
	}
	return provenanceFixturePDF([]string{pipelineTitle, "Alice Smith", "Abstract", strings.Repeat("body ", size/5+1)})
}

func TestPublishedCandidateOwnershipPipeline(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext unavailable")
	}
	for _, tc := range []struct {
		name, title string
		lines       []string
		accepted    bool
	}{
		{"correct article", pipelineTitle, []string{pipelineTitle, "Alice Smith", "Abstract", "Body"}, true},
		{"DOI and title in references are not ownership", pipelineTitle, []string{"An Unrelated Classical Search Algorithm", "References", pipelineTitle, "doi:10.1145/3800579"}, false},
		{"missing expected metadata", "", []string{pipelineTitle, "Abstract"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := provenanceFixturePDF(tc.lines)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/pdf")
				_, _ = w.Write(pdf)
			}))
			defer srv.Close()
			client, err := NewFetchClient(FetchConfig{HTTPClient: srv.Client(), MinPDFBytes: 32, RespectRobots: false})
			if err != nil {
				t.Fatal(err)
			}
			d := &Downloader{fetch: client}
			out := &FetchOutcome{DOI: "10.1145/3800579", PublishedTitle: tc.title}
			res := d.tryCandidate(context.Background(), "fixture", srv.URL+"/paper.pdf", out)
			if (res != nil) != tc.accepted {
				t.Fatalf("res=%v trace=%+v", res, out.Trace)
			}
			if !tc.accepted && (out.Result != nil || len(out.Trace) != 1 || out.Trace[0].FailureKind != "identity_unproven") {
				t.Fatalf("unproven candidate usable: %+v", out)
			}
		})
	}
}

func TestPublishedCandidateRecomputesWorkerMetadata(t *testing.T) {
	pdf := pipelinePDF(t, 16000)
	res := &FetchResult{Body: bytes.NewReader(pdf), Size: 1, Sha256: strings.Repeat("0", 64)}
	d := &Downloader{}
	if err := d.verifyPublishedCandidate(t.Context(), res, &FetchOutcome{DOI: "10.1145/3800579", PublishedTitle: pipelineTitle}); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(pdf)
	actual, err := io.ReadAll(res.Body)
	if err != nil || !bytes.Equal(actual, pdf) || res.Size != int64(len(pdf)) || res.Sha256 != hex.EncodeToString(want[:]) {
		t.Fatalf("worker metadata retained or verified body not rewound: size=%d sha=%s err=%v", res.Size, res.Sha256, err)
	}
}

func TestPublishedArchiveRejectsWrongExistingPDF(t *testing.T) {
	ctx := context.Background()
	store := newLocalStore(t, t.TempDir())
	reg := newFakeReg()
	d := &Downloader{store: store, reg: reg}
	wrong := provenanceFixturePDF([]string{"An Unrelated Classical Search Algorithm", "References", pipelineTitle, strings.Repeat("reference ", 3000)})
	doi := "10.1145/3800579"
	paper, _, _ := reg.ResolveOrMint(ctx, registry.PaperRef{DOI: doi})
	src, err := reg.RegisterFrozenPDF(ctx, store, paper, "legacy", wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BindPaperSourceImport(ctx, store, paper, src.SourceID, paperassets.DOIAssetKey("pdf", doi)); err != nil {
		t.Fatal(err)
	}
	good := pipelinePDF(t, 16000)
	out := &FetchOutcome{DOI: doi, Result: &FetchResult{Body: bytes.NewReader(good), Size: int64(len(good))}}
	err = d.storeOutcome(ctx, job{ref: registry.PaperRef{DOI: doi, Title: pipelineTitle}}, out)
	if !errors.Is(err, paperbundle.ErrIntegrity) || len(reg.upsertDOI) != 0 {
		t.Fatalf("wrong existing PDF registered: err=%v reg=%v", err, reg.upsertDOI)
	}
}

func TestPublishedArchiveDoesNotTrustWorkerTitle(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext unavailable")
	}
	wrongTitle := "An Unrelated Classical Search Algorithm"
	wrong := provenanceFixturePDF([]string{wrongTitle, "Abstract"})
	reg := newFakeReg()
	d := &Downloader{store: newLocalStore(t, t.TempDir()), reg: reg}
	out := &FetchOutcome{DOI: "10.1145/3800579", PublishedTitle: wrongTitle, Result: &FetchResult{Body: bytes.NewReader(wrong), Size: int64(len(wrong))}}
	err := d.storeOutcome(context.Background(), job{ref: registry.PaperRef{DOI: out.DOI, Title: pipelineTitle}}, out)
	if !errors.Is(err, ErrPDFIdentityUnproven) || len(reg.upsertDOI) != 0 {
		t.Fatalf("worker title bypass: %v", err)
	}
}
