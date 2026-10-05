package routes

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Dashboard/status probes are strictly observational: no freezing, acquisition
// or inference. Old output presence and process Done are never ready evidence.
func frozenPaperStatus(ctx context.Context, catalog contentCatalog, store objstore.Store, converter *mineru.Converter, requested string) paperStatusEntry {
	e := paperStatusEntry{RequestedID: requested, State: "none"}
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, normalizeIDForDispatch(requested))
	if status != 0 {
		e.Error = detail
		return e
	}
	e.ResolvedID = rp.canonical
	if store == nil {
		e.State, e.Error = "unavailable", "asset storage unavailable; retry shortly"
		return e
	}
	ctx, cancel := objstore.ReadContext(ctx)
	defer cancel()
	d, found, err := catalog.GetWithAssets(ctx, rp.canonical)
	if err != nil {
		e.Error = statusErrText(err)
		return e
	}
	if !found || d == nil || d.Paper == nil {
		e.Error = "not found"
		return e
	}
	src, selectedAsset, found, err := selectStatusPDF(ctx, catalog, d, requested)
	if err != nil {
		e.Error = statusErrText(err)
		return e
	}
	if found {
		if src.ObjstoreKey == paperbundle.PDFKey(rp.canonical, src.SourceID) {
			if _, err := paperbundle.New(store).ReadPDF(ctx, rp.canonical, src.SourceID, src.Sha256, src.SizeBytes); err != nil {
				e.Error = "source PDF missing or failed integrity verification"
				return e
			}
			e.PdfReady = true
			bundle, ready, getErr := catalog.GetReadyParseBundle(ctx, store, rp.canonical, src.SourceID)
			if getErr != nil {
				e.State, e.Error = "unavailable", "content bundle verification unavailable"
				return e
			}
			if ready {
				e.MdReady, e.State, e.Phase = true, "cached", "ready"
				if m, err := paperbundle.New(store).GetManifest(ctx, bundle.PaperID, bundle.SourceID, bundle.RevisionID); err == nil {
					for _, f := range m.Files {
						if isBundleImage(f.Path) {
							e.ImageCount++
						}
					}
				}
				return e
			}
		} else {
			data, err := blockReadVerified(ctx, store, src.ObjstoreKey, src.Sha256)
			if err == nil && int64(len(data)) == src.SizeBytes {
				e.PdfReady = true
			}
		}
		if converter != nil {
			if job, ok := converter.LookupSource(rp.canonical, src.SourceID); ok && job.State != mineru.JobStateDone {
				e.State, e.Phase = string(job.State), string(job.Phase)
			}
		}
		return e
	}
	// Untouched legacy PDFs remain visible as PDF-only metadata. Merely probing
	// them never copies to the new bucket and never marks old markdown ready.
	if selectedAsset != nil {
		a := *selectedAsset
		key := a.PDFPath
		if !strings.HasPrefix(key, "pdf/") && !strings.HasPrefix(key, "content/") {
			key = "pdf/" + key
		}
		info, exists, statErr := store.Stat(ctx, key)
		if statErr != nil {
			e.State, e.Error = "unavailable", "asset storage unavailable; retry shortly"
			return e
		}
		e.PdfReady = exists && (a.PDFSize <= 0 || info.Size == a.PDFSize)
	}
	return e
}

// Select the same semantic edition as content access, without freezing or
// submitting anything. A bound canonical alias is authoritative even if the
// mutable old asset disappeared; a newer eligible asset outranks an old source.
func selectStatusPDF(ctx context.Context, catalog contentCatalog, d *registry.PaperDetail, requested string) (registry.PaperSource, *registry.Asset, bool, error) {
	paper := d.Paper.PaperID
	parsed, parseErr := paperassets.Parse(requested)
	isArxiv := parseErr == nil && parsed.IsValid()
	want := 0
	if isArxiv {
		want = registry.ArxivVersionOf(parsed.Canonical)
	}
	canonical := ""
	if want > 0 {
		canonical = paperassets.AssetKey("pdf", parsed.Canonical)
	} else if isDOICandidate(requested) {
		canonical = paperassets.DOIAssetKey("pdf", requested)
	}
	if canonical != "" {
		src, ok, err := catalog.GetImportedPaperSource(ctx, paper, canonical)
		if err != nil || ok {
			return src, nil, ok, err
		}
	}
	rank := func(origin string) int {
		v := sourceOriginVersion(origin)
		published := strings.HasPrefix(origin, "doi:") || strings.HasPrefix(origin, "published")
		if want > 0 {
			if v == want {
				return 1000 + v
			}
			return -1
		}
		if isArxiv && published {
			return -1
		}
		if published {
			return 1000000
		}
		if v > 0 {
			return 1000 + v
		}
		return 1
	}
	sources, err := catalog.ListPaperSources(ctx, paper)
	if err != nil {
		return registry.PaperSource{}, nil, false, err
	}
	sort.SliceStable(sources, func(i, j int) bool {
		a, b := rank(sources[i].Origin), rank(sources[j].Origin)
		if a != b {
			return a > b
		}
		if !sources[i].CreatedAt.Equal(sources[j].CreatedAt) {
			return sources[i].CreatedAt.After(sources[j].CreatedAt)
		}
		return sources[i].SourceID > sources[j].SourceID
	})
	srcRank := -1
	var src registry.PaperSource
	if len(sources) > 0 {
		src = sources[0]
		srcRank = rank(src.Origin)
	}
	assetRank := -1
	var selected *registry.Asset
	for _, a := range d.Assets {
		if a.PDFPath == "" {
			continue
		}
		r := -1
		if a.Source == "arxiv" && a.ArxivVersion > 0 && (want == 0 || a.ArxivVersion == want) {
			r = 1000 + a.ArxivVersion
		}
		if a.Source == "published" && !isArxiv && want == 0 {
			r = 1000000
		}
		if r > assetRank || r == assetRank && selected != nil && a.FetchedAt.After(selected.FetchedAt) {
			copy := a
			selected = &copy
			assetRank = r
		}
	}
	if srcRank >= 0 && srcRank >= assetRank {
		return src, nil, true, nil
	}
	if selected != nil {
		key := selected.PDFPath
		if !strings.HasPrefix(key, "pdf/") && !strings.HasPrefix(key, "content/") {
			key = "pdf/" + key
		}
		if selected.Source == "arxiv" && d.Paper.ArxivID != "" {
			canonical = paperassets.AssetKey("pdf", fmt.Sprintf("%sv%d", registry.NormalizeArxivID(d.Paper.ArxivID), selected.ArxivVersion))
		} else if selected.Source == "published" && d.Paper.DOI != "" {
			canonical = paperassets.DOIAssetKey("pdf", d.Paper.DOI)
		}
		for _, alias := range []string{canonical, key} {
			if alias == "" {
				continue
			}
			bound, ok, err := catalog.GetImportedPaperSource(ctx, paper, alias)
			if err != nil || ok {
				return bound, selected, ok, err
			}
		}
	}
	return registry.PaperSource{}, selected, false, nil
}
