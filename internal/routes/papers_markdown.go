package routes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/pocketbase/pocketbase/core"
)

// markdownHandler answers GET /api/papers/{arxiv_id}/markdown.
//
// State machine driven by the converter snapshot:
//
//   200  cache hit — stream markdown bytes from object store (text/markdown)
//   202  conversion queued or running — Operation-Location + Retry-After
//   400  malformed arxiv id (canonical version-suffix check failed)
//   404  asset endpoints disabled (switch off — never reachable here
//        because RegisterPapers gates the route; included for safety)
//   502  conversion failed (fatal / retryable / daily-limit)
//   503  converter disabled / cache-only mode and no cached bytes
//
// Auth: gated by scopeGuard("papers", "read") at the route layer.
// Routing: only invoked when cfg.PaperAccessEnabled is true.
func markdownHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for markdown: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()
	resolution := resolutionFromContext(ctx)
	applyResolutionHeaders(re.Response, resolution)
	job := converter.Ensure(ctx, canonical)

	switch job.State {
	case mineru.JobStateDone:
		return streamMarkdown(re, store, canonical)
	case mineru.JobStateQueued, mineru.JobStateRunning:
		re.Response.Header().Set("Operation-Location", fmt.Sprintf("/api/papers/%s/markdown/status", canonical))
		re.Response.Header().Set("Retry-After", "5")
		body := snapshotBody(canonical, job)
		body["detail"] = "conversion in progress"
		body["operation"] = map[string]any{
			"status_url":          "/api/papers/" + canonical + "/markdown/status",
			"next_poll_after_iso": time.Now().Add(5 * time.Second).UTC().Format(time.RFC3339),
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusAccepted, body)
	case mineru.JobStateFailed:
		if !converter.Enabled() {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail":   "markdown not cached and converter disabled: " + converter.DisabledReason(),
				"arxiv_id": canonical,
			})
		}
		status := http.StatusBadGateway
		detail := "conversion failed: " + errString(job.Err)
		if errors.Is(job.ErrKind, mineru.ErrDailyLimit) {
			status = http.StatusServiceUnavailable
			detail = errString(job.Err)
			if detail == "" {
				detail = "server quota exhausted: every configured MinerU API key has hit today's daily limit; service will resume automatically at the next midnight reset"
			}
		}
		body := map[string]any{
			"detail":   detail,
			"arxiv_id": canonical,
			"kind":     jobKindLabel(job.ErrKind),
		}
		if !job.CooldownUntil.IsZero() {
			body["retry_after"] = job.CooldownUntil.Unix()
			body["retry_after_iso"] = job.CooldownUntil.UTC().Format(time.RFC3339)
			secs := int(time.Until(job.CooldownUntil).Seconds()) + 1
			if secs < 1 {
				secs = 1
			}
			re.Response.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(status, body)
	}
	return re.JSON(http.StatusInternalServerError, map[string]string{
		"detail": "unknown converter job state: " + string(job.State),
	})
}

// markdownStatusHandler answers GET /api/papers/{arxiv_id}/markdown/status.
//
// Side-effect-free poll surface: returns the current state of any
// in-flight or recently-failed conversion without triggering a new
// submission. The browser / qatlas client polls this after receiving a
// 202 from markdownHandler.
//
// Response shape (200 in all cases except 400/404):
//
//   - state: cached | queued | running | none | failed | cooldown | unavailable | not_in_arxiv
//   - phase: ready | fetching_pdf | converting_md | error_fetching | error_converting (omitted when meaningless)
//   - pdf_ready / md_ready (bool): the agent-decision triple — derived
//     from object-store probes plus the in-flight Phase
//   - fetch: {bytes_received, bytes_total, attempts, sha256, ...} when
//     a silent arxiv fetch is in flight or recently completed
//   - convert: {mineru_task_id, stage, polled_count, ...} when MinerU
//     is in flight or recently completed
//
// Response codes:
//
//   200  status payload
//   400  malformed arxiv id
//   404  paper unknown — no record AND silent fetch unavailable
//        (router didn't know it, store didn't have a PDF, fetcher disabled)
func markdownStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for markdown/status: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()
	resolution := resolutionFromContext(ctx)
	applyResolutionHeaders(re.Response, resolution)
	pdfReady, mdReady := probeAssetReadiness(ctx, store, canonical)

	// 1) Cache hit — even if we have no in-flight job for this id.
	//    Dual-read tolerates pre-A1 bare-stem objects that haven't been
	//    moved to the new per-category layout yet.
	if mdReady {
		body := map[string]any{
			"arxiv_id":     canonical,
			"state":        "cached",
			"phase":        string(mineru.PhaseReady),
			"pdf_ready":    pdfReady,
			"md_ready":     true,
			"markdown_url": "/api/papers/" + canonical + "/markdown",
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusOK, body)
	}

	// 2) In-flight / recently-failed job?
	if job, ok := converter.Lookup(canonical); ok {
		body := snapshotBody(canonical, job)
		body["pdf_ready"] = pdfReady || job.Phase == mineru.PhaseConvertingMD || job.State == mineru.JobStateDone
		body["md_ready"] = false
		// For Done state, double-check stat says no md (race with delete?)
		if job.State == mineru.JobStateDone && !mdReady {
			body["state"] = "cached"
			body["phase"] = string(mineru.PhaseReady)
			body["markdown_url"] = "/api/papers/" + canonical + "/markdown"
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusOK, body)
	}

	// 3) No cache, no job — does the PDF even exist?
	if !pdfReady {
		// Silent-fetch capable? Hint to the agent that hitting GET
		// /markdown will trigger a fetch+convert pipeline.
		if converter.Enabled() {
			body := map[string]any{
				"arxiv_id":  canonical,
				"state":     "none",
				"phase":     "",
				"pdf_ready": false,
				"md_ready":  false,
				"detail":    "no PDF in store, no job in flight; GET /api/papers/{id}/markdown will trigger a silent fetch + convert",
			}
			embedResolutionInBody(body, resolution)
			return re.JSON(http.StatusOK, body)
		}
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail":   "paper unknown: no PDF in catalog and silent fetch unavailable",
			"arxiv_id": canonical,
		})
	}

	// 4) PDF present, no job, no markdown — converter status drives
	//    what the client can expect.
	state := "none"
	if !converter.Enabled() {
		state = "unavailable"
	}
	body := map[string]any{
		"arxiv_id":  canonical,
		"state":     state,
		"phase":     "",
		"pdf_ready": true,
		"md_ready":  false,
	}
	if !converter.Enabled() {
		body["detail"] = converter.DisabledReason()
	}
	embedResolutionInBody(body, resolution)
	return re.JSON(http.StatusOK, body)
}

// probeAssetReadiness reports whether PDF + markdown are present in
// the store right now. Read-only, no writes. Cheap enough to call from
// status endpoints — the underlying LocateAssetByID does two HEADs at
// most when dual-read fallback fires.
func probeAssetReadiness(ctx context.Context, store objstore.Store, canonical string) (pdfReady, mdReady bool) {
	if _, _, exists, err := paperassets.LocateAssetByID(ctx, store, "pdf", canonical); err == nil && exists {
		pdfReady = true
	}
	if _, _, exists, err := paperassets.LocateAssetByID(ctx, store, "markdown", canonical); err == nil && exists {
		mdReady = true
	}
	return pdfReady, mdReady
}

// snapshotBody renders a Job snapshot into the JSON shape the
// markdown/status (and /pdf/status) endpoints emit. Includes phase,
// fetch sub-state, convert sub-state, and error metadata. Callers
// post-decorate with pdf_ready / md_ready and (via embedResolutionInBody)
// the requested_id / defaults_applied fields when applicable.
func snapshotBody(canonical string, job *mineru.Job) map[string]any {
	body := map[string]any{
		"arxiv_id": canonical,
		"state":    string(job.State),
	}
	if job.Phase != mineru.PhaseNone {
		body["phase"] = string(job.Phase)
	}
	if !job.SubmittedAt.IsZero() {
		body["submitted_at"] = job.SubmittedAt.UTC().Format(time.RFC3339)
	}
	if !job.StartedAt.IsZero() {
		body["started_at"] = job.StartedAt.UTC().Format(time.RFC3339)
	}
	if job.Fetch != nil {
		fetch := map[string]any{
			"attempts": job.Fetch.Attempts,
		}
		if !job.Fetch.StartedAt.IsZero() {
			fetch["started_at"] = job.Fetch.StartedAt.UTC().Format(time.RFC3339)
		}
		if !job.Fetch.CompletedAt.IsZero() {
			fetch["completed_at"] = job.Fetch.CompletedAt.UTC().Format(time.RFC3339)
		}
		if job.Fetch.BytesReceived > 0 {
			fetch["bytes_received"] = job.Fetch.BytesReceived
		}
		if job.Fetch.BytesTotal > 0 {
			fetch["bytes_total"] = job.Fetch.BytesTotal
		}
		if job.Fetch.Sha256 != "" {
			fetch["sha256"] = job.Fetch.Sha256
		}
		body["fetch"] = fetch
	}
	if job.Convert != nil {
		convert := map[string]any{
			"polled_count": job.Convert.PolledCount,
		}
		if job.Convert.MinerUTaskID != "" {
			convert["mineru_task_id"] = job.Convert.MinerUTaskID
		}
		if job.Convert.Stage != "" {
			convert["stage"] = job.Convert.Stage
		}
		if !job.Convert.StartedAt.IsZero() {
			convert["started_at"] = job.Convert.StartedAt.UTC().Format(time.RFC3339)
		}
		if !job.Convert.CompletedAt.IsZero() {
			convert["completed_at"] = job.Convert.CompletedAt.UTC().Format(time.RFC3339)
		}
		body["convert"] = convert
	}
	if job.Queue != nil {
		queue := map[string]any{
			"running_count":  job.Queue.RunningCount,
			"max_concurrent": job.Queue.MaxConcurrent,
			"eta_basis":      job.Queue.EtaBasis,
		}
		// Position / AheadOfMe only meaningful while queued; running
		// jobs always have position 0 — omit so the agent doesn't
		// render "you are at position 0".
		if job.Queue.Position > 0 {
			queue["position"] = job.Queue.Position
			queue["ahead_of_me"] = job.Queue.AheadOfMe
		}
		if job.Queue.EtaSeconds > 0 {
			queue["eta_seconds"] = job.Queue.EtaSeconds
		}
		if job.Queue.AvgDuration > 0 {
			queue["avg_duration_seconds"] = int64(job.Queue.AvgDuration.Seconds())
		}
		body["queue"] = queue
	}
	switch job.State {
	case mineru.JobStateFailed:
		if !job.CooldownUntil.IsZero() && time.Now().Before(job.CooldownUntil) {
			body["state"] = "cooldown"
			body["retry_after"] = job.CooldownUntil.Unix()
			body["retry_after_iso"] = job.CooldownUntil.UTC().Format(time.RFC3339)
		}
		body["kind"] = jobKindLabel(job.ErrKind)
		if job.Err != nil {
			body["detail"] = job.Err.Error()
		}
	case mineru.JobStateDone:
		body["markdown_url"] = "/api/papers/" + canonical + "/markdown"
	}
	return body
}

// streamMarkdown copies the cached markdown bytes from object store to
// the response body. Sets Content-Type and Content-Length when the
// backend reports them.
//
// Uses LocateAsset for dual-read fallback so pre-A1 bare-stem objects
// are still served while the storage migration is in flight.
func streamMarkdown(re *core.RequestEvent, store objstore.Store, canonical string) error {
	ctx := re.Request.Context()
	mdKey, _, exists, err := paperassets.LocateAssetByID(ctx, store, "markdown", canonical)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "locate markdown: " + err.Error(),
		})
	}
	if !exists {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail":   "markdown not found",
			"arxiv_id": canonical,
		})
	}
	rc, info, err := store.Get(ctx, mdKey)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			// Raced with a delete between Locate and Get — treat as miss.
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail":   "markdown not found",
				"arxiv_id": canonical,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch markdown: " + err.Error(),
		})
	}
	defer rc.Close()
	re.Response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(re.Response, rc); err != nil {
		slog.Warn("markdown: stream copy failed", "arxiv_id", canonical, "error", err)
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func jobKindLabel(kind error) string {
	switch {
	case errors.Is(kind, mineru.ErrFatal):
		return "fatal"
	case errors.Is(kind, mineru.ErrRetryable):
		return "retryable"
	case errors.Is(kind, mineru.ErrDailyLimit):
		return "daily_limit"
	default:
		return "unknown"
	}
}
