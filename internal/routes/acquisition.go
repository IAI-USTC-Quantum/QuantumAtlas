package routes

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/ingest"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type acquisitionEvent struct {
	Phase  string    `json:"phase"`
	State  string    `json:"state"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// paperAcquisition merges the PDF ingester and MinerU converter into one
// UI-facing status. Calling it also idempotently starts conversion when
// a PDF exists without markdown; this makes search-card polling useful
// for both newly ingested and pre-existing published assets.
func paperAcquisition(
	ctx context.Context,
	p *registry.Paper,
	assets []registry.Asset,
	ingester *ingest.Ingester,
	converter *mineru.Converter,
) map[string]any {
	state := "idle"
	phase := "idle"
	active := false
	var errText string
	events := make([]acquisitionEvent, 0, 10)

	if progress, ok := ingester.SnapshotFor(p.PaperID); ok {
		state, phase, active = progress.State, progress.Phase, progress.Active
		errText = progress.Error
		for _, event := range progress.Events {
			events = append(events, acquisitionEvent{
				Phase: event.Phase, State: event.State, At: event.At, Detail: event.Detail,
			})
		}
	} else if p.Status == "pending" {
		state, phase, active = "queued", "queued", true
		events = append(events, acquisitionEvent{Phase: "queued", State: "queued", At: p.CreatedAt})
	} else if p.Status == "failed" {
		state, phase = "failed", "failed"
	}

	asset := defaultAcquisitionAsset(assets)
	if asset != nil && !asset.FetchedAt.IsZero() && !hasAcquisitionPhase(events, "pdf_ready") {
		events = append(events, acquisitionEvent{Phase: "pdf_ready", State: "done", At: asset.FetchedAt})
	}
	job := ensureAndLookupConversion(ctx, p, asset, converter)
	if job != nil {
		appendMinerUEvents(&events, job)
		switch job.State {
		case mineru.JobStateQueued:
			state, phase, active = "queued", "mineru_queued", true
		case mineru.JobStateRunning:
			state, active = "running", true
			phase = string(job.Phase)
			if phase == "" {
				phase = "mineru_running"
			}
		case mineru.JobStateDone:
			state, phase, active = "done", "ready", false
		case mineru.JobStateFailed:
			state, phase, active = "failed", string(job.Phase), false
			if job.Err != nil {
				errText = job.Err.Error()
			}
		}
	} else if asset != nil {
		if asset.MinerUMDPath != "" {
			state, phase, active = "done", "ready", false
			if !asset.FetchedAt.IsZero() && !hasAcquisitionPhase(events, "ready") {
				events = append(events, acquisitionEvent{Phase: "ready", State: "done", At: asset.FetchedAt})
			}
		} else {
			state, phase = "queued", "waiting_mineru"
			active = converter != nil && converter.Enabled()
		}
	}

	sort.SliceStable(events, func(a, b int) bool { return events[a].At.Before(events[b].At) })
	body := map[string]any{
		"state":  state,
		"phase":  phase,
		"active": active,
		"events": events,
	}
	if errText != "" {
		body["error"] = errText
	}
	if len(events) > 0 {
		body["updated_at"] = events[len(events)-1].At
	}
	if job != nil && job.Queue != nil {
		body["queue"] = map[string]any{
			"position":       job.Queue.Position,
			"ahead_of_me":    job.Queue.AheadOfMe,
			"running_count":  job.Queue.RunningCount,
			"max_concurrent": job.Queue.MaxConcurrent,
			"eta_seconds":    job.Queue.EtaSeconds,
		}
	}
	return body
}

func hasAcquisitionPhase(events []acquisitionEvent, phase string) bool {
	for _, event := range events {
		if event.Phase == phase {
			return true
		}
	}
	return false
}

func defaultAcquisitionAsset(assets []registry.Asset) *registry.Asset {
	if len(assets) == 0 {
		return nil
	}
	return &assets[0] // registry.Assets already orders published/default first.
}

func ensureAndLookupConversion(ctx context.Context, p *registry.Paper, asset *registry.Asset, c *mineru.Converter) *mineru.Job {
	if c == nil || asset == nil {
		return nil
	}
	if asset.Source == "published" && p.DOI != "" {
		if asset.MinerUMDPath == "" && c.Enabled() {
			c.EnsureByDOI(ctx, p.DOI, "")
		}
		job, _ := c.LookupDOI(p.DOI)
		return job
	}
	if asset.Source == "arxiv" && p.ArxivID != "" {
		canonical := p.ArxivID
		if asset.ArxivVersion > 0 {
			canonical += "v" + strconv.Itoa(asset.ArxivVersion)
		}
		if asset.MinerUMDPath == "" && c.Enabled() {
			c.Ensure(ctx, canonical)
		}
		job, _ := c.Lookup(canonical)
		return job
	}
	return nil
}

func appendMinerUEvents(events *[]acquisitionEvent, job *mineru.Job) {
	if !job.SubmittedAt.IsZero() {
		*events = append(*events, acquisitionEvent{Phase: "mineru_queued", State: "queued", At: job.SubmittedAt})
	}
	if !job.StartedAt.IsZero() {
		*events = append(*events, acquisitionEvent{Phase: "mineru_started", State: "running", At: job.StartedAt})
	}
	if job.Fetch != nil {
		if !job.Fetch.StartedAt.IsZero() {
			*events = append(*events, acquisitionEvent{Phase: "downloading_pdf", State: "running", At: job.Fetch.StartedAt})
		}
		if !job.Fetch.CompletedAt.IsZero() {
			*events = append(*events, acquisitionEvent{Phase: "pdf_ready", State: "running", At: job.Fetch.CompletedAt})
		}
	}
	if job.Convert != nil && !job.Convert.StartedAt.IsZero() {
		detail := job.Convert.Stage
		*events = append(*events, acquisitionEvent{Phase: "converting_md", State: "running", At: job.Convert.StartedAt, Detail: detail})
	}
	if !job.FinishedAt.IsZero() {
		phase, state := "ready", "done"
		detail := ""
		if job.State == mineru.JobStateFailed {
			phase, state = string(job.Phase), "failed"
			if job.Err != nil {
				detail = job.Err.Error()
			}
		}
		*events = append(*events, acquisitionEvent{Phase: phase, State: state, At: job.FinishedAt, Detail: detail})
	}
}
