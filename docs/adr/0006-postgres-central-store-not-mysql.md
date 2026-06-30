# PostgreSQL as the central relational store (paper catalog + OpenAlex corpus), not MySQL

The Paper catalog moved off Neo4j into a relational store (`b1e37fa`), and we additionally want an
**OpenAlex works corpus** held locally — instead of hitting the rate-limited public API — so that
citation context, batch analysis, and vector joins against our own arXiv assets all run as plain
SQL. One relational engine serves both. We pick **PostgreSQL** (via `pgx`) and host it as a
**single centralized instance on the mesh**, co-located with RustFS and Neo4j; both edges connect
to it over the mesh, exactly as they already do for RustFS and Neo4j. The host-core `paper_works`
catalog and the OpenAlex corpus live in the **same database as separate tables**, so a deep join
(corpus × asset status × vectors) is one query.

## Why PostgreSQL, not MySQL

The workload sits in the part of the relational space where PostgreSQL is strongest and MySQL
weakest:

- **Raw JSON, only filtered, never modified.** OpenAlex records are stored verbatim as `jsonb`;
  hot fields are exposed through generated columns and GIN / expression indexes **without
  rewriting the record**. PostgreSQL `jsonb` (binary, `@>`/`->>`, partial + expression indexes)
  is far ahead of MySQL's JSON type.
- **Vector join.** Domain-subset embeddings live in the same database via `pgvector`
  (HNSW / IVFFlat). MySQL has no comparable indexed-vector support.
- **Citation + collection queries.** Recursive CTEs, partial unique / queue indexes, row-lock
  leases, `count(*) FILTER (...)`, window functions — all first-class (see
  [`architecture.md` #paperindex](../concepts/architecture.md#paperindex)).

MySQL's genuine advantages — extreme write throughput (InnoDB / MyRocks), thread-per-connection at
high concurrency, Vitess horizontal sharding, a gentler operational learning curve — all target
high-concurrency OLTP serving and sharded scale-out. This store is **internal, read-mostly,
single-instance, never exposed as an outbound API, and explicitly not sharded** (catalog ≈ 10⁵
rows; OpenAlex works ≈ 2.87 × 10⁸ rows / ~1–2 TB, comfortably single-node). None of MySQL's edges
apply, and its sharding trump card is mutually exclusive with the "one database, deep joins across
catalog + corpus + vectors" requirement that motivates the whole design.

## Considered options

- **MySQL** — rejected (above). Its strengths target a profile (sharded, high-concurrency
  serving) we do not have; `jsonb`, `pgvector`, and CTE support are each weaker.
- **No relational store (stay object-store + Neo4j only)** — rejected: object storage answers only
  point `GetObject` / `ListObjects` and cannot do collection queries, counts, partial-unique DOI
  constraints, or atomic MinerU leases (`architecture.md` #paperindex). Neo4j stays, but only as
  the **citation graph** behind the (optional) graph plugin — a rebuildable derived view, not the
  source of truth.

## Consequences

- One central PostgreSQL becomes a shared mesh dependency alongside RustFS and Neo4j. Edges
  **degrade gracefully** when it is unreachable: asset writes still return `201` and set
  `X-Catalog-Sync: deferred`; reads report `availability=false`. (The earlier "PG per edge"
  sketch in `storage-architecture.md` was aspirational — PG joins the other central backends.)
- **Scope (recommended): full metadata, subset vectors.** The full ~2.87 × 10⁸-work metadata
  corpus is stored as raw `jsonb`, so every `referenced_works` id resolves **locally** — citation
  traversal never falls back to the public API. Vectors are built **only on the domain subset**;
  full-corpus embeddings (~3 TB + large embedding compute) are explicitly out of scope.
- The OpenAlex corpus is **only filtered, never modified**: the stored record stays faithful to
  the snapshot; every derived field is a generated column, not a destructive transformation.
- `paper_works` and the OpenAlex corpus are **separate tables in one database** — semantically
  distinct (a 10⁵-row, bucket-rebuildable host-core index vs a 10⁸-row external reference corpus)
  but co-resident so deep joins stay in SQL.
- Sync: the corpus is refreshed from the OpenAlex snapshot (quarterly public cadence) by pulling
  only new `updated_date` partitions.
