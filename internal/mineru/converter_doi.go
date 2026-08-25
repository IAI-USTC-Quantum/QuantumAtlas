package mineru

// converter_doi.go: the DOI fetch+convert pipeline (plan §A).
//
// Mirror of the arxiv silent-fetch flow (Ensure → run → runOnce →
// fetchAndStorePDF → writeResult) for DOI-indexed papers: when a DOI
// has no arXiv twin but OpenAlex surfaced an open-access PDF URL
// (best_oa_location.pdf_url), the markdown handler calls EnsureByDOI
// which fetches that URL, stores the bytes under the "<kind>/doi/..."
// layout (source='published'), drives MinerU, and writes the DOI-keyed
// markdown + images. Pollable via LookupDOI from the DOI markdown
// status endpoint, exactly like the arxiv LRO.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// doiJobKey derives the internal jobs-map key for a normalized DOI.
// The "doi:" prefix keeps DOI jobs disjoint from arxiv-canonical keys
// even though no arxiv canonical can ever match the DOI shape.
func doiJobKey(doi string) string { return "doi:" + doi }

// EnsureByDOI starts (or piggybacks on) a fetch+convert job for a
// DOI-indexed paper and returns a Job describing the current state —
// the DOI analogue of Ensure. oaPdfURL is the OpenAlex OA PDF the job
// fetches when the DOI-keyed PDF is not yet in the store; pass "" to
// convert an already-stored (contributed) PDF only.
//
// Cache-hit short-circuit: if the DOI-keyed markdown is already in the
// object store the call returns JobStateDone immediately (CacheHits++).
// A miss with neither a stored PDF nor an oaPdfURL fails fast with
// ErrNoDOISource (fatal) which the handler maps to 404 + contrib hint.
//
// Safe for concurrent use; concurrent EnsureByDOI calls for the same
// DOI are deduped to a single job, and a later caller's oaPdfURL
// backfills an in-flight job that was queued without one.
func (c *Converter) EnsureByDOI(ctx context.Context, doi, oaPdfURL string) *Job {
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return &Job{
			Canonical: doi,
			State:     JobStateFailed,
			Err:       fmt.Errorf("invalid DOI %q", doi),
			ErrKind:   ErrFatal,
		}
	}

	mdKey := paperassets.DOIAssetKey("markdown", norm)
	if _, exists, err := c.store.Stat(ctx, mdKey); err == nil && exists {
		c.counters.CacheHits.Add(1)
		return &Job{Canonical: norm, State: JobStateDone, Phase: PhaseReady, FinishedAt: c.now()}
	}
	c.counters.CacheMisses.Add(1)

	if !c.enabled {
		return &Job{
			Canonical: norm,
			State:     JobStateFailed,
			Err:       errors.New(c.disabledMsg),
			ErrKind:   ErrFatal,
		}
	}

	jobKey := doiJobKey(norm)
	now := c.now()

	c.mu.Lock()
	if existing, ok := c.jobs[jobKey]; ok {
		switch existing.State {
		case JobStateQueued, JobStateRunning:
			// Backfill the fetch source if the original caller had none
			// (e.g. a status-poll-triggered Ensure before OpenAlex
			// resolved) — the run goroutine reads it lazily.
			if existing.oaPdfURL == "" {
				existing.oaPdfURL = oaPdfURL
			}
			cp := *existing
			c.mu.Unlock()
			return &cp
		case JobStateDone:
			cp := *existing
			c.mu.Unlock()
			return &cp
		case JobStateFailed:
			if !existing.CooldownUntil.IsZero() && now.Before(existing.CooldownUntil) {
				cp := *existing
				c.mu.Unlock()
				return &cp
			}
			// Cooldown expired — fall through, replace the failed
			// record with a fresh queued job.
		}
	}

	job := &Job{
		Canonical:   norm,
		State:       JobStateQueued,
		SubmittedAt: now,
		oaPdfURL:    oaPdfURL,
	}
	c.jobs[jobKey] = job
	c.mu.Unlock()

	go c.runDOI(jobKey, norm)

	// Return a snapshot of the queued state.
	cp := *job
	return &cp
}

// LookupDOI returns the in-flight / recently-finished job snapshot for
// a DOI, if any. Side-effect-free — used by the DOI markdown status
// endpoint. doi may be un-normalized; normalization mirrors
// EnsureByDOI so both agree on the job key.
func (c *Converter) LookupDOI(doi string) (*Job, bool) {
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return nil, false
	}
	return c.Lookup(doiJobKey(norm))
}

// runDOI is the DOI-job background driver; see runJob for the shared
// lifecycle. Log lines carry "doi" instead of "arxiv_id".
func (c *Converter) runDOI(jobKey, doi string) {
	c.runJob(jobKey, "doi", doi, func(ctx context.Context) error {
		return c.runOnceDOI(ctx, jobKey, doi)
	})
}

// runOnceDOI is one full DOI conversion attempt: ensure the DOI-keyed
// PDF exists (fetching the job's OA PDF URL when missing), then hand
// off to convertStoredPDF for the MinerU step. A missing PDF with no
// OA URL fails with ErrNoDOISource (fatal → 404 at the handler).
func (c *Converter) runOnceDOI(ctx context.Context, jobKey, doi string) error {
	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	_, exists, err := c.store.Stat(ctx, pdfKey)
	if err != nil {
		return fmt.Errorf("stat pdf: %w", err)
	}
	if !exists {
		oaPdfURL := ""
		if j, ok := c.Lookup(jobKey); ok {
			oaPdfURL = j.oaPdfURL
		}
		if oaPdfURL == "" {
			return &Error{
				Msg:  ErrNoDOISource.Error() + ": " + doi,
				Kind: ErrNoDOISource,
			}
		}
		if c.cfg.Fetcher == nil {
			return &Error{Msg: "no PDF in store for " + doi + " and PDF fetcher not configured", Kind: ErrFatal}
		}
		if err := c.fetchAndStorePDFByDOI(ctx, jobKey, doi, oaPdfURL); err != nil {
			return err
		}
		if _, exists, err = c.store.Stat(ctx, pdfKey); err != nil {
			return fmt.Errorf("stat pdf (post-fetch): %w", err)
		}
		if !exists {
			return &Error{Msg: "pdf not found after OA fetch (race? concurrent delete?) for " + doi, Kind: ErrRetryable}
		}
	}

	// Same upload-name rule as the arxiv path: real .pdf suffix, no
	// path separators (DOIs are slash-ful by definition).
	uploadName := strings.ReplaceAll(doi, "/", "_") + ".pdf"

	return c.convertStoredPDF(ctx, jobKey, pdfKey, uploadName, jobKey,
		func(ctx context.Context, result Result) error {
			return c.writeResultDOI(ctx, doi, result)
		})
}

// fetchAndStorePDFByDOI downloads the OA PDF for doi via
// Fetcher.FetchURL and writes it to the DOI-keyed object-store path
// with IfNoneMatch:"*" (first-writer-wins, matches upload-pdf). The
// catalog write-through uses UpsertPDFByDOI (source='published') and is
// best-effort. Shares the arxiv-fetch semaphore and fetch counters with
// the arxiv silent-fetch path — both are the same upstream politeness
// budget. Mirrors fetchAndStorePDF; updates Phase + FetchProgress.
func (c *Converter) fetchAndStorePDFByDOI(ctx context.Context, jobKey, doi, oaPdfURL string) error {
	// Acquire the fetch semaphore (independent from MinerU concurrency)
	// so a thundering herd of DOI markdown requests can't open one
	// socket per request to publisher hosts. Block until a slot frees.
	select {
	case c.arxivSem <- struct{}{}:
	case <-ctx.Done():
		return &Error{Msg: "OA pdf fetch cancelled: " + ctx.Err().Error(), Kind: ErrRetryable}
	}
	c.counters.InflightArxivFetches.Add(1)
	defer func() {
		<-c.arxivSem
		c.counters.InflightArxivFetches.Add(-1)
	}()

	startedAt := c.now()
	c.transition(jobKey, func(j *Job) {
		j.Phase = PhaseFetchingPDF
		j.Fetch = &FetchProgress{StartedAt: startedAt}
	})
	c.counters.ArxivFetches.Add(1)

	result, err := c.cfg.Fetcher.FetchURL(ctx, oaPdfURL)
	if err != nil {
		c.counters.ArxivFetchFailed.Add(1)
		c.transition(jobKey, func(j *Job) {
			if j.Fetch != nil {
				j.Fetch.CompletedAt = c.now()
			}
		})
		kind := ErrRetryable
		switch {
		case errors.Is(err, arxiv.ErrNotFound):
			kind = ErrFatal
		case errors.Is(err, arxiv.ErrNotPDF), errors.Is(err, arxiv.ErrTooLarge):
			kind = ErrFatal
		case errors.Is(err, arxiv.ErrRateLimited):
			kind = ErrRetryable
		}
		return &Error{Msg: "fetch OA pdf for " + doi + ": " + err.Error(), Kind: kind}
	}

	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	_, putErr := c.store.PutWithOptions(ctx, pdfKey, result.Body, result.Size, objstore.PutOptions{
		ContentType: "application/pdf",
		IfNoneMatch: "*",
		Metadata: map[string]string{
			"sha256":     result.Sha256,
			"source":     "oa-pdf-silent-fetch",
			"source_url": oaPdfURL,
			"fetched_by": "qatlasd-converter",
			"fetched_at": c.now().UTC().Format(time.RFC3339),
		},
	})
	if putErr != nil && !errors.Is(putErr, objstore.ErrPreconditionFailed) {
		c.counters.ArxivFetchFailed.Add(1)
		return &Error{Msg: "store pdf after OA fetch: " + putErr.Error(), Kind: ErrRetryable}
	}

	completedAt := c.now()
	c.transition(jobKey, func(j *Job) {
		if j.Fetch == nil {
			j.Fetch = &FetchProgress{StartedAt: startedAt}
		}
		j.Fetch.CompletedAt = completedAt
		j.Fetch.BytesReceived = result.Size
		j.Fetch.BytesTotal = result.Size
		j.Fetch.Sha256 = result.Sha256
		j.Fetch.Attempts = result.Attempts
	})
	c.counters.ArxivFetchSucceeded.Add(1)

	// Registry write-through is best-effort.
	if c.catalog != nil {
		if _, _, uErr := c.catalog.UpsertPDFByDOI(ctx,
			registry.PaperRef{DOI: doi}, result.Sha256, result.Size,
			bucketRelKey(pdfKey)); uErr != nil &&
			!errors.Is(uErr, registry.ErrCatalogUnavailable) {
			c.logger.Warn("papers: UpsertPDFByDOI write-through after OA silent fetch failed",
				"doi", doi, "error", uErr)
		}
	}

	c.logger.Info("oa pdf silent fetch succeeded",
		"doi", doi,
		"source_url", oaPdfURL,
		"bytes", result.Size,
		"sha256", result.Sha256,
		"attempts", result.Attempts,
		"duration_seconds", c.now().Sub(startedAt).Seconds(),
	)
	return nil
}
