package search

import (
	"context"
	"sync"
)

// Provider is one search backend (catalog, arXiv, OpenAlex, ...). Each
// provider answers a SearchEntry with zero or more Hits.
//
// Failure contract: a backend failure (network, parse, quota) MUST be
// reported as (nil, nil) with the error recorded internally (embed
// BaseProvider), never as a non-nil error — one source failing must not
// sink the whole multi-provider search. The engine additionally isolates
// providers that violate the contract (returning an error or panicking),
// but providers should not rely on that.
type Provider interface {
	Name() string
	Search(ctx context.Context, e SearchEntry) ([]Hit, error)
}

// BaseProvider is the shared helper providers embed to satisfy the
// failure contract: RecordFailure stores the backend error for later
// inspection (surfaced in verbose / debug paths) and LastError returns
// the most recent one. Safe for concurrent use.
type BaseProvider struct {
	mu      sync.Mutex
	lastErr error
}

// RecordFailure stores err as the provider's most recent backend failure
// and returns (nil, nil) so the failing Search can simply
// `return p.RecordFailure(err)`.
func (b *BaseProvider) RecordFailure(err error) ([]Hit, error) {
	b.mu.Lock()
	b.lastErr = err
	b.mu.Unlock()
	return nil, nil
}

// LastError returns the most recent backend failure, or nil when the
// last Search succeeded (or none ran yet).
func (b *BaseProvider) LastError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}
