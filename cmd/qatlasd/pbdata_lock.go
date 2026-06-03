// Single-process lock on PBDataDir using a flock(2) on a sentinel file.
//
// PocketBase + SQLite do NOT serialize multiple writer processes safely:
// SQLite in WAL mode allows multi-reader/single-writer, but two PocketBase
// processes opening the same pb_data will race on `_collections`,
// `_externalAuths`, and PocketBase's own boot-time migration ladder. The
// common failure mode is silent data corruption: each process boots
// "successfully", they take turns writing, and at some point one process
// rolls back a transaction the other already saw committed.
//
// PocketBase itself ships no pid lock — `apis.Serve` opens the SQLite
// connection and trusts the operator to run a single process. We add the
// missing layer here.
//
// The lock is an OS-level **advisory** flock(2) on the file
// <pb_data>/qatlasd.lock. Kernel releases it automatically on process
// exit (graceful, SIGTERM, kill -9, OOM), so a crashed qatlasd never
// requires manual cleanup — unlike a hand-rolled pid file + pidAlive()
// check, which leaks state after `kill -9`.
//
// We use github.com/gofrs/flock (≈ 8k stars, used by terraform, k0s,
// gitea, ...) — cross-platform (Linux/macOS/BSD/Windows) and a thin
// wrapper around the platform-native primitive.
//
// Disable knob: QATLAS_SKIP_PB_DATA_LOCK=1 bypasses the check. Reserved
// for emergency recovery / multi-process diagnostic experiments — never
// for production.

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// pbDataLockFilename is the sentinel file flock(2) targets. Lives inside
// PBDataDir so it ships with the data — if you copy pb_data to a new
// machine you also copy the lock affordance.
const pbDataLockFilename = "qatlasd.lock"

// acquirePBDataLock takes an exclusive non-blocking flock on a sentinel
// file inside pbDataDir. Returns the locked *flock.Flock the caller must
// keep alive for the lifetime of the process (call Unlock on shutdown,
// though kernel release on exit is the more important guarantee).
//
// Returns a typed sentinel error when the lock is already held by another
// process so callers can distinguish "configuration problem" from
// "another instance is running" in their fatal message.
func acquirePBDataLock(pbDataDir string) (*flock.Flock, error) {
	if pbDataDir == "" {
		return nil, errors.New("acquirePBDataLock: empty pb_data dir")
	}
	// Tolerate the case where pb_data hasn't been created yet (fresh
	// install): make the parent so flock has somewhere to live.
	if err := os.MkdirAll(pbDataDir, 0o700); err != nil {
		return nil, fmt.Errorf("acquirePBDataLock: mkdir %s: %w", pbDataDir, err)
	}

	lockPath := filepath.Join(pbDataDir, pbDataLockFilename)
	fl := flock.New(lockPath)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquirePBDataLock: flock %s: %w", lockPath, err)
	}
	if !locked {
		return nil, &pbDataLockedError{path: lockPath, dir: pbDataDir}
	}
	slog.Info("pb_data lock acquired",
		"path", lockPath,
		"pid", os.Getpid(),
	)
	return fl, nil
}

// pbDataLockedError signals that another qatlasd process is currently
// holding the lock on the given pb_data. We use a typed error so the
// fatal message can be unambiguous ("another instance is running" vs
// "lock file permission denied").
type pbDataLockedError struct {
	path string
	dir  string
}

func (e *pbDataLockedError) Error() string {
	return fmt.Sprintf(
		"pb_data at %s is already locked by another qatlasd process "+
			"(lock file: %s). PocketBase + SQLite do not serialize "+
			"multiple writers safely — refusing to start. To run a "+
			"second qatlasd alongside, point QATLAS_PB_DATA_DIR at a "+
			"different directory. To bypass for emergency recovery, "+
			"set QATLAS_SKIP_PB_DATA_LOCK=1 (NOT for production).",
		e.dir, e.path,
	)
}

// pbDataLockSkipRequested reports whether the operator opted into the
// "skip the safety check" escape hatch. Recognises the same shape as
// other boolean env vars in this codebase: 1/true/yes/on/y/t.
func pbDataLockSkipRequested() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("QATLAS_SKIP_PB_DATA_LOCK")))
	switch v {
	case "1", "true", "yes", "on", "y", "t":
		return true
	}
	return false
}
