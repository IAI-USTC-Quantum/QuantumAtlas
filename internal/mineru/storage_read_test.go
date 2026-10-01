package mineru

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

type failedCacheProbe struct {
	objstore.Store
	deadline time.Time
	err      error
}

func (s *failedCacheProbe) Stat(ctx context.Context, _ string) (objstore.ObjectInfo, bool, error) {
	s.deadline, _ = ctx.Deadline()
	return objstore.ObjectInfo{}, false, s.err
}

func TestCacheProbeFailureDoesNotAcquire(t *testing.T) {
	for _, entry := range []string{"markdown", "pdf", "doi"} {
		t.Run(entry, func(t *testing.T) {
			err := errors.New("connection reset by peer")
			store := &failedCacheProbe{Store: newFakeStore(), err: err}
			c := NewConverter(ConverterConfig{PaperAccessEnabled: true, MinerUAPITokens: []string{"test"}}, store, nil, nil)
			var job *Job
			switch entry {
			case "markdown":
				job = c.Ensure(context.Background(), "1010.4458v2")
			case "pdf":
				job = c.EnsurePDF(context.Background(), "1010.4458v2")
			case "doi":
				job = c.EnsureByDOI(context.Background(), "10.1103/test", "")
			}
			if job.State != JobStateFailed || !errors.Is(job.Err, objstore.ErrUnavailable) || !errors.Is(job.Err, err) || !errors.Is(job.ErrKind, ErrRetryable) {
				t.Fatalf("wrong failure classification: %+v", job)
			}
			if store.deadline.IsZero() || time.Until(store.deadline) > objstore.ReadTimeout {
				t.Fatal("unbounded cache probe")
			}
			if len(c.jobs) != 0 || c.Snapshot().Submitted != 0 || c.Snapshot().CacheMisses != 0 {
				t.Fatal("failed cache probe mutated acquisition state")
			}
			store.err = nil
			// Storage is reachable now and empty; use cache-only mode so recovery
			// follows the ordinary missing-asset contract without a network call.
			c.enabled = false
			job = c.Ensure(context.Background(), "1010.4458v2")
			if errors.Is(job.Err, objstore.ErrUnavailable) || c.Snapshot().CacheMisses != 1 {
				t.Fatal("storage failure was retained after recovery")
			}
		})
	}
}
