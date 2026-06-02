package papers

import (
	"context"
	"log/slog"
	"time"
)

// JanitorInterval is how often the background janitor sweeps expired
// claims. Both edges run it; the REMOVE/DELETE are idempotent so
// duplicate sweeps are harmless.
const JanitorInterval = 1 * time.Minute

// RunJanitor blocks until ctx is cancelled, sweeping expired claims
// every JanitorInterval. Intended to run in its own goroutine started
// at boot.
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
// Neo4j outage just means the next tick retries.
func (s *Store) sweep(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if n, err := s.GCExpiredClaims(cctx); err != nil {
		slog.Debug("papers janitor: claim GC failed", "error", err)
	} else if n > 0 {
		slog.Info("papers janitor: expired claims cleared", "count", n)
	}
}
