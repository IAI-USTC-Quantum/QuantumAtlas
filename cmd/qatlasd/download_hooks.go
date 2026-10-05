package main

import (
	"context"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloadfleet"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type remoteArchiveConverter interface {
	Enabled() bool
	Ensure(context.Context, string) *mineru.Job
	EnsureByDOI(context.Context, string, string) *mineru.Job
}

// PDF archive outboxes reconcile PDF storage only. Parsing is deliberately not
// started by download completion/recovery: a later content read authorizes it.
func durableRemoteHooks(_ remoteArchiveConverter, after downloadfleet.ArchiveFunc) downloadfleet.ArchiveFunc {
	return func(ctx context.Context, ref registry.PaperRef, out *downloader.FetchOutcome) error {
		if after != nil {
			return after(ctx, ref, out)
		}
		return nil
	}
}
