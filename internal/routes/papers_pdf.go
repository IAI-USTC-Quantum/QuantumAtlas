package routes

import (
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

// pdfHandler answers GET /api/papers/{arxiv_id}/pdf.
//
// Mirrors markdownHandler's long-running-operation shape but skips the
// MinerU pipeline — this endpoint serves the PDF bytes directly, and
// on cache miss delegates to converter.EnsurePDF which silent-fetches
// from arxiv.org (when the operator wired a fetcher) and writes the
// bytes to the object store under the canonical key.
//
// Response codes:
//
//   200  cache hit — stream application/pdf bytes
//   202  fetch in progress — Operation-Location + Retry-After, body
//        contains decision triple + Phase + Fetch sub-state
//   400  malformed arxiv id
//   404  paper not on arxiv (arxiv.ErrNotFound)
//   500  store read failure
//   502  fetch failed (upstream / transport / non-PDF body)
//   503  silent fetch unavailable (converter disabled)
//
// Routing: only registered when cfg.PaperAccessEnabled is true. Auth:
// gated by scopeGuard("papers", "read") at the route layer.
func pdfHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for pdf: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()
	resolution := resolutionFromContext(ctx)
	applyResolutionHeaders(re.Response, resolution)
	job := converter.EnsurePDF(ctx, canonical)

	switch job.State {
	case mineru.JobStateDone:
		return streamPDF(re, store, canonical)
	case mineru.JobStateQueued, mineru.JobStateRunning:
		re.Response.Header().Set("Operation-Location", fmt.Sprintf("/api/papers/%s/pdf/status", canonical))
		re.Response.Header().Set("Retry-After", "5")
		body := snapshotBody(canonical, job)
		body["detail"] = "PDF not in store; fetching from arxiv.org"
		body["pdf_ready"] = false
		body["md_ready"] = false
		body["operation"] = map[string]any{
			"status_url":          "/api/papers/" + canonical + "/pdf/status",
			"next_poll_after_iso": time.Now().Add(5 * time.Second).UTC().Format(time.RFC3339),
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusAccepted, body)
	case mineru.JobStateFailed:
		return failedPDFResponse(re, converter, canonical, job)
	}
	return re.JSON(http.StatusInternalServerError, map[string]string{
		"detail": "unknown converter job state: " + string(job.State),
	})
}

// pdfStatusHandler answers GET /api/papers/{arxiv_id}/pdf/status.
// Side-effect-free poll surface — same contract as
// markdownStatusHandler but states are restricted to the fetch-only
// flow (no convert phase).
func pdfStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for pdf/status: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()
	resolution := resolutionFromContext(ctx)
	applyResolutionHeaders(re.Response, resolution)
	pdfReady, mdReady := probeAssetReadiness(ctx, store, canonical)

	if pdfReady {
		body := map[string]any{
			"arxiv_id":  canonical,
			"state":     "cached",
			"phase":     string(mineru.PhaseReady),
			"pdf_ready": true,
			"md_ready":  mdReady,
			"pdf_url":   "/api/papers/" + canonical + "/pdf",
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusOK, body)
	}

	if job, ok := converter.Lookup(canonical); ok {
		body := snapshotBody(canonical, job)
		body["pdf_ready"] = job.State == mineru.JobStateDone
		body["md_ready"] = false
		// PDF endpoint reports cached only on Done+pdf-present.
		if job.State == mineru.JobStateDone {
			body["state"] = "cached"
			body["phase"] = string(mineru.PhaseReady)
			body["pdf_url"] = "/api/papers/" + canonical + "/pdf"
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusOK, body)
	}

	// No cache, no job — describe whether silent fetch is on the table.
	state := "none"
	detail := "no PDF in store, no fetch in flight; GET /api/papers/{id}/pdf will trigger a silent fetch"
	if !converter.Enabled() {
		state = "unavailable"
		detail = converter.DisabledReason()
	}
	body := map[string]any{
		"arxiv_id":  canonical,
		"state":     state,
		"phase":     "",
		"pdf_ready": false,
		"md_ready":  mdReady,
		"detail":    detail,
	}
	embedResolutionInBody(body, resolution)
	return re.JSON(http.StatusOK, body)
}

// streamPDF copies the cached PDF bytes from the object store to the
// response. Uses LocateAssetByID for dual-read fallback.
func streamPDF(re *core.RequestEvent, store objstore.Store, canonical string) error {
	ctx := re.Request.Context()
	pdfKey, _, exists, err := paperassets.LocateAssetByID(ctx, store, "pdf", canonical)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "locate pdf: " + err.Error(),
		})
	}
	if !exists {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail":   "pdf not found",
			"arxiv_id": canonical,
		})
	}
	rc, info, err := store.Get(ctx, pdfKey)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail":   "pdf not found",
				"arxiv_id": canonical,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch pdf: " + err.Error(),
		})
	}
	defer rc.Close()
	re.Response.Header().Set("Content-Type", "application/pdf")
	re.Response.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.pdf"`, sanitizeFilename(canonical)))
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(re.Response, rc); err != nil {
		slog.Warn("pdf: stream copy failed", "arxiv_id", canonical, "error", err)
	}
	return nil
}

// failedPDFResponse renders a JSON 4xx/5xx from a failed PDF-fetch
// Job. Maps the converter's typed errors onto the appropriate HTTP
// status: not_in_arxiv (ErrNotFound chain) → 404, fetch_disabled
// (converter disabled / no fetcher) → 503, anything else → 502.
func failedPDFResponse(re *core.RequestEvent, converter *mineru.Converter, canonical string, job *mineru.Job) error {
	if !converter.Enabled() || job.Phase == mineru.PhaseErrorFetching && converter.DisabledReason() != "" {
		// Disabled-mode messages flow through job.Err already, but
		// hint the status code so the agent gives up vs retries.
		return re.JSON(http.StatusServiceUnavailable, map[string]any{
			"arxiv_id":  canonical,
			"state":     "unavailable",
			"phase":     string(job.Phase),
			"pdf_ready": false,
			"md_ready":  false,
			"detail":    converter.DisabledReason(),
		})
	}

	status := http.StatusBadGateway
	detail := "fetch failed: " + errString(job.Err)
	if errors.Is(job.ErrKind, mineru.ErrFatal) {
		// Fatal in fetch phase usually means arxiv.ErrNotFound.
		// Convert to 404 so agents know to give up.
		status = http.StatusNotFound
	}
	body := map[string]any{
		"arxiv_id":  canonical,
		"state":     "failed",
		"phase":     string(job.Phase),
		"pdf_ready": false,
		"md_ready":  false,
		"kind":      jobKindLabel(job.ErrKind),
		"detail":    detail,
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
	return re.JSON(status, body)
}

// sanitizeFilename converts a canonical arxiv id (which may contain
// '/' for old-style ids like "quant-ph/9508027v2") into a safe
// Content-Disposition filename component. Slash → underscore is the
// minimum needed; nothing else in the canonical alphabet needs escaping.
func sanitizeFilename(canonical string) string {
	out := make([]byte, 0, len(canonical))
	for i := 0; i < len(canonical); i++ {
		c := canonical[i]
		if c == '/' || c == '\\' || c == '"' {
			out = append(out, '_')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
