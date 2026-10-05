package routes

import (
	"context"
	"errors"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/pocketbase/pocketbase/core"
)

// Markdown is always the original member of a verified immutable bundle.
func markdownHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	catalog, ok, err := legacyContentCatalog(re, cfg, converter)
	if !ok {
		return err
	}
	return contentDerivativeHandler(re, cfg, store, catalog, converter, arxivID, "markdown")
}

func markdownStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	catalog, ok, err := legacyContentCatalog(re, cfg, converter)
	if !ok {
		return err
	}
	return contentReadStatusHandler(re, cfg, store, catalog, converter, arxivID)
}

// probeAssetReadiness reports whether PDF + markdown are present in
// the store right now. Read-only, no writes. Cheap enough to call from
// status endpoints — the underlying LocateAssetByID does two HEADs at
// most when dual-read fallback fires.
func probeAssetReadiness(ctx context.Context, store objstore.Store, canonical string) (pdfReady, mdReady bool, err error) {
	ctx, cancel := objstore.ReadContext(ctx)
	defer cancel()
	_, _, pdfReady, err = paperassets.LocateAssetByID(ctx, store, "pdf", canonical)
	if err != nil {
		return false, false, err
	}
	// Historical markdown is deliberately not a complete-bundle readiness signal.
	mdReady = false
	return pdfReady, mdReady, err
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
