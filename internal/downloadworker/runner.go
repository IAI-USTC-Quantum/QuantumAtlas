package downloadworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

type Fetcher interface {
	FetchPDF(context.Context, registry.PaperRef) (*downloader.FetchOutcome, error)
}
type activeTask struct {
	cancel context.CancelFunc
	lease  time.Time
}
type Runner struct {
	Config         Config
	Spool          *Spool
	Client         *Client
	Identity       Identity
	Fetcher        Fetcher
	BrowserHealthy func(context.Context) bool
	Log            *slog.Logger
	mu             sync.Mutex
	active         map[string]activeTask
	wg             sync.WaitGroup
	allowed        atomic.Bool
	fatal          chan error
}

type Health struct {
	Updated       time.Time `json:"updated"`
	Healthy       bool      `json:"healthy"`
	Status        string    `json:"status"`
	BrowserOK     bool      `json:"browser_ok"`
	SpoolBytes    int64     `json:"spool_bytes"`
	DiskFreeBytes int64     `json:"disk_free_bytes"`
	Running       int       `json:"running"`
	LastError     string    `json:"last_error,omitempty"`
}

func CheckHealth(dir string, now time.Time) error {
	b, err := os.ReadFile(filepath.Join(dir, "health.json"))
	if err != nil {
		return errors.New("worker health unavailable")
	}
	var h Health
	if json.Unmarshal(b, &h) != nil || !h.Healthy || now.Sub(h.Updated) > 90*time.Second || h.Updated.After(now.Add(5*time.Second)) {
		return errors.New("worker is unhealthy or heartbeat stale")
	}
	return nil
}
func (r *Runner) health(status string, browser bool, message string) error {
	used, free := r.Spool.Usage()
	r.mu.Lock()
	running := len(r.active)
	r.mu.Unlock()
	h := Health{Updated: time.Now().UTC(), Healthy: message == "" && status == "approved" && browser && r.Spool.Slots(r.Config.Concurrency) > 0, Status: status, BrowserOK: browser, SpoolBytes: used, DiskFreeBytes: free, Running: running, LastError: message}
	// A fully occupied worker is healthy when its reservation can complete.
	if message == "" && status == "approved" && browser && running > 0 && free > 1<<20 {
		h.Healthy = true
	}
	return atomicJSON(filepath.Join(r.Config.DataDir, "health.json"), h)
}
func (r *Runner) fatalError(err error) {
	select {
	case r.fatal <- err:
	default:
	}
}

func (r *Runner) Run(parent context.Context) error {
	if err := r.Config.Validate(); err != nil {
		return err
	}
	if r.Spool == nil || r.Client == nil || r.Fetcher == nil || r.BrowserHealthy == nil {
		return errors.New("incomplete worker dependencies")
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
	r.active = map[string]activeTask{}
	r.fatal = make(chan error, 1)
	ctx, cancel := context.WithCancel(parent)
	defer func() { cancel(); r.wg.Wait(); _ = os.Remove(filepath.Join(r.Config.DataDir, "health.json")) }()
	r.wg.Add(2)
	go func() { defer r.wg.Done(); r.uploadLoop(ctx) }()
	go func() { defer r.wg.Done(); r.leaseLoop(ctx) }()
	ticker := time.NewTicker(r.Config.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.cycle(ctx); err != nil {
			// Only fixed local messages are emitted; upstream bodies/URLs never enter logs.
			r.Log.Warn("worker control request failed; retrying")
			if e := r.health("unavailable", false, "master communication unavailable"); e != nil {
				return errors.New("cannot persist worker health")
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-r.fatal:
			return err
		case <-ticker.C:
		}
	}
}
func (r *Runner) cycle(ctx context.Context) error {
	node, err := r.Client.Status(ctx)
	if err != nil && !r.Identity.Registered {
		var status *HTTPError
		if errors.As(err, &status) && status.Status == 401 {
			if r.Config.EnrollmentToken == "" {
				return errors.New("enrollment credential required")
			}
			node, err = r.Client.Register(ctx, workerprotocol.RegisterRequest{ID: r.Identity.ID, Name: r.Config.Name, Secret: r.Identity.Secret, EnrollmentToken: r.Config.EnrollmentToken})
		}
	}
	if err != nil {
		return err
	}
	if node.ID != r.Identity.ID {
		return errors.New("master returned a different worker identity")
	}
	if !r.Identity.Registered {
		r.Identity.Registered = true
		if err = r.Spool.SaveIdentity(r.Identity); err != nil {
			return err
		}
		r.Config.EnrollmentToken = ""
	}
	allowed := node.Status == "approved" || node.Status == "draining"
	r.allowed.Store(allowed)
	if !allowed {
		r.mu.Lock()
		for _, a := range r.active {
			a.cancel()
		}
		r.mu.Unlock()
		return r.health(node.Status, false, "")
	}
	browser := r.BrowserHealthy(ctx)
	records := r.Spool.Records()
	// The protocol caps heartbeat IDs at 128. Always include executing tasks
	// first; old retained PDFs must not crowd their renewable leases out.
	ids := make([]string, 0, 128)
	r.mu.Lock()
	for id := range r.active {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, record := range records {
		if len(ids) >= 128 {
			break
		}
		if record.State == "ready" && record.Assignment.Deadline.After(time.Now()) {
			duplicate := false
			for _, id := range ids {
				if id == record.Assignment.AttemptID {
					duplicate = true
					break
				}
			}
			if !duplicate {
				ids = append(ids, record.Assignment.AttemptID)
			}
		}
	}
	used, free := r.Spool.Usage()
	beat, err := r.Client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{Capacity: r.Config.Concurrency, BrowserOK: browser, DiskFreeBytes: free, SpoolBytes: used, RunningAttemptIDs: ids})
	if err != nil {
		return err
	}
	r.mu.Lock()
	for id, a := range r.active {
		lease, ok := beat.LeaseExpires[id]
		if !ok {
			a.cancel()
		} else {
			a.lease = lease
			r.active[id] = a
		}
	}
	r.mu.Unlock()
	r.allowed.Store(beat.Status == "approved" || beat.Status == "draining")
	if err = r.health(beat.Status, browser, ""); err != nil {
		return err
	}
	if beat.Status != "approved" || !browser {
		return nil
	}
	slots := r.Spool.Slots(r.Config.Concurrency)
	if slots == 0 {
		return nil
	}
	claimed, err := r.Client.Claim(ctx, slots)
	if err != nil {
		return err
	}
	if len(claimed.Attempts) > slots {
		return errors.New("master exceeded requested claim capacity")
	}
	for _, a := range claimed.Attempts {
		now := time.Now()
		if !a.LeaseExpires.After(now) || !a.Deadline.After(now) {
			return errors.New("master returned expired assignment")
		}
		if err = r.Spool.Start(a, r.Config.Concurrency, now); err != nil {
			return err
		}
		r.startTask(ctx, a)
	}
	return nil
}
func (r *Runner) startTask(ctx context.Context, a workerprotocol.Assignment) {
	deadline := time.Now().Add(r.Config.TaskTimeout)
	if a.Deadline.Before(deadline) {
		deadline = a.Deadline
	}
	taskCtx, cancel := context.WithDeadline(ctx, deadline)
	r.mu.Lock()
	r.active[a.AttemptID] = activeTask{cancel: cancel, lease: a.LeaseExpires}
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		defer func() { r.mu.Lock(); delete(r.active, a.AttemptID); r.mu.Unlock() }()
		defer func() {
			if recover() != nil {
				if err := r.Spool.Fail(a.AttemptID, workerprotocol.FailureInternal, "download worker panic"); err != nil {
					r.fatalError(errors.New("cannot persist interrupted attempt"))
				}
			}
		}()
		ref := registry.PaperRef{ArxivID: a.Ref.ArxivID, DOI: a.Ref.DOI, OpenAlexID: a.Ref.OpenAlexID, Title: a.Ref.Title, Authors: a.Ref.Authors, Year: a.Ref.Year}
		result, err := r.Fetcher.FetchPDF(taskCtx, ref)
		if result != nil && result.Result != nil {
			if closer, ok := result.Result.Body.(io.Closer); ok {
				defer closer.Close()
			}
		}
		if err == nil && result != nil && result.Result != nil && result.Result.Body != nil {
			if e := r.Spool.Ready(a.AttemptID, result.Result.Body, resultMetadata(result), time.Now().UTC()); e == nil {
				return
			}
			// An fsync/rename error can leave the commit outcome uncertain. Stop
			// admission and let recovery inspect the durable catalog; do not
			// overwrite a possibly committed ready result with a failure.
			r.fatalError(errors.New("cannot persist downloaded PDF"))
			return
		}
		code, message := workerprotocol.FailureNotFound, "download strategies did not produce a PDF"
		if taskCtx.Err() != nil {
			code = workerprotocol.FailureTimeout
			message = "download interrupted or deadline exceeded"
		} else if errors.Is(err, downloader.ErrNoIdentity) {
			code = workerprotocol.FailureInvalidIdentifier
			message = "assignment has no supported identity"
		}
		if err = r.Spool.Fail(a.AttemptID, code, message); err != nil {
			r.fatalError(errors.New("cannot persist download failure"))
		}
	}()
}
func (r *Runner) leaseLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.mu.Lock()
			for _, a := range r.active {
				if !a.lease.After(now) {
					a.cancel()
				}
			}
			r.mu.Unlock()
		}
	}
}
func (r *Runner) uploadLoop(ctx context.Context) {
	ticker := time.NewTicker(r.Config.PollInterval)
	defer ticker.Stop()
	for {
		if r.allowed.Load() {
			for _, record := range r.Spool.Records() {
				if ctx.Err() != nil {
					return
				}
				if err := r.deliver(ctx, record); err != nil {
					r.Log.Warn("worker result delivery deferred; retained for retry")
				}
			}
		}
		if err := r.Spool.Expire(time.Now()); err != nil {
			r.fatalError(errors.New("cannot expire spool result"))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (r *Runner) deliver(ctx context.Context, record Record) error {
	id := record.Assignment.AttemptID
	if record.State == "failed" || record.State == "expired" {
		receipt, err := r.Client.Report(ctx, record)
		if err != nil {
			return err
		}
		return r.Spool.Reported(id, receipt)
	}
	if record.State != "ready" {
		return nil
	}
	current, file, err := r.Spool.BeginUpload(id)
	if err != nil {
		return err
	}
	defer r.Spool.EndUpload(id)
	defer file.Close()
	receipt, err := r.Client.Receipt(ctx, id)
	if err == nil {
		archived, e := r.Spool.Archived(id, receipt)
		if e != nil || archived {
			return e
		}
		if receipt.State == "staged" || receipt.State == "done" || receipt.State == "failed" || receipt.State == "expired" {
			return nil
		}
	} else {
		var status *HTTPError
		if !errors.As(err, &status) || status.Status != 404 {
			return err
		}
	}
	receipt, err = r.Client.Upload(ctx, current, file)
	if err != nil {
		return err
	}
	_, err = r.Spool.Archived(id, receipt)
	return err
}
