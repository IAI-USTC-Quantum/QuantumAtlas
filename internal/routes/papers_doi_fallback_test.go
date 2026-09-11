package routes

// Regression tests for the DOI-input fast-path guard
// (decideLocalDOIServing): a metadata-backfilled DOI on an arXiv-sourced
// paper must fall back to the paper's arXiv identity instead of
// dead-ending the DOI pipeline with ErrNoDOISource — the mirror of the
// HasPublishedAsset guard the arXiv-input shape applies before
// redirecting TO the DOI namespace. Production instance of the bug: a
// paper with arxiv_id 0811.3171 + DOI 10.1103/physrevlett.103.150502
// whose only asset is the arXiv-source markdown 0811/0811.3171v3.md —
// lookup reported has_md=true while GET by DOI answered 202 →
// ErrNoDOISource, although GET by arXiv id served 200.

import (
	"context"
	"errors"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// storeWithKeys answers Stat positively for exactly the given keys.
func storeWithKeys(keys ...string) *fakeStore {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return &fakeStore{statFn: func(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
		if set[key] {
			return objstore.ObjectInfo{Key: key}, true, nil
		}
		return objstore.ObjectInfo{}, false, nil
	}}
}

// arxivBackfilledPaper builds the HHL-shaped fixture: one paper row
// carrying BOTH identities, with its only asset being arXiv-source.
// version 0 models a paper with no versioned arXiv asset.
func arxivBackfilledPaper(version int) *registry.PaperDetail {
	asset := registry.Asset{AssetID: 1, Source: "arxiv"}
	if version > 0 {
		asset.ArxivVersion = version
		asset.MinerUMDPath = "0811/0811.3171v3.md"
	}
	return &registry.PaperDetail{
		Paper: &registry.Paper{
			PaperID: "qa_test_hhl",
			ArxivID: "0811.3171",
			DOI:     "10.1103/physrevlett.103.150502",
		},
		Assets: []registry.Asset{asset},
	}
}

func backfilledCatalog(t *testing.T, detail *registry.PaperDetail) *fakePaperCatalog {
	t.Helper()
	c := newFakePaperCatalog()
	c.papers[detail.Paper.PaperID] = detail
	c.identity["doi:"+detail.Paper.DOI] = detail.Paper.PaperID
	c.doiRows[detail.Paper.DOI] = true
	return c
}

func TestDecideLocalDOIServing(t *testing.T) {
	ctx := context.Background()
	const doi = "10.1103/physrevlett.103.150502"

	t.Run("no papers row defers to openalex", func(t *testing.T) {
		outcome, twin, err := decideLocalDOIServing(ctx, newFakePaperCatalog(), storeWithKeys(), doi)
		if outcome != doiServeDefer || twin != "" || err != nil {
			t.Fatalf("got (%v, %q, %v), want (defer, \"\", nil)", outcome, twin, err)
		}
	})

	t.Run("lookup error surfaces for the 503 path", func(t *testing.T) {
		c := newFakePaperCatalog()
		c.err = errors.New("pg down")
		outcome, _, err := decideLocalDOIServing(ctx, c, storeWithKeys(), doi)
		if outcome != doiServeDefer || err == nil {
			t.Fatalf("got (%v, %v), want (defer, err)", outcome, err)
		}
	})

	t.Run("published asset serves the doi namespace", func(t *testing.T) {
		c := backfilledCatalog(t, arxivBackfilledPaper(3))
		c.publishedAssets[doi] = true
		outcome, twin, err := decideLocalDOIServing(ctx, c, storeWithKeys(), doi)
		if outcome != doiServeDOI || twin != "" || err != nil {
			t.Fatalf("got (%v, %q, %v), want (serveDOI, \"\", nil)", outcome, twin, err)
		}
	})

	t.Run("doi-keyed markdown object serves the doi namespace", func(t *testing.T) {
		c := backfilledCatalog(t, arxivBackfilledPaper(3))
		mdKey := paperassets.DOIAssetKey("markdown", doi)
		if mdKey == "" {
			t.Fatal("DOIAssetKey returned empty for a valid DOI")
		}
		outcome, _, err := decideLocalDOIServing(ctx, c, storeWithKeys(mdKey), doi)
		if outcome != doiServeDOI || err != nil {
			t.Fatalf("got (%v, %v), want (serveDOI, nil)", outcome, err)
		}
	})

	t.Run("doi-keyed pdf object serves the doi namespace", func(t *testing.T) {
		c := backfilledCatalog(t, arxivBackfilledPaper(3))
		pdfKey := paperassets.DOIAssetKey("pdf", doi)
		if pdfKey == "" {
			t.Fatal("DOIAssetKey returned empty for a valid DOI")
		}
		outcome, _, err := decideLocalDOIServing(ctx, c, storeWithKeys(pdfKey), doi)
		if outcome != doiServeDOI || err != nil {
			t.Fatalf("got (%v, %v), want (serveDOI, nil)", outcome, err)
		}
	})

	t.Run("backfilled doi falls back to the versioned arxiv twin", func(t *testing.T) {
		// The production bug: nothing DOI-side, markdown lives under the
		// arXiv asset. Must serve 0811.3171v3, not dispatch the DOI
		// pipeline into ErrNoDOISource.
		c := backfilledCatalog(t, arxivBackfilledPaper(3))
		outcome, twin, err := decideLocalDOIServing(ctx, c, storeWithKeys(), doi)
		if outcome != doiServeArxiv || twin != "0811.3171v3" || err != nil {
			t.Fatalf("got (%v, %q, %v), want (serveArxiv, 0811.3171v3, nil)", outcome, twin, err)
		}
	})

	t.Run("backfilled doi falls back to the bare arxiv twin", func(t *testing.T) {
		c := backfilledCatalog(t, arxivBackfilledPaper(0))
		outcome, twin, err := decideLocalDOIServing(ctx, c, storeWithKeys(), doi)
		if outcome != doiServeArxiv || twin != "0811.3171" || err != nil {
			t.Fatalf("got (%v, %q, %v), want (serveArxiv, 0811.3171, nil)", outcome, twin, err)
		}
	})

	t.Run("doi-only paper defers when nothing can serve", func(t *testing.T) {
		c := newFakePaperCatalog()
		c.doiRows[doi] = true
		// No identity row → GetPaperIDByIdentity misses → no twin.
		outcome, twin, err := decideLocalDOIServing(ctx, c, storeWithKeys(), doi)
		if outcome != doiServeDefer || twin != "" || err != nil {
			t.Fatalf("got (%v, %q, %v), want (defer, \"\", nil)", outcome, twin, err)
		}
	})

	t.Run("published-probe error still finds the twin", func(t *testing.T) {
		// HasPublishedAsset erroring must not strand the request on the
		// DOI pipeline when an arXiv twin exists (fail toward serving
		// the bytes we hold).
		c := failingPublishedProbe{backfilledCatalog(t, arxivBackfilledPaper(3))}
		if doiNamespaceServable(ctx, c, storeWithKeys(), doi) {
			t.Fatal("servable should be false when the registry probe errors")
		}
		outcome, twin, err := decideLocalDOIServing(ctx, c, storeWithKeys(), doi)
		if outcome != doiServeArxiv || twin != "0811.3171v3" || err != nil {
			t.Fatalf("got (%v, %q, %v), want (serveArxiv, 0811.3171v3, nil)", outcome, twin, err)
		}
	})
}

// failingPublishedProbe overrides only HasPublishedAsset to always
// error; every other method is promoted from the embedded fake.
type failingPublishedProbe struct {
	*fakePaperCatalog
}

func (failingPublishedProbe) HasPublishedAsset(context.Context, string) (bool, error) {
	return false, errors.New("pg hiccup")
}
