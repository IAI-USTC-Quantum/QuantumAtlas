package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/safego"
)

// DefaultProviderTimeout bounds each provider call when Engine.ProviderTimeout
// is unset. One slow source must not stall the whole fan-out.
const DefaultProviderTimeout = 15 * time.Second

// minter is the slice of *registry.Store the engine needs, factored out so
// tests can fake minting without a database. *registry.Store satisfies it
// implicitly.
type minter interface {
	ResolveOrMint(ctx context.Context, ref registry.PaperRef) (paperID string, created bool, err error)
	UpdateStatus(ctx context.Context, paperID, status string) (found bool, err error)
	Get(ctx context.Context, paperID string) (paper *registry.Paper, found bool, err error)
}

// Engine fans one SearchEntry out to every provider, merges the hits by
// paper identity, and resolves-or-mints merged hits against the registry.
// A nil registry leaves minting disabled: hits still come back (with an
// empty Result.PaperID), they just are not anchored to a paper_id.
type Engine struct {
	providers []Provider
	reg       minter
	onMint    func(ctx context.Context, paperID string, ref registry.PaperRef)

	// ProviderTimeout bounds each individual provider call. Zero applies
	// DefaultProviderTimeout.
	ProviderTimeout time.Duration
}

// NewEngine builds an Engine. reg may be nil (minting disabled); onMint is
// an optional hook fired once per newly minted paper (used by the
// lazy-ingestion pipeline) and may be nil.
func NewEngine(reg *registry.Store, onMint func(ctx context.Context, paperID string, ref registry.PaperRef), providers ...Provider) *Engine {
	e := &Engine{providers: providers, onMint: onMint}
	if reg != nil {
		e.reg = reg
	}
	return e
}

// Result is a merged hit anchored to a registry paper. PaperID is empty
// when minting is disabled (nil registry); Created reports whether this
// search minted the paper.
type Result struct {
	PaperID string
	Hit     Hit
	Created bool
}

// Response is the outcome of one Search: Results are identity-anchored
// merged hits (ordered by score desc, capped at the entry's MaxResults);
// Candidates are title-only merged hits that were NOT minted (the title
// hash alone is too weak to anchor identity on), capped at MaxCandidates.
type Response struct {
	Results    []Result
	Candidates []Hit
}

// providerResult collects one provider's hits (empty on failure).
type providerResult struct {
	hits []Hit
}

// Search runs the full fan-out → merge → resolve-or-mint pipeline. It
// never fails because of an individual provider; a database failure during
// minting is returned as the error.
func (e *Engine) Search(ctx context.Context, entry SearchEntry) (Response, error) {
	entry.Normalize()
	hits, _ := e.fanOut(ctx, entry)
	merged := mergeHits(hits)

	results, candidates, err := e.MintHits(ctx, merged, entry.MaxResults)
	return Response{Results: results, Candidates: candidates}, err
}

// Collect runs the fan-out and returns the merged hits together with the
// per-provider failure table (provider name → error). Unlike Search it
// performs no minting — the local agentic backend (internal/agentic) uses
// it to build the raw result set handed to the claude CLI and the
// agent=false response, where provider failures must surface in the
// response's errors map instead of being swallowed.
func (e *Engine) Collect(ctx context.Context, entry SearchEntry) ([]Hit, map[string]error) {
	entry.Normalize()
	hits, errs := e.fanOut(ctx, entry)
	return mergeHits(hits), errs
}

// MintHits runs the resolve-or-mint half of the pipeline over already-
// merged hits — shared with the agentic endpoint, whose hits arrive
// from the remote microservice instead of the fan-out. Title-only hits
// are never minted: they are returned (capped at MaxCandidates) as
// candidates. Returns the partial results gathered so far together with
// the first minting error.
func (e *Engine) MintHits(ctx context.Context, hits []Hit, maxResults int) ([]Result, []Hit, error) {
	return MintHits(ctx, e.reg, e.onMint, hits, maxResults)
}

// MintHits resolves-or-mints identity-anchored hits against reg,
// capped at maxResults; title-only hits are collected as un-minted
// candidates (capped at MaxCandidates). reg may be nil (minting
// disabled — hits come back with an empty Result.PaperID); onMint is an
// optional hook fired once per newly minted paper. On a minting error
// the partial results are returned together with the error.
func MintHits(ctx context.Context, reg minter, onMint func(ctx context.Context, paperID string, ref registry.PaperRef), hits []Hit, maxResults int) ([]Result, []Hit, error) {
	var results []Result
	var candidates []Hit
	for _, h := range hits {
		if h.DOI == "" && h.ArxivID == "" {
			// Title-only hits never mint — surface as candidates.
			if len(candidates) < MaxCandidates {
				candidates = append(candidates, h)
			}
			continue
		}
		if len(results) >= maxResults {
			continue
		}
		res := Result{Hit: h}
		if reg != nil {
			ref := registry.PaperRef{DOI: h.DOI, ArxivID: h.ArxivID, Title: h.Title, Authors: h.Authors, Year: h.Year}
			paperID, created, err := reg.ResolveOrMint(ctx, ref)
			if err != nil {
				return results, candidates, fmt.Errorf("search: resolve-or-mint %s: %w", identityKey(h), err)
			}
			shouldIngest := created
			if created {
				// Registry mints with the schema default status 'ready';
				// search-minted papers still need ingestion, so demote to
				// 'pending' before notifying the lazy-ingestion hook.
				if _, err := reg.UpdateStatus(ctx, paperID, "pending"); err != nil {
					return results, candidates, fmt.Errorf("search: mark minted paper %s pending: %w", paperID, err)
				}
			} else if onMint != nil {
				// A process restart can strand an in-memory ingestion job.
				// Re-searching an existing pending paper re-drives it, while
				// ready/failed papers remain side-effect free.
				paper, found, err := reg.Get(ctx, paperID)
				if err != nil {
					return results, candidates, fmt.Errorf("search: inspect paper %s for ingestion: %w", paperID, err)
				}
				shouldIngest = found && paper.Status == "pending"
			}
			if shouldIngest && onMint != nil {
				onMint(ctx, paperID, ref)
			}
			res.PaperID = paperID
			res.Created = created
		}
		results = append(results, res)
	}
	return results, candidates, nil
}

// fanOut runs every provider concurrently, each under its own timeout and
// panic-safe (the WaitGroup awaits the goroutines, so it uses the
// safego.LogPanic pattern rather than safego.Go). A provider that errors,
// panics, or times out contributes zero hits; the returned map carries
// the per-provider failures (name → error) for callers that surface them
// (the local agentic backend). Search ignores the map.
//
// Providers honor the failure contract by returning (nil, nil) and
// recording internally (BaseProvider), so the table is filled from the
// recorded LastError when a provider came back empty-handed. Caveat:
// LastError is sticky across calls — an empty-but-successful result can
// inherit a stale failure from an earlier call.
func (e *Engine) fanOut(ctx context.Context, entry SearchEntry) ([]Hit, map[string]error) {
	timeout := e.ProviderTimeout
	if timeout <= 0 {
		timeout = DefaultProviderTimeout
	}
	results := make([]providerResult, len(e.providers))
	errs := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, p := range e.providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					safego.LogPanic("search.provider:"+p.Name(), r)
				}
			}()
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			hits, err := p.Search(pctx, entry)
			if err == nil && len(hits) == 0 {
				if ep, ok := p.(interface{ LastError() error }); ok {
					err = ep.LastError()
				}
			}
			if err != nil {
				// Contract violation (or recorded backend failure) —
				// isolate the provider, note the failure.
				mu.Lock()
				errs[p.Name()] = err
				mu.Unlock()
				return
			}
			results[i] = providerResult{hits: hits}
		}(i, p)
	}
	wg.Wait()

	var all []Hit
	for _, r := range results {
		all = append(all, r.hits...)
	}
	if len(errs) == 0 {
		errs = nil
	}
	return all, errs
}

// identityKey reduces a hit to its dedup identity: DOI (normalized,
// lowercased) > bare arXiv id > fuzzy title hash — the same priority the
// Python identity_key uses.
func identityKey(h Hit) string {
	if doi := registry.NormalizeDOI(h.DOI); doi != "" {
		return registry.DOIKey(doi)
	}
	if arxiv := registry.NormalizeArxivID(h.ArxivID); arxiv != "" {
		return registry.ArxivKey(arxiv)
	}
	return registry.TitleKey(registry.TitleHash(h.Title, nil, 0))
}

// mergeHits dedups hits by identity key. The merged hit keeps the max
// Score, the union of Sources (comma-joined, first-seen order), and the
// first non-empty DOI / arXiv id / title seen. Output is ordered by Score
// desc with first-seen order as the tiebreak.
func mergeHits(hits []Hit) []Hit {
	index := map[string]int{}
	var merged []Hit
	for _, h := range hits {
		h.DOI = registry.NormalizeDOI(h.DOI)
		h.ArxivID = registry.NormalizeArxivID(h.ArxivID)
		key := identityKey(h)
		i, seen := index[key]
		if !seen {
			index[key] = len(merged)
			merged = append(merged, h)
			continue
		}
		m := &merged[i]
		if h.Score > m.Score {
			m.Score = h.Score
		}
		if m.DOI == "" {
			m.DOI = h.DOI
		}
		if m.ArxivID == "" {
			m.ArxivID = h.ArxivID
		}
		if m.Title == "" {
			m.Title = h.Title
		}
		if m.Abstract == "" {
			m.Abstract = h.Abstract
		}
		if h.Source != "" && !sourceListed(m.Source, h.Source) {
			if m.Source == "" {
				m.Source = h.Source
			} else {
				m.Source += "," + h.Source
			}
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	return merged
}

// sourceListed reports whether src already appears in the comma-joined
// source list.
func sourceListed(list, src string) bool {
	for _, s := range strings.Split(list, ",") {
		if s == src {
			return true
		}
	}
	return false
}
