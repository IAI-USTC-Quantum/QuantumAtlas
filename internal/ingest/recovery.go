package ingest

import (
	"context"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

const recoveryBatchSize = 200

type pendingReader interface {
	PendingPapers(ctx context.Context, after string, limit int) ([]registry.PendingPaper, error)
}

// RecoverPending replays every persisted pending paper through OnMint.
// Keyset pagination remains stable while workers concurrently update
// earlier rows. OnMint/singleflight preserve normal queue bounds and
// duplicate suppression.
func (i *Ingester) RecoverPending(ctx context.Context) (int, error) {
	if i == nil || i.fetcher == nil || i.closed.Load() {
		return 0, nil
	}
	reader, ok := i.reg.(pendingReader)
	if !ok {
		return 0, fmt.Errorf("ingest: registry does not support pending recovery")
	}

	total := 0
	after := ""
	for {
		rows, err := reader.PendingPapers(ctx, after, recoveryBatchSize)
		if err != nil {
			return total, err
		}
		for _, row := range rows {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			i.OnMint(ctx, row.PaperID, row.Ref)
			after = row.PaperID
			total++
		}
		if len(rows) < recoveryBatchSize {
			return total, nil
		}
	}
}
