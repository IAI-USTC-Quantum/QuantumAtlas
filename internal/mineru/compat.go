package mineru

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Ensure and EnsurePDF preserve canonical APIs, but both resolve immutable
// source identity before use. Only Ensure triggers parsing, never EnsurePDF.
func (c *Converter) Ensure(ctx context.Context, canonical string) *Job {
	return c.ensureCanonical(ctx, canonical, "", false, false)
}
func (c *Converter) EnsurePDF(ctx context.Context, canonical string) *Job {
	return c.ensureCanonical(ctx, canonical, "", false, true)
}

// EnsurePDFByDOI is the DOI fetch/freeze-only path. It is token-independent
// and does not submit parsing, even when no existing parse is available.
func (c *Converter) EnsurePDFByDOI(ctx context.Context, doi, oaPDFURL string) *Job {
	return c.ensureCanonical(ctx, doi, oaPDFURL, true, true)
}

// ResolveFrozenCanonicalSource synchronously resolves/imports a stored arXiv
// PDF without inference or upstream fetching. Explicit qa/source selection
// uses EnsureFrozenSource; version selection may pass the versioned arXiv id.
func (c *Converter) ResolveFrozenCanonicalSource(ctx context.Context, canonical string) (registry.PaperSource, error) {
	if !c.cfg.PaperAccessEnabled {
		return registry.PaperSource{}, &Error{Msg: "paper access disabled", Kind: ErrFatal}
	}
	if _, err := paperassets.Parse(canonical); err != nil {
		return registry.PaperSource{}, &Error{Msg: "invalid arXiv ID", Kind: ErrFatal}
	}
	readCtx, cancel := objstore.ReadContext(ctx)
	defer cancel()
	src, _, err := c.canonicalSource(readCtx, canonical, false)
	return src, err
}

// ResolveFrozenDOISource is the stored-PDF-only DOI counterpart. A missing
// or corrupt frozen copy fails closed, never falling back to legacy bytes.
func (c *Converter) ResolveFrozenDOISource(ctx context.Context, doi string) (registry.PaperSource, error) {
	if !c.cfg.PaperAccessEnabled {
		return registry.PaperSource{}, &Error{Msg: "paper access disabled", Kind: ErrFatal}
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return registry.PaperSource{}, &Error{Msg: "invalid DOI", Kind: ErrFatal}
	}
	readCtx, cancel := objstore.ReadContext(ctx)
	defer cancel()
	src, _, err := c.canonicalSource(readCtx, norm, true)
	return src, err
}

func (c *Converter) canonicalSource(ctx context.Context, id string, doi bool) (registry.PaperSource, string, error) {
	if c.sources == nil {
		return registry.PaperSource{}, "", registry.ErrCatalogUnavailable
	}
	ref := registry.PaperRef{ArxivID: id}
	origin := "arxiv:" + id
	if doi {
		ref = registry.PaperRef{DOI: id}
		origin = "doi:" + id
	}
	paperID, _, err := c.sources.ResolveOrMint(ctx, ref)
	if err != nil {
		return registry.PaperSource{}, "", err
	}
	legacyKey := paperassets.AssetKey("pdf", id)
	if doi {
		legacyKey = paperassets.DOIAssetKey("pdf", id)
	}
	imported, found, err := c.sources.GetImportedPaperSource(ctx, paperID, legacyKey)
	if err != nil {
		return imported, paperID, err
	}
	if found {
		frozen, err := c.EnsureFrozenSource(ctx, paperID, imported.SourceID)
		if err != nil {
			return imported, paperID, err
		}
		return frozen, paperID, nil
	}
	sources, err := c.sources.ListPaperSources(ctx, paperID)
	if err != nil {
		return registry.PaperSource{}, paperID, err
	}
	for _, src := range sources {
		if src.Origin == origin || !doi && src.Origin == "arxiv:v"+fmt.Sprint(registry.ArxivVersionOf(id)) || doi && strings.HasPrefix(src.Origin, "published") {
			frozen, err := c.EnsureFrozenSource(ctx, paperID, src.SourceID)
			if err != nil {
				return src, paperID, err
			}
			bound, err := c.sources.BindPaperSourceImport(ctx, c.store, paperID, frozen.SourceID, legacyKey)
			if err != nil {
				return frozen, paperID, err
			}
			return bound, paperID, nil
		}
	}
	var key string
	var exists bool
	if doi {
		key = paperassets.DOIAssetKey("pdf", id)
		_, exists, err = c.store.Stat(ctx, key)
	} else {
		key, _, exists, err = paperassets.LocateAssetByID(ctx, c.store, "pdf", id)
	}
	if err != nil {
		return registry.PaperSource{}, paperID, err
	}
	if !exists {
		return registry.PaperSource{}, paperID, objstore.ErrNotFound
	}
	src, err := c.sources.RegisterFrozenPaperSource(ctx, c.store, paperID, origin, key)
	if err != nil {
		return src, paperID, err
	}
	// Pre-A1 lookup may resolve another key. Pin the canonical alias too,
	// without reading it or changing the SHA-reused historical source ID.
	bound, err := c.sources.BindPaperSourceImport(ctx, c.store, paperID, src.SourceID, legacyKey)
	if err != nil {
		return src, paperID, err
	}
	return bound, paperID, nil
}

func (c *Converter) ensureCanonical(ctx context.Context, id, oaURL string, doi, pdfOnly bool) *Job {
	if !c.cfg.PaperAccessEnabled {
		return sourceFailure("", id, &Error{Msg: "paper access disabled", Kind: ErrFatal})
	}
	if doi {
		norm, ok := paperassets.ValidateDOI(id)
		if !ok {
			return sourceFailure("", id, &Error{Msg: "invalid DOI", Kind: ErrFatal})
		}
		id = norm
	} else if _, err := paperassets.Parse(id); err != nil {
		return sourceFailure("", id, &Error{Msg: "invalid arXiv ID", Kind: ErrFatal})
	}
	alias := id
	if doi {
		alias = doiJobKey(id)
	}
	readCtx, cancel := objstore.ReadContext(ctx)
	src, paperID, err := c.canonicalSource(readCtx, id, doi)
	cancel()
	if err == nil {
		if pdfOnly {
			c.counters.CacheHits.Add(1)
			return &Job{Canonical: id, PaperID: paperID, SourceID: src.SourceID, SourcePDFSHA256: src.Sha256, State: JobStateDone, Phase: PhaseReady, FinishedAt: c.now()}
		}
		job := c.EnsureSource(ctx, paperID, src.SourceID)
		c.mu.Lock()
		c.aliases[alias] = sourceJobKey(src)
		c.mu.Unlock()
		cp := cloneJob(job)
		cp.Canonical = id
		return &cp
	}
	if src.SourceID != "" || !errors.Is(err, objstore.ErrNotFound) {
		return sourceFailure(paperID, id, err)
	}
	c.counters.CacheMisses.Add(1)
	if c.cfg.Fetcher == nil {
		return sourceFailure(paperID, id, &Error{Msg: "no PDF in store and PDF fetcher not configured", Kind: ErrFatal})
	}
	if doi && oaURL == "" && c.cfg.DOIPDFURL == nil {
		return sourceFailure(paperID, id, &Error{Msg: ErrNoDOISource.Error(), Kind: ErrNoDOISource})
	}
	if !pdfOnly && !c.Enabled() {
		return sourceFailure(paperID, id, &Error{Msg: c.DisabledReason(), Kind: ErrFatal})
	}
	key := "fetch:" + alias
	c.mu.Lock()
	if existing, ok := c.jobs[key]; ok && (existing.State == JobStateQueued || existing.State == JobStateRunning || existing.State == JobStateFailed && c.now().Before(existing.CooldownUntil)) {
		// Only an actual content request authorizes parsing after a PDF fetch.
		if !pdfOnly {
			existing.parseAfterFetch = true
		}
		cp := cloneJob(existing)
		c.mu.Unlock()
		return &cp
	}
	job := &Job{Canonical: id, PaperID: paperID, State: JobStateQueued, Phase: PhaseFetchingPDF, SubmittedAt: c.now(), parseAfterFetch: !pdfOnly}
	c.jobs[key] = job
	c.aliases[alias] = key
	cp := *job
	c.mu.Unlock()
	go c.fetchCanonical(key, alias, paperID, id, oaURL, doi, pdfOnly)
	return &cp
}

func (c *Converter) fetchCanonical(key, alias, paperID, id, oaURL string, doi, pdfOnly bool) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.MinerUTimeout)
	defer cancel()
	c.transition(key, func(j *Job) {
		j.State = JobStateRunning
		j.StartedAt = c.now()
		j.Fetch = &FetchProgress{StartedAt: c.now()}
	})
	select {
	case c.arxivSem <- struct{}{}:
	case <-ctx.Done():
		c.finishFetchFailure(key, ctx.Err())
		return
	}
	c.counters.InflightArxivFetches.Add(1)
	c.counters.ArxivFetches.Add(1)
	defer func() { <-c.arxivSem; c.counters.InflightArxivFetches.Add(-1) }()
	var src registry.PaperSource
	var err error
	if doi {
		if oaURL == "" && c.cfg.DOIPDFURL != nil {
			oaURL, err = c.cfg.DOIPDFURL(ctx, id)
			if err != nil {
				c.finishFetchFailure(key, err)
				return
			}
			oaURL = strings.TrimSpace(oaURL)
			parsed, parseErr := url.Parse(oaURL)
			if parseErr != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.EqualFold(parsed.Hostname(), "arxiv.org") || strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".arxiv.org") {
				c.finishFetchFailure(key, &Error{Msg: ErrNoDOISource.Error() + ": " + id, Kind: ErrNoDOISource})
				return
			}
		}
		result, fetchErr := c.cfg.Fetcher.FetchURL(ctx, oaURL)
		err = fetchErr
		if err == nil {
			var pdf []byte
			pdf, err = io.ReadAll(result.Body)
			if err == nil && c.cfg.VerifyDOIPDF != nil {
				if verifyErr := c.cfg.VerifyDOIPDF(ctx, id, pdf); verifyErr != nil {
					err = fmt.Errorf("verify published DOI PDF: %w", errors.Join(ErrFatal, verifyErr))
				}
			}
			if err == nil {
				src, err = c.registerFreshPDF(ctx, paperID, "doi:"+id, pdf, result.Sha256)
			}
		}
	} else {
		parsed, _ := paperassets.Parse(id)
		result, fetchErr := c.cfg.Fetcher.Fetch(ctx, parsed)
		err = fetchErr
		if err == nil {
			var pdf []byte
			pdf, err = io.ReadAll(result.Body)
			if err == nil {
				src, err = c.registerFreshPDF(ctx, paperID, "arxiv:"+id, pdf, result.Sha256)
			}
		}
	}
	if err != nil {
		c.finishFetchFailure(key, err)
		return
	}
	c.counters.ArxivFetchSucceeded.Add(1)
	c.transition(key, func(j *Job) {
		j.SourceID = src.SourceID
		j.SourcePDFSHA256 = src.Sha256
		j.Fetch.CompletedAt = c.now()
		j.Fetch.Sha256 = src.Sha256
		j.Fetch.BytesReceived = src.SizeBytes
		j.Fetch.BytesTotal = src.SizeBytes
	})
	c.mu.Lock()
	fetchSnapshot := cloneJob(c.jobs[key])
	pdfOnly = !c.jobs[key].parseAfterFetch
	if pdfOnly {
		c.jobs[key].State = JobStateDone
		c.jobs[key].Phase = PhaseReady
		c.jobs[key].FinishedAt = c.now()
	}
	c.mu.Unlock()
	if pdfOnly {
		return
	}
	job := c.EnsureSource(ctx, paperID, src.SourceID)
	c.mu.Lock()
	if job.State == JobStateFailed {
		cp := *job
		c.jobs[key] = &cp
	} else {
		if sourceJob := c.jobs[sourceJobKey(src)]; sourceJob != nil {
			sourceJob.Fetch = fetchSnapshot.Fetch
		}
		delete(c.jobs, key)
		c.aliases[alias] = sourceJobKey(src)
	}
	c.mu.Unlock()
}

func (c *Converter) registerFreshPDF(ctx context.Context, paperID, origin string, pdf []byte, sha string) (registry.PaperSource, error) {
	if paperbundle.SHA256(pdf) != sha {
		return registry.PaperSource{}, paperbundle.ErrIntegrity
	}
	src, err := c.sources.RegisterFrozenPDF(ctx, c.store, paperID, origin, pdf)
	if err != nil {
		return src, err
	}
	legacyKey := ""
	if strings.HasPrefix(origin, "doi:") {
		legacyKey = paperassets.DOIAssetKey("pdf", strings.TrimPrefix(origin, "doi:"))
	} else {
		legacyKey = paperassets.AssetKey("pdf", strings.TrimPrefix(origin, "arxiv:"))
	}
	return c.sources.BindPaperSourceImport(ctx, c.store, paperID, src.SourceID, legacyKey)
}

func (c *Converter) finishFetchFailure(key string, err error) {
	c.counters.ArxivFetchFailed.Add(1)
	if errors.Is(err, arxiv.ErrNotFound) || errors.Is(err, arxiv.ErrNotPDF) || errors.Is(err, arxiv.ErrTooLarge) {
		err = &Error{Msg: err.Error(), Kind: ErrFatal}
	}
	kind, cooldown := c.classifyFailure(err)
	c.transition(key, func(j *Job) {
		j.State = JobStateFailed
		j.Phase = PhaseErrorFetching
		j.FinishedAt = c.now()
		j.Err = err
		j.ErrKind = kind
		j.CooldownUntil = cooldown
	})
}
