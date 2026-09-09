package downloader

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type localPermitKey struct{}
type localPermit struct {
	slots chan struct{}
	held  bool
}

func (p *localPermit) acquire(ctx context.Context) error {
	if p.held {
		return nil
	}
	select {
	case p.slots <- struct{}{}:
		p.held = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *localPermit) release() {
	if p.held {
		<-p.slots
		p.held = false
	}
}

func (d *Downloader) remoteEnabled() bool {
	return (d.cfg.Remote != nil && d.cfg.Remote.Enabled()) || d.proxy.Enabled()
}

// tryRemote preserves the whole remote outcome (including canonical identity and
// committed state), not just a body. The trace marker prevents double delegation.
func (d *Downloader) tryRemote(ctx context.Context, ref registry.PaperRef, out *FetchOutcome) bool {
	start := time.Now()
	if IsPMCID(ref.DOI) && out.DOI != "" && !IsPMCID(out.DOI) {
		ref.DOI = out.DOI
	}
	if permit, ok := ctx.Value(localPermitKey{}).(*localPermit); ok {
		permit.release()
		defer func() { _ = permit.acquire(ctx) }()
	}
	if d.cfg.Remote != nil && d.cfg.Remote.Enabled() {
		remote, err := d.cfg.Remote.FetchPDF(ctx, ref)
		if remote != nil {
			out.Trace = append(out.Trace, remote.Trace...)
			out.RemoteTaskID, out.WorkerID = remote.RemoteTaskID, remote.WorkerID
		}
		if err != nil {
			out.Pending = errors.Is(err, ErrRemotePending)
			out.Trace = append(out.Trace, attemptOf("remote-worker", "", err, start))
			return false
		}
		if remote == nil || (!remote.Archived && (remote.Result == nil || remote.Result.Body == nil)) {
			out.Trace = append(out.Trace, attemptOf("remote-worker", "", fmt.Errorf("worker returned no PDF or archive receipt"), start))
			return false
		}
		trace := out.Trace
		*out = *remote
		out.Trace = append(trace, attemptOf("remote-worker", remote.URL, nil, start))
		return true
	}
	res, attempts, strategy, err := d.proxy.FetchPDF(ctx, ref)
	out.Trace = append(out.Trace, attempts...)
	if err != nil {
		out.Trace = append(out.Trace, attemptOf("remote-proxy", "", err, start))
		return false
	}
	out.Result, out.Strategy, out.URL = res, strategy, res.URL
	out.Trace = append(out.Trace, attemptOf(strategy, res.URL, nil, start))
	return true
}

// ArchiveRemote is the fleet's idempotent commit callback. The caller owns Body
// and closes it; success means BOTH object storage and asset registration finished.
// It intentionally does not invoke MinerU: the fleet owns a durable hook outbox.
func (d *Downloader) ArchiveRemote(ctx context.Context, ref registry.PaperRef, out *FetchOutcome) error {
	if d == nil || d.reg == nil || d.store == nil {
		return fmt.Errorf("downloader archive requires registry and object storage")
	}
	if out == nil || out.Result == nil || out.Result.Body == nil {
		return fmt.Errorf("downloader archive requires validated PDF bytes")
	}
	minter, ok := d.reg.(interface {
		ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error)
	})
	if !ok {
		return fmt.Errorf("downloader registry cannot resolve paper identity")
	}
	// Retain bibliographic identity while carrying the actual resolved version.
	if out.DOI != "" {
		ref.DOI = out.DOI
	}
	if out.ArxivCanonical != "" {
		ref.ArxivID = out.ArxivCanonical
	}
	paperID, _, err := minter.ResolveOrMint(ctx, ref)
	if err != nil {
		return fmt.Errorf("resolve remote paper: %w", err)
	}
	j := job{paperID: paperID, ref: ref, ctx: ctx}
	if err := d.storeOutcome(ctx, j, out); err != nil {
		return err
	}
	d.recordTrace(ctx, paperID, out)
	d.transition(ctx, paperID, "pdf_ready", "done", out.Strategy, true)
	return d.finishProgress(ctx, paperID, "done", out.Strategy, "")
}

// AfterRemoteArchive is retried by the fleet until it returns nil. PDF-ready
// consumers must coalesce duplicate calls; a crash after a hook is at-least-once.
func (d *Downloader) AfterRemoteArchive(ctx context.Context, ref registry.PaperRef, out *FetchOutcome) error {
	if out != nil {
		if out.ArxivCanonical != "" {
			ref.ArxivID = out.ArxivCanonical
		}
		if out.DOI != "" {
			ref.DOI = out.DOI
		}
	}
	canonical, isDOI := ref.DOI, true
	if ref.ArxivID != "" {
		canonical, isDOI = ref.ArxivID, false
	}
	if canonical == "" {
		return fmt.Errorf("remote hook: missing paper identity")
	}
	if d.onPDFReady != nil {
		d.onPDFReady(ctx, canonical, isDOI)
	}
	if d.pusher != nil {
		return d.pusher.PushIndex(ctx, canonical)
	}
	return nil
}
