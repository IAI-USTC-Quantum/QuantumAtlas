package main

import (
	"context"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type hookConverter struct {
	enabled    bool
	state      mineru.JobState
	doi, arxiv string
	calls      int
}

func (c *hookConverter) Enabled() bool { return c.enabled }
func (c *hookConverter) Ensure(_ context.Context, id string) *mineru.Job {
	c.arxiv = id
	c.calls++
	return &mineru.Job{State: c.state}
}
func (c *hookConverter) EnsureByDOI(_ context.Context, id, _ string) *mineru.Job {
	c.doi = id
	c.calls++
	return &mineru.Job{State: c.state}
}
func TestRemoteHookReconcilesVolatileDOIConversion(t *testing.T) {
	converter := &hookConverter{enabled: true, state: mineru.JobStateQueued}
	afterCalls := 0
	after := func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { afterCalls++; return nil }
	hook := durableRemoteHooks(converter, after)
	ctx := context.Background()
	ref := registry.PaperRef{DOI: "10.1000/recover-hook"}
	if err := hook(ctx, ref, nil); err == nil || afterCalls != 0 {
		t.Fatal("memory-only queued conversion acknowledged as durable")
	}
	// Simulate a restarted converter: the outbox must call Ensure again rather
	// than having been permanently acknowledged when the first goroutine started.
	converter.calls = 0
	if err := hook(ctx, ref, nil); err == nil || converter.calls != 1 {
		t.Fatal("lost conversion admission not replayed")
	}
	converter.state = mineru.JobStateDone
	if err := hook(ctx, ref, nil); err != nil || afterCalls != 1 || converter.doi != ref.DOI {
		t.Fatalf("completed hook err=%v calls=%d", err, afterCalls)
	}
}
func TestRemoteHookRespectsResolvedVersionAndDisabledConverter(t *testing.T) {
	c := &hookConverter{enabled: true, state: mineru.JobStateDone}
	hook := durableRemoteHooks(c, nil)
	if err := hook(context.Background(), registry.PaperRef{ArxivID: "2401.12345"}, &downloader.FetchOutcome{ArxivCanonical: "2401.12345v2"}); err != nil || c.arxiv != "2401.12345v2" {
		t.Fatal("resolved arxiv version lost")
	}
	c.enabled = false
	c.calls = 0
	if err := hook(context.Background(), registry.PaperRef{DOI: "10.1000/disabled"}, nil); err != nil || c.calls != 0 {
		t.Fatal("explicitly disabled converter should not block hook completion")
	}
}
