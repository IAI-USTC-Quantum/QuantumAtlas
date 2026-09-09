package main

import (
	"context"
	"fmt"

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

// durableRemoteHooks keeps the post-archive outbox pending until conversion is
// durably visible, not merely queued in a converter's memory. This is invoked
// AFTER the worker's archive receipt transaction; it never holds worker cleanup
// hostage to MinerU. Replaying Ensure repairs a crash-lost DOI or arXiv job.
func durableRemoteHooks(converter remoteArchiveConverter, after downloadfleet.ArchiveFunc) downloadfleet.ArchiveFunc {
	return func(ctx context.Context, ref registry.PaperRef, out *downloader.FetchOutcome) error {
		if converter != nil && converter.Enabled() {
			doi, arxivID := ref.DOI, ref.ArxivID
			if out != nil {
				if out.DOI != "" {
					doi = out.DOI
				}
				if out.ArxivCanonical != "" {
					arxivID = out.ArxivCanonical
				}
			}
			var job *mineru.Job
			if arxivID != "" {
				job = converter.Ensure(ctx, arxivID)
			} else if doi != "" {
				job = converter.EnsureByDOI(ctx, doi, "")
			}
			if job == nil {
				return fmt.Errorf("post-archive conversion has no paper identity")
			}
			if job.State != mineru.JobStateDone {
				return fmt.Errorf("post-archive conversion %s; retained for reconciliation", job.State)
			}
		}
		if after != nil {
			return after(ctx, ref, out)
		}
		return nil
	}
}
