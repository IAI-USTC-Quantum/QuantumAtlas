package downloader

import (
	"context"
	"fmt"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type frozenSourceRegistry interface {
	ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error)
	GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	FreezePaperSource(context.Context, objstore.Store, registry.PaperSource) (registry.PaperSource, error)
	RegisterFrozenPDF(context.Context, objstore.Store, string, string, []byte) (registry.PaperSource, error)
	BindPaperSourceImport(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
}

var _ frozenSourceRegistry = (*registry.Store)(nil)

func (d *Downloader) frozenCatalog() (frozenSourceRegistry, error) {
	r, ok := d.reg.(frozenSourceRegistry)
	if !ok || d.store == nil {
		return nil, fmt.Errorf("downloader: frozen source protocol unavailable: %w", registry.ErrCatalogUnavailable)
	}
	return r, nil
}
func acquisitionAlias(ref registry.PaperRef) string {
	if p, err := paperassets.Parse(ref.ArxivID); err == nil && p.Version != "" {
		return paperassets.AssetKeyFor("pdf", p)
	}
	if doi, ok := paperassets.ValidateDOI(ref.DOI); ok {
		return paperassets.DOIAssetKey("pdf", doi)
	}
	return ""
}
func (d *Downloader) boundFrozenSource(ctx context.Context, ref registry.PaperRef) (registry.PaperSource, bool, error) {
	reg, err := d.frozenCatalog()
	if err != nil {
		return registry.PaperSource{}, false, err
	}
	alias := acquisitionAlias(ref)
	if alias == "" {
		return registry.PaperSource{}, false, nil
	}
	id, _, err := reg.ResolveOrMint(ctx, ref)
	if err != nil {
		return registry.PaperSource{}, false, err
	}
	src, found, err := reg.GetImportedPaperSource(ctx, id, alias)
	if err != nil || !found {
		return src, found, err
	}
	src, err = reg.FreezePaperSource(ctx, d.store, src)
	if err != nil {
		return src, true, err
	}
	if src.ObjstoreKey != paperbundle.PDFKey(id, src.SourceID) {
		return src, true, paperbundle.ErrIntegrity
	}
	return src, true, nil
}
func (d *Downloader) registerFrozenOutcome(ctx context.Context, ref registry.PaperRef, origin, alias string, pdf []byte) (registry.PaperSource, error) {
	reg, err := d.frozenCatalog()
	if err != nil {
		return registry.PaperSource{}, err
	}
	id, _, err := reg.ResolveOrMint(ctx, ref)
	if err != nil {
		return registry.PaperSource{}, err
	}
	sha := paperbundle.SHA256(pdf)
	bound, found, err := reg.GetImportedPaperSource(ctx, id, alias)
	if err != nil {
		return registry.PaperSource{}, err
	}
	if found {
		if bound.Sha256 != sha || bound.SizeBytes != int64(len(pdf)) {
			return registry.PaperSource{}, fmt.Errorf("candidate conflicts with frozen PDF alias: %w", paperbundle.ErrIntegrity)
		}
		return reg.FreezePaperSource(ctx, d.store, bound)
	}
	src, err := reg.RegisterFrozenPDF(ctx, d.store, id, origin, pdf)
	if err != nil {
		return registry.PaperSource{}, err
	}
	src, err = reg.BindPaperSourceImport(ctx, d.store, id, src.SourceID, alias)
	if err != nil {
		return registry.PaperSource{}, err
	}
	if src.Sha256 != sha || src.SizeBytes != int64(len(pdf)) || src.ObjstoreKey != paperbundle.PDFKey(id, src.SourceID) {
		return registry.PaperSource{}, paperbundle.ErrIntegrity
	}
	return src, nil
}

// Recovery never trusts a metadata row alone. Full content keys must name a
// registered source and a canonical binding; legacy unbound rows do not count
// as completed new-format acquisition and cannot suppress validation/fetch.
func (d *Downloader) recoveredFrozenSource(ctx context.Context, ref registry.PaperRef, paperID string) (registry.PaperSource, bool, error) {
	src, found, err := d.boundFrozenSource(ctx, ref)
	if err != nil || found {
		return src, found, err
	}
	lookup, ok := d.reg.(assetLookup)
	if !ok {
		return src, false, nil
	}
	detail, exists, err := lookup.GetWithAssets(ctx, paperID)
	if err != nil {
		return src, false, err
	}
	if !exists || detail == nil || detail.Paper == nil {
		return src, false, nil
	}
	for _, a := range detail.Assets {
		if a.PDFPath == "" {
			continue
		}
		candidate := ref
		if a.Source == "arxiv" && a.ArxivVersion > 0 {
			if v := registry.ArxivVersionOf(ref.ArxivID); v > 0 && v != a.ArxivVersion {
				continue
			}
			candidate.ArxivID = fmt.Sprintf("%sv%d", registry.NormalizeArxivID(detail.Paper.ArxivID), a.ArxivVersion)
		} else if a.Source == "published" && ref.ArxivID == "" {
			candidate.DOI = detail.Paper.DOI
		} else {
			continue
		}
		src, bound, err := d.boundFrozenSource(ctx, candidate)
		if err != nil {
			return src, bound, err
		}
		if bound {
			return src, true, nil
		}
		if strings.HasPrefix(a.PDFPath, "content/") {
			return src, false, fmt.Errorf("content asset lacks authoritative source binding: %w", paperbundle.ErrIntegrity)
		}
	}
	return src, false, nil
}
