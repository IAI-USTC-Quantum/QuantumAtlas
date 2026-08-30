package ingest

import "time"

// ProgressEvent is one observable transition in the PDF acquisition
// pipeline. The history is process-local but remains available after a
// job reaches a terminal state, which lets the UI render a compact
// timeline without introducing another persistence table.
type ProgressEvent struct {
	Phase  string    `json:"phase"`
	State  string    `json:"state"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// Progress is the current PDF acquisition state plus its transition
// history. SnapshotFor always returns a deep copy.
type Progress struct {
	PaperID   string          `json:"paper_id"`
	State     string          `json:"state"`
	Phase     string          `json:"phase"`
	Active    bool            `json:"active"`
	Submitted time.Time       `json:"submitted_at"`
	Updated   time.Time       `json:"updated_at"`
	Finished  time.Time       `json:"finished_at,omitempty"`
	Error     string          `json:"error,omitempty"`
	Events    []ProgressEvent `json:"events"`
}

func (i *Ingester) beginProgress(paperID string) {
	now := time.Now().UTC()
	i.progressMu.Lock()
	i.progress[paperID] = &Progress{
		PaperID:   paperID,
		State:     "queued",
		Phase:     "queued",
		Active:    true,
		Submitted: now,
		Updated:   now,
		Events: []ProgressEvent{{
			Phase: "queued", State: "queued", At: now,
		}},
	}
	i.progressMu.Unlock()
}

func (i *Ingester) transition(paperID, phase, state, detail string, terminal bool) {
	now := time.Now().UTC()
	i.progressMu.Lock()
	p := i.progress[paperID]
	if p == nil {
		p = &Progress{PaperID: paperID, Submitted: now}
		i.progress[paperID] = p
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
	}
	i.progressMu.Unlock()
}

// SnapshotFor returns a deep-copy snapshot for one registry paper.
func (i *Ingester) SnapshotFor(paperID string) (Progress, bool) {
	if i == nil {
		return Progress{}, false
	}
	i.progressMu.Lock()
	defer i.progressMu.Unlock()
	p, ok := i.progress[paperID]
	if !ok {
		return Progress{}, false
	}
	cp := *p
	cp.Events = append([]ProgressEvent(nil), p.Events...)
	return cp, true
}
