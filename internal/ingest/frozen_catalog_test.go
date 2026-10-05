package ingest

import (
	"context"
	"fmt"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"strings"
	"time"
)

func (f *fakeReg) ResolveOrMint(_ context.Context, ref registry.PaperRef) (string, bool, error) {
	key := registry.NormalizeDOI(ref.DOI)
	if key == "" {
		key = registry.NormalizeArxivID(ref.ArxivID)
	}
	if key == "" {
		return "", false, registry.ErrTitleOnlyRef
	}
	return "qa_" + paperbundle.SHA256([]byte(key))[:26], false, nil
}
func (f *fakeReg) initFrozen() {
	if f.frozenSources == nil {
		f.frozenSources = map[string]registry.PaperSource{}
	}
	if f.frozenAliases == nil {
		f.frozenAliases = map[string]string{}
	}
}
func (f *fakeReg) GetImportedPaperSource(_ context.Context, paper, key string) (registry.PaperSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initFrozen()
	id, ok := f.frozenAliases[paper+"\n"+key]
	return f.frozenSources[id], ok, nil
}
func (f *fakeReg) FreezePaperSource(ctx context.Context, store objstore.Store, src registry.PaperSource) (registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initFrozen()
	if src.ObjstoreKey != paperbundle.PDFKey(src.PaperID, src.SourceID) {
		return src, paperbundle.ErrIntegrity
	}
	_, err := paperbundle.New(store).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
	return src, err
}
func (f *fakeReg) RegisterFrozenPDF(ctx context.Context, store objstore.Store, paper, origin string, pdf []byte) (registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initFrozen()
	sha := paperbundle.SHA256(pdf)
	for _, src := range f.frozenSources {
		if src.PaperID == paper && src.Sha256 == sha {
			_, err := paperbundle.New(store).ReadPDF(ctx, paper, src.SourceID, sha, src.SizeBytes)
			return src, err
		}
	}
	src := registry.PaperSource{PaperID: paper, SourceID: registry.NewSourceID(), Origin: origin, Sha256: sha, SizeBytes: int64(len(pdf)), CreatedAt: time.Now()}
	frozen, err := paperbundle.New(store).FreezePDF(ctx, paper, src.SourceID, pdf, sha)
	if err != nil {
		return src, err
	}
	src.ObjstoreKey = frozen.Key
	f.frozenSources[src.SourceID] = src
	return src, nil
}
func (f *fakeReg) BindPaperSourceImport(ctx context.Context, store objstore.Store, paper, id, key string) (registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initFrozen()
	src, ok := f.frozenSources[id]
	if !ok || src.PaperID != paper {
		return src, objstore.ErrNotFound
	}
	alias := paper + "\n" + key
	if priorID, bound := f.frozenAliases[alias]; bound {
		prior := f.frozenSources[priorID]
		if prior.Sha256 != src.Sha256 {
			return src, fmt.Errorf("alias conflict: %w", paperbundle.ErrIntegrity)
		}
		src = prior
	}
	if !strings.HasPrefix(src.ObjstoreKey, "content/") {
		return src, paperbundle.ErrIntegrity
	}
	if _, err := paperbundle.New(store).ReadPDF(ctx, paper, src.SourceID, src.Sha256, src.SizeBytes); err != nil {
		return src, err
	}
	f.frozenAliases[alias] = src.SourceID
	return src, nil
}

var _ frozenSourceRegistry = (*fakeReg)(nil)
