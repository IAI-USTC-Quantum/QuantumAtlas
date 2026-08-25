package registry

import (
	"context"
	"log/slog"
	"time"
)

// JanitorInterval is how often the background janitor sweeps expired
// leases. Every edge runs it; the UPDATE is idempotent so duplicate
// sweeps are harmless.
const JanitorInterval = 1 * time.Minute

// RunJanitor blocks until ctx is cancelled, sweeping expired MinerU
// leases every JanitorInterval. Intended to run in its own goroutine
// started at boot.
func (s *Store) RunJanitor(ctx context.Context) {
	ticker := time.NewTicker(JanitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// sweep runs one GC pass. Errors are logged, never fatal — a transient
// PostgreSQL outage just means the next tick retries.
func (s *Store) sweep(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if n, err := s.GCExpiredLeases(cctx); err != nil {
		slog.Debug("registry janitor: lease GC failed", "error", err)
	} else if n > 0 {
		slog.Info("registry janitor: expired leases cleared", "count", n)
	}
}
