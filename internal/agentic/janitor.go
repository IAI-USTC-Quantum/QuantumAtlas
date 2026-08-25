package agentic

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// JanitorInterval is how often the background janitor sweeps expired
// sandboxes. Mirrors the registry janitor's cadence; sweeps are
// idempotent so duplicate runs are harmless.
const JanitorInterval = 1 * time.Minute

// RunJanitor blocks until ctx is cancelled, sweeping sandboxes older than
// retention every JanitorInterval. One sweep runs immediately at startup
// to catch leftovers from a crash. Intended to run in its own goroutine.
// A non-positive retention disables sweeping entirely (never wipe the
// whole root on a misconfiguration).
func RunJanitor(ctx context.Context, root string, retention time.Duration) {
	if retention <= 0 {
		slog.Warn("agentic janitor: non-positive retention; sweeping disabled", "retention", retention)
		return
	}
	SweepOnce(root, retention)
	ticker := time.NewTicker(JanitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			SweepOnce(root, retention)
		}
	}
}

// SweepOnce removes every sandbox directory under root whose modtime is
// older than retention and returns the number removed. Errors are logged,
// never fatal — a transient filesystem hiccup just means the next tick
// retries.
func SweepOnce(root string, retention time.Duration) int {
	if retention <= 0 {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("agentic janitor: read sandbox root failed", "root", root, "error", err)
		}
		return 0
	}
	cutoff := time.Now().Add(-retention)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("agentic janitor: sweep failed", "dir", dir, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		slog.Info("agentic janitor: expired sandboxes cleared", "count", removed, "root", root)
	}
	return removed
}
