package downloader

import (
	"context"
	"time"
)

// ProgressEvent is one observable transition in the downloader job
// (mirrors ingest.ProgressEvent so the SPA timeline renders the same
// way; strategy failures appear as events with phase "strategy:<id>").
type ProgressEvent struct {
	Phase  string    `json:"phase"`
	State  string    `json:"state"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// Progress is a downloader job's state plus history, as served by
// GET /api/downloader/jobs.
type Progress struct {
	RequestID string          `json:"request_id,omitempty"`
	PaperID   string          `json:"paper_id"`
	Input     string          `json:"input,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	State     string          `json:"state"` // queued|running|done|failed
	Phase     string          `json:"phase"`
	Active    bool            `json:"active"`
	Strategy  string          `json:"strategy,omitempty"` // winning strategy on success
	Error     string          `json:"error,omitempty"`
	Submitted time.Time       `json:"submitted_at"`
	Updated   time.Time       `json:"updated_at"`
	Finished  time.Time       `json:"finished_at,omitempty"`
	Trace     []Attempt       `json:"trace,omitempty"`
	Events    []ProgressEvent `json:"events"`
}

// maxTerminalProgress bounds how many finished jobs stay listable.
const maxTerminalProgress = 200

func (d *Downloader) beginProgress(ctx context.Context, paperID, input string, kind IdentifierKind) {
	now := time.Now().UTC()
	d.progressMu.Lock()
	d.progress[paperID] = &Progress{
		PaperID:   paperID,
		RequestID: AdmissionID(ctx),
		Input:     input,
		Kind:      string(kind),
		State:     "queued",
		Phase:     "queued",
		Active:    true,
		Submitted: now,
		Updated:   now,
		Events:    []ProgressEvent{{Phase: "queued", State: "queued", At: now}},
	}
	d.progressMu.Unlock()
	d.persistEvent(ctx, paperID, "queued", "queued", "")
}

// transition updates the phase/state and appends an event.
func (d *Downloader) transition(ctx context.Context, paperID, phase, state, detail string, terminal bool) {
	now := time.Now().UTC()
	d.progressMu.Lock()
	p := d.progress[paperID]
	if p == nil {
		p = &Progress{PaperID: paperID, RequestID: AdmissionID(ctx), Submitted: now}
		d.progress[paperID] = p
	}
	if p.RequestID != "" && p.RequestID != AdmissionID(ctx) {
		d.progressMu.Unlock()
		return
	}
	if p.Phase != phase || p.State != state || detail != "" {
		p.Events = append(p.Events, ProgressEvent{Phase: phase, State: state, At: now, Detail: detail})
	}
	p.Phase = phase
	p.State = state
	p.Active = !terminal
	p.Updated = now
	if terminal {
		p.Finished = now
	}
	if state == "failed" {
		p.Error = detail
	} else if state == "done" {
		p.Error = ""
	}
	d.progressMu.Unlock()
	d.persistEvent(ctx, paperID, phase, state, detail)
}

// finishProgress stamps the terminal strategy and prunes old entries
// so the snapshot stays bounded.
func (d *Downloader) finishProgress(ctx context.Context, paperID, state, strategy, errMsg string) error {
	var journalErr error
	if d.cfg.Journal != nil && (state == "done" || state == "failed") {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		journalErr = d.cfg.Journal.FinishDownloadRequest(persistCtx, paperID, AdmissionID(ctx), state, errMsg)
		cancel()
		if journalErr != nil {
			d.log.Error("persist download completion; admission remains recoverable", "paper_id", paperID, "error", journalErr)
		}
	}
	d.progressMu.Lock()
	if p := d.progress[paperID]; p != nil && (p.RequestID == "" || p.RequestID == AdmissionID(ctx)) {
		p.Strategy = strategy
		if state == "done" {
			p.Error = ""
		} else if errMsg != "" {
			p.Error = errMsg
		}
	}
	d.pruneLocked()
	d.progressMu.Unlock()
	return journalErr
}

// recordTrace attaches the strategy trace to the snapshot and persists
// each failed attempt as an audit event (phase "strategy:<id>").
func (d *Downloader) recordTrace(ctx context.Context, paperID string, out *FetchOutcome) {
	if out == nil {
		return
	}
	d.progressMu.Lock()
	if p := d.progress[paperID]; p != nil && (p.RequestID == "" || p.RequestID == AdmissionID(ctx)) {
		p.Trace = append([]Attempt(nil), out.Trace...)
	}
	d.progressMu.Unlock()
	for _, a := range out.Trace {
		if a.Error == "" {
			continue
		}
		detail := a.URL
		if detail == "" {
			detail = a.Error
		} else {
			detail = detail + " — " + a.Error
		}
		d.persistEvent(ctx, paperID, "strategy:"+a.Strategy, "failed", detail)
	}
}

// pruneLocked drops the oldest terminal entries beyond the cap. Caller
// holds progressMu.
func (d *Downloader) pruneLocked() {
	live := 0
	terminal := 0
	for _, p := range d.progress {
		if p.Active {
			live++
		} else {
			terminal++
		}
	}
	if terminal <= maxTerminalProgress {
		return
	}
	// Evict oldest-finished terminal entries first.
	type fin struct {
		id string
		at time.Time
	}
	var fins []fin
	for id, p := range d.progress {
		if !p.Active {
			fins = append(fins, fin{id, p.Finished})
		}
	}
	for i := 0; i < len(fins); i++ {
		for j := i + 1; j < len(fins); j++ {
			if fins[j].at.Before(fins[i].at) {
				fins[i], fins[j] = fins[j], fins[i]
			}
		}
	}
	drop := terminal - maxTerminalProgress
	for i := 0; i < drop && i < len(fins); i++ {
		delete(d.progress, fins[i].id)
	}
}

// persistEvent mirrors ingest's best-effort audit write.
func (d *Downloader) persistEvent(ctx context.Context, paperID, phase, state, detail string) {
	if d.reg == nil {
		return
	}
	writer, ok := d.reg.(acquisitionEventWriter)
	if !ok {
		return
	}
	if err := writer.RecordAcquisitionEvent(ctx, paperID, phase, state, detail); err != nil {
		d.log.Warn("downloader: persist acquisition event failed", "paper_id", paperID, "phase", phase, "error", err)
	}
}

// Snapshot returns all job progresses, newest-submitted first, deep
// copied.
func (d *Downloader) Snapshot() []Progress {
	d.progressMu.Lock()
	out := make([]Progress, 0, len(d.progress))
	for _, p := range d.progress {
		cp := *p
		cp.Events = append([]ProgressEvent(nil), p.Events...)
		cp.Trace = append([]Attempt(nil), p.Trace...)
		out = append(out, cp)
	}
	d.progressMu.Unlock()
	// Include durable overflow and recovered admissions that have not acquired
	// an in-memory slot yet. Do not hold progressMu while accessing PostgreSQL.
	if d.cfg.Journal != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pending, err := d.cfg.Journal.PendingDownloadRequests(ctx, 512)
		cancel()
		if err == nil {
			for _, request := range pending {
				index := -1
				for i := range out {
					if out[i].PaperID == request.PaperID {
						index = i
						break
					}
				}
				if index >= 0 && out[index].RequestID == request.RequestID && out[index].Active {
					continue
				}
				p := Progress{PaperID: request.PaperID, RequestID: request.RequestID, Input: request.Input, Kind: request.Kind, State: "queued", Phase: "queued", Active: true, Submitted: request.CreatedAt, Updated: request.UpdatedAt, Events: []ProgressEvent{}}
				if index >= 0 {
					out[index] = p
				} else {
					out = append(out, p)
				}
			}
		}
	}
	// Newest first; active jobs before terminal ones at equal times.
	sortProgress(out)
	return out
}

func sortProgress(list []Progress) {
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			a, b := list[i], list[j]
			if b.Active != a.Active {
				if b.Active {
					list[i], list[j] = list[j], list[i]
				}
				continue
			}
			if b.Submitted.After(a.Submitted) {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
}
