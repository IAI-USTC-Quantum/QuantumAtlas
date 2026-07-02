// Package lazyload is a small, generic lazy write-through cache-aside
// orchestrator for QuantumAtlas (ADR 0012).
//
// It captures — as reusable Go, not an imported framework — the pattern that
// SolidCache (Rails), FusionCache (.NET), dogpile.cache (Python), foyer (Rust)
// and JCache (JVM) all converge on: a durable store IS the cache, a miss calls
// a user Loader once (concurrent callers for the same key coalesce onto that
// single call), and the loaded value is written back through the store so the
// next read is local.
//
// The three concerns are kept as independent, composable seams:
//
//   - [Store] — the durable cache tier (a SQL table, an object store, …); never
//     in-process memory, because the durable store already IS the cache.
//   - [Loader] — the read-through "creator"/"factory" run on a miss.
//   - the coalescing — an in-process [singleflight.Group], keyed by the cache
//     key, so a concurrent miss for the same key does exactly one Load→Loader→
//     Store cycle. This is the exact Go equivalent of foyer's InflightManager
//     Lead/Wait, cache-manager's coalesceAsync, and dogpile's mutex-elected
//     creator.
//
// This is the SYNCHRONOUS materialization mode: [Materializer.Get] blocks until
// the value is ready. It is the right shape for cheap loaders (a single HTTP
// GET, one SQL round-trip). Slow/expensive materialization that must not block a
// request — a multi-minute conversion with queryable progress, cooldown on
// failure, and non-blocking polling — uses a job-oriented model instead (see
// internal/mineru.Converter). That is the boundary a bare singleflight cannot
// cross, and ADR 0012 documents why the two live side by side.
//
// Refinements the reference libraries add on top of this core — a cross-process
// claim (a Postgres advisory lock, à la SolidCache's SELECT … FOR UPDATE), a
// transient-vs-real fail-safe split, and refresh-ahead on a soft TTL — are
// deliberately deferred (ADR 0012, staged plan) until a measured need. Get's
// error return already surfaces Loader/Store errors so a fail-safe layer can be
// added without changing this signature.
package lazyload

import (
	"context"
	"errors"

	"golang.org/x/sync/singleflight"
)

// ErrNotFound is returned by a [Loader] to signal that the key genuinely has no
// value (the upstream has nothing either), as opposed to a transient failure.
// [Materializer.Get] maps it to (zero, false, nil): a miss, not an error.
var ErrNotFound = errors.New("lazyload: no value for key")

// Store is the durable cache tier. Load reads a previously-cached value; Store
// writes a freshly-loaded one back (write-through). Implementations back this
// with a SQL table, an object store, etc.
//
// Load must report a cache miss as (zero, false, nil) and reserve a non-nil
// error for genuine read failures (a dead connection, a malformed row). Store is
// called only after a successful [Loader] run, never on a hit.
type Store[V any] interface {
	Load(ctx context.Context, key string) (value V, hit bool, err error)
	Store(ctx context.Context, key string, value V) error
}

// Loader produces a value on a cache miss (the read-through creator/factory).
// Return [ErrNotFound] for a genuine miss (nothing upstream either); return any
// other error for a transient/real failure, which Get propagates unchanged.
type Loader[V any] func(ctx context.Context, key string) (V, error)

// Option configures a [Materializer].
type Option[V any] func(*Materializer[V])

// WithWriteErrorHandler installs a hook invoked when the write-back after a
// successful load fails. The loaded value is still returned to the caller (the
// read resolves even if caching it failed) — the hook is for logging/metrics.
func WithWriteErrorHandler[V any](fn func(key string, err error)) Option[V] {
	return func(m *Materializer[V]) { m.onWriteErr = fn }
}

// Materializer is a lazy, write-through cache-aside orchestrator. See the
// package doc and ADR 0012 for the model and its boundaries.
//
// A Materializer must be long-lived and shared across callers: the singleflight
// group only coalesces executions that overlap in time, so a per-request
// instance would coalesce nothing. It is safe for concurrent use.
type Materializer[V any] struct {
	store      Store[V]
	loader     Loader[V]
	sf         singleflight.Group
	onWriteErr func(key string, err error)
}

// New builds a Materializer over a durable store and a read-through loader.
func New[V any](store Store[V], loader Loader[V], opts ...Option[V]) *Materializer[V] {
	m := &Materializer[V]{store: store, loader: loader}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

type outcome[V any] struct {
	val   V
	found bool
}

// Get returns the value for key, materializing it on a miss:
//
//   - (v, true, nil)     — a hit, or loaded-and-written-back
//   - (zero, false, nil) — a genuine miss (the Loader returned [ErrNotFound])
//   - (zero, false, err) — a real error from Store.Load or the Loader
//
// Concurrent Get calls for the same key share a single execution: exactly one
// runs Load→Loader→Store while the others wait and receive the same result. The
// shared execution is detached from the caller's context so that one waiter's
// cancellation cannot abort the load for the others. Because that also drops the
// caller's deadline, a Store or Loader whose backend can hang MUST impose its own
// timeout — a detached, deadline-less call would otherwise pin the shared
// execution (and its goroutine) indefinitely. This mirrors the
// context.WithoutCancel idiom already used by internal/openalex.Resolver.
func (m *Materializer[V]) Get(ctx context.Context, key string) (V, bool, error) {
	res, err, _ := m.sf.Do(key, func() (any, error) {
		dctx := context.WithoutCancel(ctx)

		if v, hit, lerr := m.store.Load(dctx, key); lerr != nil {
			return outcome[V]{}, lerr
		} else if hit {
			return outcome[V]{val: v, found: true}, nil
		}

		v, lerr := m.loader(dctx, key)
		if errors.Is(lerr, ErrNotFound) {
			return outcome[V]{}, nil // genuine miss — not an error
		}
		if lerr != nil {
			return outcome[V]{}, lerr
		}

		if werr := m.store.Store(dctx, key, v); werr != nil && m.onWriteErr != nil {
			m.onWriteErr(key, werr) // best-effort; the read still resolves
		}
		return outcome[V]{val: v, found: true}, nil
	})
	if err != nil {
		var zero V
		return zero, false, err
	}
	r := res.(outcome[V])
	return r.val, r.found, nil
}
