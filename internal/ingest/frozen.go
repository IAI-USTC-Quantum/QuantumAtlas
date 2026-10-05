package ingest

import (
	"context"
	"errors"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Fresh acquisition requires the frozen-source protocol. Unsupported catalogs
// fail closed; there is no fallback PUT into a legacy PDF bucket.
type frozenSourceRegistry interface {
	ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error)
	GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	FreezePaperSource(context.Context, objstore.Store, registry.PaperSource) (registry.PaperSource, error)
	RegisterFrozenPDF(context.Context, objstore.Store, string, string, []byte) (registry.PaperSource, error)
	BindPaperSourceImport(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
}

var _ frozenSourceRegistry = (*registry.Store)(nil)

func (i *Ingester) frozenCatalog() (frozenSourceRegistry, error) {
	reg, ok := i.reg.(frozenSourceRegistry)
	if !ok || i.store == nil {
		return nil, fmt.Errorf("ingest: frozen source storage/catalog unavailable: %w", registry.ErrCatalogUnavailable)
	}
	return reg, nil
}
func (i *Ingester) boundSource(ctx context.Context, ref registry.PaperRef, alias string) (registry.PaperSource, bool, error) {
	reg, err := i.frozenCatalog()
	if err != nil {
		return registry.PaperSource{}, false, err
	}
	paper, _, err := reg.ResolveOrMint(ctx, ref)
	if err != nil {
		return registry.PaperSource{}, false, err
	}
	src, found, err := reg.GetImportedPaperSource(ctx, paper, alias)
	if err != nil || !found {
		return src, found, err
	}
	src, err = reg.FreezePaperSource(ctx, i.store, src)
	if err != nil {
		return src, true, err
	}
	if src.ObjstoreKey != paperbundle.PDFKey(paper, src.SourceID) {
		return src, true, paperbundle.ErrIntegrity
	}
	return src, true, nil
}
func (i *Ingester) registerFreshSource(ctx context.Context, ref registry.PaperRef, origin, alias string, pdf []byte, sha string, size int64) (registry.PaperSource, error) {
	if int64(len(pdf)) != size || paperbundle.SHA256(pdf) != sha {
		return registry.PaperSource{}, paperbundle.ErrIntegrity
	}
	reg, err := i.frozenCatalog()
	if err != nil {
		return registry.PaperSource{}, err
	}
	paper, _, err := reg.ResolveOrMint(ctx, ref)
	if err != nil {
		return registry.PaperSource{}, err
	}
	bound, found, err := reg.GetImportedPaperSource(ctx, paper, alias)
	if err != nil {
		return registry.PaperSource{}, err
	}
	if found {
		if bound.Sha256 != sha || bound.SizeBytes != size {
			return registry.PaperSource{}, fmt.Errorf("ingest: candidate differs from immutable alias: %w", paperbundle.ErrIntegrity)
		}
		return reg.FreezePaperSource(ctx, i.store, bound)
	}
	src, err := reg.RegisterFrozenPDF(ctx, i.store, paper, origin, pdf)
	if err != nil {
		return registry.PaperSource{}, err
	}
	src, err = reg.BindPaperSourceImport(ctx, i.store, paper, src.SourceID, alias)
	if err != nil {
		return registry.PaperSource{}, err
	}
	if src.Sha256 != sha || src.SizeBytes != size || src.ObjstoreKey != paperbundle.PDFKey(paper, src.SourceID) {
		return registry.PaperSource{}, paperbundle.ErrIntegrity
	}
	return src, nil
}

func (i *Ingester) recordFrozenSource(ctx context.Context, ref registry.PaperRef, version int, isDOI bool, src registry.PaperSource) error {
	if isDOI {
		_, _, err := i.reg.UpsertPDFByDOI(ctx, ref, src.Sha256, src.SizeBytes, src.ObjstoreKey)
		return err
	}
	if version < 1 {
		return errors.New("ingest: versioned arXiv identity required")
	}
	_, _, err := i.reg.UpsertPDF(ctx, ref, version, src.Sha256, src.SizeBytes, src.ObjstoreKey)
	return err
}
