# OpenAlex works schema: inline `referenced_works`, reverse-lookup cited-by, sync-state watermark, append-only audit

_Refines the physical schema of the OpenAlex works store from ADR `0006`; the "full metadata held
locally, only filtered never modified" decision there is unchanged._

ADR `0006` shipped citation edges as a separate `work_referenced(work_id, referenced_id)` table and
sketched a stored cited-by notion. Working the corpus (grill Q8–Q16) showed both are avoidable: the
raw `record` already contains `referenced_works`, and PostgreSQL's `jsonb` + GIN can serve *both*
citation directions off one generated column with **zero maintenance**. We also add a refresh
watermark and an audit surface that ADR `0006` left implicit.

## Decision

**Citations inline, no edge table (Q8).**

- `openalex_referenced_work_ids jsonb GENERATED ALWAYS AS (strip_openalex_prefix(record->'referenced_works')) STORED`
  — bare `W…` ids. A generated column cannot subquery/aggregate, so the prefix strip is packed into
  an **`IMMUTABLE` plpgsql `strip_openalex_prefix()`** function (Q12). Build a **GIN** index on it.
- **Drop `work_referenced`.** Forward ("what does W cite") is the array itself; reverse ("who cites
  W") is `WHERE openalex_referenced_work_ids ? 'W…'` — the local equivalent of OpenAlex's official
  `cites:` filter, always fresh, no backfill, no dangling-edge bookkeeping.
- **Drop the stored `cited_by_work_ids`** (Q8): it is the same reverse-lookup above, so storing it
  only invites drift.

**Refresh watermark, not per-row bookkeeping (Q11).**

- `openalex_sync_state` — a **single-row** table (`id boolean PRIMARY KEY DEFAULT true CHECK (id)`,
  `last_updated_date date`, `last_synced_at timestamptz`, `snapshot_version text`) drives the
  incremental pull. We do **not** add per-row `arxiv_id` / `updated_date` / `publication_year`
  backfill columns to `openalex_works`.

**Constrained values are `text + CHECK`, never PG native `enum` (Q10)** — additive value changes
are a one-line `CHECK` edit, not an `ALTER TYPE`.

**Integrity split by cost/history (Q9/Q14/Q15/Q16).**

- **Structural checks are VIEWS** — local, computed, zero storage: (1) `paper_assets` md/json
  anomalies; (2) `papers.paper_openalex_id` NULL gaps (a paper that should map to a corpus work but
  doesn't). No HTTP, so views can express these entirely.
- **API-comparison audit is a persistent, append-only single table `openalex_audit`** (needs
  history / trend / unresolved-over-time):
  `id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, run_id bigint, openalex_id text, field text,
  verdict text CHECK (verdict IN ('ok','field_mismatch','cited_by_regressed','stale_drift')),
  local_value jsonb, remote_value jsonb, checked_at timestamptz DEFAULT now()`, indexed on
  `(run_id)` and `(openalex_id, field)`. **Every sampled row is recorded, including `ok`.**
  `sampled_count` / coverage / "unresolved" are all **derived**, never stored or mutated —
  *unresolved* = the latest run per `(openalex_id, field)` whose `verdict ≠ ok` (Q16).
- **Audit rules (Q9):** stable fields (`referenced_works` / `doi` / `year` / `type`) compare
  **exactly**; `cited_by_count` is **monotonic-tolerant** (report only when remote < snapshot, or
  drift exceeds a threshold — cited-by only grows). Comparison needs an **external script** that
  fetches the OpenAlex API; a SQL view cannot make HTTP calls.

## Why inline `jsonb` + GIN beats an edge table

- **The record is already the source.** `referenced_works` is in every OpenAlex record; a generated
  column re-derives it deterministically — it can never drift from the record, and ingesting one
  work does not require a separate edge-extraction pass or a full re-run.
- **One index, both directions.** A GIN inverted index answers forward (array read) and reverse
  (`? 'W…'`) queries; the edge table needed a PK *and* a second index to do the same, plus
  insert/delete maintenance and dangling-edge tolerance.
- **Cited-by that can't go stale.** A reverse-lookup is computed at read time from the same records,
  so it is always consistent with what the corpus currently holds — a stored list is a cache that
  must be invalidated on every ingest.

## Considered options

- **Keep `work_referenced` (ADR `0006` as shipped).** Rejected (Q8): a GIN reverse-lookup already
  serves both directions with no maintenance; the edge table adds writes, a second index, and
  dangling-edge handling for no query it uniquely enables.
- **Store `cited_by_work_ids` per row.** Rejected: a derivable cache that drifts on every ingest.
- **PostgreSQL native `enum` for constrained columns.** Rejected (Q10): `text + CHECK` evolves with
  a one-line edit; `ALTER TYPE … ADD VALUE` is transaction-hostile and hard to reverse.
- **A mutable audit summary row (running counters / "unresolved" flag).** Rejected (Q16):
  append-only sampled rows + derived aggregates give history, trend, and reproducible "unresolved",
  and never lie about the past.
- **Per-row `updated_date` on every work.** Rejected (Q11): the single-row `openalex_sync_state`
  watermark drives incremental refresh without ~10⁸ redundant timestamps.

## Consequences

- **ADR `0006` mechanism refined.** Its `work_referenced` edge table is superseded by the inline
  generated column + GIN; the "citation resolution is always local, never a public-API fallback"
  guarantee is **unchanged** (now served by the array + reverse-lookup).
- **Breaking migration (Phase B).** `internal/openalexcorpus/{schema,ingest,query,record,store}.go`
  and `cmd/qatlasd/openalex_cmd.go` drop `work_referenced`, add the generated column + GIN + the
  `strip_openalex_prefix()` function, `openalex_sync_state`, and `openalex_audit`; cited-by queries
  become `?`-reverse-lookups. Tests move with them.
- **New audit script + views.** An operator-run sampler fetches OpenAlex and writes `openalex_audit`
  rows; the structural views ship with the schema. The read-only SQL contract (ADR `0007`) drops
  `work_referenced` from its readable subset.
