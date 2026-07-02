package routes

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/lazyload"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalexcorpus"
)

// corpusOpTimeout bounds a single corpus read/write. The Materializer detaches
// the shared execution from the request deadline (to keep one caller's
// cancellation from poisoning coalesced waiters), so — unlike the OpenAlex fetch,
// which is already bounded by the resolver's HTTP timeout — the corpus DB calls
// must impose their own bound here. Otherwise a hung/slow corpus query would pin
// a handler goroutine + a pooled connection indefinitely, immune to client
// disconnect (ADR 0012). Generous: corpus reads are indexed and the write-back is
// a single upsert, so this only trips on a genuinely stuck backend.
const corpusOpTimeout = 30 * time.Second

// corpusValue is the value materialized by the OpenAlex-corpus lazy loader. It
// carries both the verbatim record (Raw — what the corpus persists) and the
// parsed Work (what lookup callers read). On a corpus hit Raw is empty: the
// value is already stored, so there is nothing to write back.
type corpusValue struct {
	Raw  json.RawMessage
	Work openalex.Work
}

// corpusStore adapts the OpenAlex corpus to lazyload.Store. Load resolves a
// namespaced `kind:id` ref (openalex/doi/arxiv) against the local corpus; Store
// writes a freshly-fetched record back (write-through). Both mirror the
// pre-existing corpusResolve / UpsertFetchedWork behaviour exactly — a corpus
// read failure is reported as a miss (never an error), matching the batch
// lookup's "one bad ref never fails the batch" contract.
type corpusStore struct {
	corpus *openalexcorpus.Store
}

func (s corpusStore) Load(ctx context.Context, ref string) (corpusValue, bool, error) {
	kind, id, ok := splitRef(ref)
	if !ok {
		return corpusValue{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, corpusOpTimeout)
	defer cancel()
	work, found := corpusResolve(ctx, s.corpus, kind, id)
	if !found {
		return corpusValue{}, false, nil
	}
	return corpusValue{Work: work}, true, nil
}

func (s corpusStore) Store(ctx context.Context, ref string, v corpusValue) error {
	// Only freshly-fetched values (which carry Raw) are written back; a corpus
	// hit has no Raw and never reaches here (Materializer only stores after a
	// successful loader run). The guard keeps Store defensive regardless.
	if len(v.Raw) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, corpusOpTimeout)
	defer cancel()
	return s.corpus.UpsertFetchedWork(ctx, v.Raw, v.Work)
}

// newCorpusMaterializer builds the long-lived lazy write-through cache-aside
// orchestrator for the OpenAlex corpus (ADR 0012). One instance is shared across
// requests so its singleflight coalesces concurrent misses for the same ref into
// a single corpus read + OpenAlex fetch + write-back.
//
// The loader fetches a single work live from OpenAlex on a corpus miss
// (openalex/doi refs only — arxiv is not a /works/{id} key). It reports
// lazyload.ErrNotFound for a genuine miss (resolver unconfigured, non-fetchable
// kind, or OpenAlex 404), so Get degrades to a pure corpus lookup with no
// upstream call — the same behaviour as before this refactor.
func newCorpusMaterializer(corpus *openalexcorpus.Store, resolver *openalex.Resolver) *lazyload.Materializer[corpusValue] {
	loader := func(ctx context.Context, ref string) (corpusValue, error) {
		// No resolver / not configured → the corpus is read-only; a miss stays a
		// miss with no upstream call (mirrors the old lazyFetch guard).
		if resolver == nil || !resolver.Enabled() {
			return corpusValue{}, lazyload.ErrNotFound
		}
		kind, id, ok := splitRef(ref)
		if !ok || (kind != "openalex" && kind != "doi") {
			// arxiv (and any other kind) is not a /works/{id} key — corpus-only.
			return corpusValue{}, lazyload.ErrNotFound
		}
		raw, work, err := resolver.FetchWorkRecord(ctx, kind, id)
		if err != nil {
			// A genuine "OpenAlex doesn't have it" (404, no mailto, malformed) is
			// a miss, not an error. Transient upstream errors (ErrUpstream) are
			// surfaced so a future fail-safe layer (ADR 0012 stage 2) can classify
			// them; the lookup handler treats any residual error as "unresolved",
			// preserving today's "a miss/error never fails the batch" contract.
			if errors.Is(err, openalex.ErrDOINotFound) ||
				errors.Is(err, openalex.ErrNotConfigured) ||
				errors.Is(err, openalex.ErrInvalidDOI) {
				return corpusValue{}, lazyload.ErrNotFound
			}
			return corpusValue{}, err
		}
		return corpusValue{Raw: raw, Work: work}, nil
	}

	return lazyload.New[corpusValue](
		corpusStore{corpus: corpus},
		loader,
		lazyload.WithWriteErrorHandler[corpusValue](func(ref string, err error) {
			slog.Warn("lookup: lazy corpus write-back failed", "ref", ref, "error", err)
		}),
	)
}
