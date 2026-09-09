package downloader

import (
	"context"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type admissionIDKey struct{}

// WithAdmissionID carries a durable admission generation through local and remote
// retries. A new explicit user retry receives a new ID; recovery preserves it.
func WithAdmissionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, admissionIDKey{}, id)
}
func AdmissionID(ctx context.Context) string {
	id, _ := ctx.Value(admissionIDKey{}).(string)
	return id
}

// DownloadJournal preserves accepted work across a main-server restart even
// before the local-first ladder reaches a remote worker. No worker credentials
// or browser sessions are stored in this journal.
type DownloadJournal interface {
	SaveDownloadRequest(context.Context, string, string, string, registry.PaperRef) (string, error)
	FinishDownloadRequest(context.Context, string, string, string, string) error
	PendingDownloadRequests(context.Context, int) ([]registry.DownloadRequest, error)
	PruneDownloadRequests(context.Context, time.Duration) error
}

// RunRecovery coalesces pending admissions with running jobs using singleflight.
// Call only after the server's single-process data lock is held.
func (d *Downloader) RunRecovery(ctx context.Context) {
	if d.cfg.Journal == nil {
		return
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		passCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		// Fleet mode is the sole owner of legacy pending papers too. Adoption
		// is retried here because the registry may still be migrating at boot.
		if adopter, ok := d.cfg.Journal.(interface {
			AdoptPendingDownloadRequests(context.Context, int) error
		}); ok {
			if err := adopter.AdoptPendingDownloadRequests(passCtx, 128); err != nil {
				d.log.Warn("download pending adoption", "error", err)
			}
		}
		requests, err := d.cfg.Journal.PendingDownloadRequests(passCtx, 128)
		if err != nil {
			d.log.Warn("download admission recovery", "error", err)
		} else {
			for _, r := range requests {
				if !d.enqueueAdmitted(WithAdmissionID(ctx, r.RequestID), r.PaperID, r.Input, IdentifierKind(r.Kind), r.Ref) {
					break
				}
			}
		}
		if err := d.cfg.Journal.PruneDownloadRequests(passCtx, 7*24*time.Hour); err != nil {
			d.log.Warn("download admission retention", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-d.done:
			return
		case <-tick.C:
		}
	}
}
