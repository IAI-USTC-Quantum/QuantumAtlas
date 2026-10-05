package main

import (
	"context"
	"errors"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type hookConverter struct{ calls int }

func (c *hookConverter) Enabled() bool { return true }
func (c *hookConverter) Ensure(context.Context, string) *mineru.Job {
	c.calls++
	return &mineru.Job{State: mineru.JobStateQueued}
}
func (c *hookConverter) EnsureByDOI(context.Context, string, string) *mineru.Job {
	c.calls++
	return &mineru.Job{State: mineru.JobStateQueued}
}

func TestPDFArchiveHooksNeverStartContentParsing(t *testing.T) {
	for _, ref := range []registry.PaperRef{{ArxivID: "2401.12345v2"}, {DOI: "10.1000/archive"}} {
		converter := &hookConverter{}
		called := 0
		hook := durableRemoteHooks(converter, func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { called++; return nil })
		if err := hook(t.Context(), ref, nil); err != nil {
			t.Fatal(err)
		}
		if called != 1 || converter.calls != 0 {
			t.Fatalf("archive=%d inference=%d", called, converter.calls)
		}
		if err := hook(t.Context(), ref, nil); err != nil {
			t.Fatal(err)
		}
		if converter.calls != 0 {
			t.Fatal("outbox replay started inference")
		}
	}
}
func TestPDFArchiveHookPreservesAfterFailure(t *testing.T) {
	want := errors.New("index unavailable")
	hook := durableRemoteHooks(nil, func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { return want })
	if !errors.Is(hook(t.Context(), registry.PaperRef{}, nil), want) {
		t.Fatal("archive hook lost durable callback failure")
	}
	if err := durableRemoteHooks(nil, nil)(t.Context(), registry.PaperRef{}, nil); err != nil {
		t.Fatal(err)
	}
}
