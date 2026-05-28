// Package safego provides a single helper, Go, that runs a function in
// a new goroutine with a panic recovery wrapper.
//
// # Why this exists
//
// A panic in a vanilla `go fn()` goroutine crashes the entire process —
// no deferred recover() in the parent stack can catch it, because
// recover only sees panics from the goroutine it runs on. Long-running
// QuantumAtlas servers fire-and-forget several side-effect goroutines
// (PAT last_used_at bump, health probes, mineru claim release, etc.),
// so any preventable panic in those paths brings down a production
// edge node — and worse, a panicking PAT MarkUsed brings down every
// in-flight authenticated request along with it.
//
// # Behavior
//
// Go(name, fn) starts fn in a new goroutine wrapped with `defer
// recover()`. If fn panics, we:
//
//   - log slog.Error with the supplied name, panic value, and full
//     debug.Stack() output for forensic analysis
//   - increment the package-level panic counter (exposed via
//     PanicCount() for tests / future /metrics endpoint)
//   - return — letting the offending goroutine die quietly instead of
//     taking the process with it
//
// fn itself is run unchanged; safego adds no timeouts, contexts, or
// rate-limits. Callers that need those should add them in fn.
//
// # When NOT to use this
//
// Goroutines whose result the caller waits on (sync.WaitGroup, channel
// recv) should NOT use safego — the panic recovery would silently let
// the parent block forever waiting for a goroutine that died. For those
// patterns, use a vanilla `go fn()` and let `defer` chains handle
// errors explicitly.
package safego

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
)

// panicCount is the process-lifetime count of panics recovered by Go.
// Atomic for lock-free reads from PanicCount(); only ever incremented
// inside the recover branch, never reset.
var panicCount atomic.Uint64

// Go starts fn in a new goroutine with panic recovery. The name
// argument is a short human-readable label used in the recovery log
// line — should describe what the goroutine does ("pat.MarkUsed",
// "healthz.probeNeo4j", etc.) so an operator can grep for it.
//
// fn MUST NOT be nil; we panic synchronously (in the caller's
// goroutine, where the bug is visible) rather than swallow the bug in
// a quiet background frame.
func Go(name string, fn func()) {
	if fn == nil {
		panic(fmt.Sprintf("safego.Go(%q): nil fn", name))
	}
	go func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			panicCount.Add(1)
			// Capture the stack trace from inside the deferred
			// recover so it points at the panic site, not the Go()
			// call site. debug.Stack() includes the runtime.gopanic
			// frame which makes the source line obvious.
			slog.Error("safego: goroutine panic recovered",
				"goroutine", name,
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}()
		fn()
	}()
}

// PanicCount returns the number of panics safego has recovered since
// the process started. Intended for tests and a future Prometheus
// counter; not for handler logic.
func PanicCount() uint64 {
	return panicCount.Load()
}

// LogPanic is for goroutines that the caller awaits with sync.WaitGroup
// or a channel — those CANNOT use Go() because the surrounding defer
// (e.g. wg.Done) needs to run after the recover. Pattern:
//
//	wg.Add(1)
//	go func() {
//	    defer wg.Done()
//	    defer func() {
//	        if r := recover(); r != nil {
//	            safego.LogPanic("my-probe", r)
//	        }
//	    }()
//	    realWork()
//	}()
//
// LogPanic records the panic with the same structured log format Go()
// uses, and bumps the same panicCount, so panics from waited
// goroutines surface in observability the same way as fire-and-forget
// ones.
func LogPanic(name string, r any) {
	panicCount.Add(1)
	slog.Error("safego: goroutine panic recovered",
		"goroutine", name,
		"panic", fmt.Sprintf("%v", r),
		"stack", string(debug.Stack()),
	)
}
