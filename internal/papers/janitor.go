package papers

import (
	"context"
	"log/slog"
	"time"
)

// JanitorInterval is how often the background janitor sweeps expired
// claims (and, when wired, expired share tokens). Both edges run it;
// the REMOVE/DELETE are idempotent so duplicate sweeps are harmless.
const JanitorInterval = 1 * time.Minute

// ShareGCer is the subset of the shares store the janitor needs. Kept
// as a local interface so papers doesn't import shares (avoids a cycle)
// and so tests can stub it.
type ShareGCer interface {
	GCExpired() (int, error)
}

// RunJanitor blocks until ctx is cancelled, sweeping expired claims
// every JanitorInterval. If shareGC is non-nil, expired share tokens are
// swept in the same tick. Intended to run in its own goroutine started
// at boot.
func (s *Store) RunJanitor(ctx context.Context, shareGC ShareGCer) {
	ticker := time.NewTicker(JanitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx, shareGC)
		}
	}
}

// sweep runs one GC pass. Errors are logged, never fatal — a transient
// Neo4j outage just means the next tick retries.
func (s *Store) sweep(ctx context.Context, shareGC ShareGCer) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if n, err := s.GCExpiredClaims(cctx); err != nil {
		slog.Debug("papers janitor: claim GC failed", "error", err)
	} else if n > 0 {
		slog.Info("papers janitor: expired claims cleared", "count", n)
	}
	if shareGC != nil {
		if n, err := shareGC.GCExpired(); err != nil {
			slog.Debug("papers janitor: share GC failed", "error", err)
		} else if n > 0 {
			slog.Info("papers janitor: expired shares cleared", "count", n)
		}
	}
}
