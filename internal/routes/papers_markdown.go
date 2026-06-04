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
// Routing: only invoked when cfg.AssetDownloadsEnabled is true.
func markdownHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for markdown: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()
	job := converter.Ensure(ctx, canonical)

	switch job.State {
	case mineru.JobStateDone:
		return streamMarkdown(re, store, canonical)
	case mineru.JobStateQueued, mineru.JobStateRunning:
		re.Response.Header().Set("Operation-Location", fmt.Sprintf("/api/papers/%s/markdown/status", canonical))
		re.Response.Header().Set("Retry-After", "5")
		return re.JSON(http.StatusAccepted, map[string]any{
			"detail":   "conversion in progress",
			"arxiv_id": canonical,
			"state":    string(job.State),
		})
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
			// errString already carries the converter-built quota
			// message ("server quota exhausted: all N MinerU API keys
			// ... — service resumes at <ISO time>"). Make sure the
			// "server quota exhausted" framing wins over the generic
			// "conversion failed" prefix so the client renders the
			// quota story, not a per-paper failure.
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
			// Round up to the next whole second, then floor to 1 — int(...)
			// truncates toward zero so without the floor a CooldownUntil
			// already in the past (e.g. the row was re-read just after
			// expiry) would emit a negative Retry-After value, which is
			// not valid per RFC 7231 §7.1.3.
			secs := int(time.Until(job.CooldownUntil).Seconds()) + 1
			if secs < 1 {
				secs = 1
			}
			re.Response.Header().Set("Retry-After", strconv.Itoa(secs))
		}
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
// Response codes:
//
//   200  status payload {state, ...}
//   400  malformed arxiv id
//   404  no record of this paper (never submitted, no PDF, …) per
//        rubber-duck recommendation in issue #8
//
// Possible state strings: cached, queued, running, none, failed, cooldown, unavailable.
func markdownStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for markdown/status: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()

	// 1) Cache hit — even if we have no in-flight job for this id.
	mdKey := paperassets.AssetKey("markdown", canonical)
	if _, exists, err := store.Stat(ctx, mdKey); err == nil && exists {
		return re.JSON(http.StatusOK, map[string]any{
			"arxiv_id":     canonical,
			"state":        "cached",
			"markdown_url": "/api/papers/" + canonical + "/markdown",
		})
	}

	// 2) In-flight / recently-failed job?
	if job, ok := converter.Lookup(canonical); ok {
		body := map[string]any{
			"arxiv_id": canonical,
		}
		switch job.State {
		case mineru.JobStateQueued:
			body["state"] = "queued"
		case mineru.JobStateRunning:
			body["state"] = "running"
			if !job.StartedAt.IsZero() {
				body["started_at"] = job.StartedAt.UTC().Format(time.RFC3339)
			}
		case mineru.JobStateFailed:
			if !job.CooldownUntil.IsZero() && time.Now().Before(job.CooldownUntil) {
				body["state"] = "cooldown"
				body["retry_after"] = job.CooldownUntil.Unix()
				body["retry_after_iso"] = job.CooldownUntil.UTC().Format(time.RFC3339)
			} else {
				body["state"] = "failed"
			}
			body["kind"] = jobKindLabel(job.ErrKind)
			if job.Err != nil {
				body["detail"] = job.Err.Error()
			}
		case mineru.JobStateDone:
			// Stat above said no cache; converter says done — race or
			// stale state. Report as cached optimistically.
			body["state"] = "cached"
			body["markdown_url"] = "/api/papers/" + canonical + "/markdown"
		}
		return re.JSON(http.StatusOK, body)
	}

	// 3) No cache, no job — does the PDF even exist?
	pdfKey := paperassets.AssetKey("pdf", canonical)
	if _, exists, err := store.Stat(ctx, pdfKey); err == nil && !exists {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail":   "paper unknown: no PDF in catalog",
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
		"arxiv_id": canonical,
		"state":    state,
	}
	if !converter.Enabled() {
		body["detail"] = converter.DisabledReason()
	}
	return re.JSON(http.StatusOK, body)
}

// streamMarkdown copies the cached markdown bytes from object store to
// the response body. Sets Content-Type and Content-Length when the
// backend reports them.
func streamMarkdown(re *core.RequestEvent, store objstore.Store, canonical string) error {
	mdKey := paperassets.AssetKey("markdown", canonical)
	rc, info, err := store.Get(re.Request.Context(), mdKey)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
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
