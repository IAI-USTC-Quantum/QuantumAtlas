package openalexcorpus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
)

// DefaultBatch is the number of works upserted per statement. Because the
// upsert uses unnest(array…) the whole batch is a handful of bind params
// regardless of size, so this can be generous; 1000 keeps per-statement
// memory modest while amortizing round-trips.
const DefaultBatch = 1000

// IngestOptions tunes a part ingest.
type IngestOptions struct {
	// BatchSize is the unnest batch size for the upsert statements.
	BatchSize int
}

// PartReport summarizes one part ingest.
type PartReport struct {
	// Works is the number of records upserted.
	Works int
	// Citations is the number of out-edges (referenced_works entries) seen
	// in this part. The edges are materialized automatically by the
	// openalex_referenced_work_ids generated column (ADR 0010); this is an
	// in-memory progress counter, not a separate write.
	Citations int
}

// IngestPart streams one gzip JSONL works part from store and upserts its
// records into openalex_works. Citation out-edges need no separate write:
// they are derived from each record by the openalex_referenced_work_ids
// generated column (ADR 0010). It flushes every BatchSize records so peak
// memory stays ~one batch, not the whole (hundreds-of-MB) part.
//
// Idempotent: re-running a part re-upserts the same rows (ON CONFLICT DO
// UPDATE), so an interrupted operator run is safe to resume.
func (s *Store) IngestPart(ctx context.Context, store objstore.Store, key string, opts IngestOptions) (PartReport, error) {
	var rep PartReport
	if !s.ensure(ctx) {
		return rep, ErrCorpusUnavailable
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = DefaultBatch
	}
	updatedDate := PartitionDate(key)

	buf := make([]RawWork, 0, batch)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		n, err := s.UpsertWorks(ctx, buf, updatedDate)
		if err != nil {
			return err
		}
		rep.Works += n
		for _, w := range buf {
			rep.Citations += len(w.Meta.ReferencedWorks)
		}
		buf = buf[:0]
		return nil
	}

	if err := StreamRawWorks(ctx, store, key, func(rw RawWork) error {
		if rw.OpenAlexID() == "" {
			return nil // skip records without an id (cannot key the row)
		}
		buf = append(buf, rw)
		if len(buf) >= batch {
			return flush()
		}
		return nil
	}); err != nil {
		return rep, err
	}
	if err := flush(); err != nil {
		return rep, err
	}
	return rep, nil
}

// UpsertWorks upserts a single batch of works into openalex_works. All
// rows take updatedDate (the part's partition date, "" → NULL). The record
// is stored verbatim as jsonb; the generated columns derive hot fields
// from it server-side. Returns the number of rows in the batch.
func (s *Store) UpsertWorks(ctx context.Context, works []RawWork, updatedDate string) (int, error) {
	if !s.ensure(ctx) {
		return 0, ErrCorpusUnavailable
	}
	if len(works) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(works))
	records := make([]string, 0, len(works))
	arxivIDs := make([]*string, 0, len(works))
	for _, w := range works {
		id := w.OpenAlexID()
		if id == "" {
			continue
		}
		ids = append(ids, id)
		records = append(records, string(w.Record))
		arxivIDs = append(arxivIDs, pgText(w.ArxivID()))
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// text[]::jsonb[] cast is intentional: pgx encodes []string as text[],
	// and the array cast converts element-wise to jsonb (verified against
	// the live backend). updated_date is one literal for the whole part.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO openalex_works (openalex_id, record, arxiv_id, updated_date)
		SELECT u.id, u.rec, u.arx, $4::date
		FROM unnest($1::text[], $2::text[]::jsonb[], $3::text[]) AS u(id, rec, arx)
		ON CONFLICT (openalex_id) DO UPDATE SET
			record = EXCLUDED.record,
			arxiv_id = EXCLUDED.arxiv_id,
			updated_date = EXCLUDED.updated_date,
			ingested_at = now()`,
		ids, records, arxivIDs, pgText(updatedDate))
	if err != nil {
		return 0, fmt.Errorf("openalexcorpus: upsert works: %w", err)
	}
	return len(ids), nil
}

// UpsertFetchedWork stores a single work fetched live from the OpenAlex API
// (lazy fetch-on-miss write-through, ADR 0006): the local corpus is a cache,
// and a by-id miss can be filled from the public API and written back so the
// next read hits locally. raw is the verbatim json record; meta is its
// decoded projection (for the openalex_id + arxiv_id derivation). Idempotent
// (ON CONFLICT DO UPDATE). updatedDate is left empty (a live fetch is not a
// snapshot partition). Returns ErrCorpusUnavailable when PostgreSQL is down.
func (s *Store) UpsertFetchedWork(ctx context.Context, raw json.RawMessage, meta openalex.Work) error {
	if !s.ensure(ctx) {
		return ErrCorpusUnavailable
	}
	if shortID(meta.ID) == "" {
		return fmt.Errorf("openalexcorpus: fetched work has no id")
	}
	_, err := s.UpsertWorks(ctx, []RawWork{{Record: raw, Meta: meta}}, "")
	return err
}

// ListPartKeys returns the object keys of every works part under prefix in
// the snapshot bucket. Thin re-export of the openalex helper so callers
// driving a PG ingest don't import two packages for one list.
func ListPartKeys(ctx context.Context, store objstore.Store, prefix string) ([]string, error) {
	infos, err := store.ListPrefix(ctx, prefix, 0)
	if err != nil {
		return nil, fmt.Errorf("openalexcorpus: list %s: %w", prefix, err)
	}
	keys := make([]string, 0, len(infos))
	for _, o := range infos {
		keys = append(keys, o.Key)
	}
	return keys, nil
}

// pgText maps "" to a nil *string so it encodes as SQL NULL (used for
// nullable text/date columns: arxiv_id, updated_date).
func pgText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
