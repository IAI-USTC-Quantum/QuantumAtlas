# Paper catalog physical redesign: surrogate `papers` + child `paper_assets`, generated `paper_ref`

_Refines the physical shape of the `paper_works` catalog from ADR `0006`; the "PostgreSQL, one
central store" decision there is unchanged._

The paper catalog started as a single `paper_works` table keyed on `arxiv_id text PRIMARY KEY`,
with an `identifier_scheme` discriminator, a synthetic `"doi:<doi>"` primary key for DOI-only
contributions, and **all asset state inlined** (`has_pdf` / `pdf_path` / `md_path` / `image_count`
/ …). That shape conflates three separable things — a paper's *identity*, a specific *external id*,
and a single *asset* — and cannot represent the real OpenAlex world where one work has **several
PDFs** (arXiv v1/v2/… plus the published version). We split it into a surrogate-keyed **`papers`**
catalog and a child **`paper_assets`** table (grill Q1–Q21).

## Decision

**`papers`** — one row per work, identity decoupled from any external id:

- `paper_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` — surrogate key. PostgreSQL has no
  `AUTO_INCREMENT`/`UNSIGNED`; `IDENTITY` is the idiom (Q1). **qatlas-lean references a QA paper by
  this numeric `paper_id`** (the paper-of-record pointer), *not* a `kind:id` string — distinct from
  a Claim's bibliographic `references`, which stay `kind:id` (ADR `0007`).
- Three nullable external-id columns, **each `UNIQUE`**: `paper_arxiv_id` / `paper_doi` /
  `paper_openalex_id`. The **column name is the scheme**, so there is no `scheme` column and no
  child id table; three independent `UNIQUE`s already answer both "find the paper for this id" and
  "get this paper's id of kind X" (Q2). PostgreSQL `UNIQUE` permits multiple `NULL`s, so **do not**
  use `NULLS NOT DISTINCT`. `paper_arxiv_id` stores the **base, unversioned** id (`2208.06941`),
  extracted from the OpenAlex arXiv location URL, never `ids.arxiv` (Q17).
- `paper_openalex_id` carries a **nullable FK → `openalex_works(openalex_id) ON DELETE SET NULL`**
  (Q14): "has an openalex_id ⟺ that work is in the corpus", so the FK never blocks a write; if the
  work is later dropped from the snapshot the column goes `NULL` and awaits backfill.
- `paper_ref text GENERATED ALWAYS AS (…) STORED` — the canonical `kind:id`, priority
  **openalex > arxiv > doi** (Q2).
- `paper_default_asset_id bigint REFERENCES paper_assets(asset_id)` — the asset a bare
  by-paper read resolves to (Q19), maintained by trigger (policy below).
- `CHECK (paper_arxiv_id IS NOT NULL OR paper_doi IS NOT NULL OR paper_openalex_id IS NOT NULL)`.
- Columns keep the `paper_` prefix (Q3); the table is renamed `paper_works` → `papers` (Q4, a
  rename migration). No asset columns live here — they all move to `paper_assets` (Q18).

**`paper_assets`** — one PDF per row, because a work can have many (Q18):

- `asset_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY`;
  `paper_id bigint NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE`.
- `source text NOT NULL CHECK (source IN ('arxiv','published'))`;
  `arxiv_version int` — non-null for `arxiv`, `NULL` for `published` (Q20).
- `pdf_path text NOT NULL` — an **object-store key, not a URL** (Q5); `mineru_md_path` /
  `mineru_json_path` are keys too. Asset presence is **derived** from `*_path IS NOT NULL` (Q6):
  no `has_pdf`/`has_md` booleans.
- Constraints (Q7/Q20): `CHECK ((mineru_md_path IS NULL) = (mineru_json_path IS NULL))` (MinerU
  emits both or neither); the source/version CHECK above; `UNIQUE (paper_id, source,
  arxiv_version)`; a **partial** `UNIQUE (paper_id) WHERE source='published'` (one published PDF
  per paper, sidestepping the `NULL`-version uniqueness gap); and an explicit
  `INDEX (paper_id)` — PostgreSQL does **not** auto-index an FK column, and "all assets of a paper"
  would otherwise seq-scan.
- **Default-asset policy (Q21):** `paper_default_asset_id` points at the **published** asset if one
  exists, else the **latest** `arxiv` (max `arxiv_version`). A trigger recomputes it on
  `paper_assets` insert/delete.

## Why a surrogate key + a child table, not the arxiv-PK single table

- **A work is not one id and not one PDF.** The old `arxiv_id` PK forced a paper to *be* its arXiv
  id and hid the DOI-only case behind a synthetic `"doi:<doi>"` key — a string hack that leaks the
  scheme into the primary key. A surrogate `paper_id` lets the same row own an arXiv id, a DOI, and
  an OpenAlex id simultaneously, each independently unique.
- **Multiple PDFs are the norm.** arXiv v1/v2 plus a published version are three distinct byte
  sets with distinct MinerU outputs; inlining a single `pdf_path`/`md_path` on the paper cannot
  hold them. The child table makes each a first-class row with its own version and conversion state.
- **Stable identity for cross-repo references.** A numeric `paper_id` is a compact, scheme-free
  handle qatlas-lean can store without coupling to which external id a paper happens to have.

## Considered options

- **Keep `arxiv_id` PK + `identifier_scheme` discriminator (status quo).** Rejected: cannot model
  multi-asset works; the synthetic `"doi:"` PK is a hack; identity is welded to one external id.
- **Inline the asset columns on `papers`.** Rejected (Q18): one work → many PDFs; a single set of
  `pdf_path`/`md_path` columns can hold only one.
- **A child table of external ids (`paper_ids(paper_id, scheme, value)`).** Rejected (Q2): three
  `UNIQUE` columns with the scheme *as the column name* are simpler, index cleanly, and there are
  exactly three schemes — a generic key/value child adds joins for no gain.

## Consequences

- **Breaking migration + data move (Phase A).** `paper_works` → `papers` rename; backfill
  `paper_assets` from the old inline columns; populate the three external-id columns from
  `arxiv_id` / `identifier_scheme` / `doi` / `openalex_id`; drop the `"doi:<doi>"` synthetic key.
  Touches `internal/papers/{schema,store,doi_store,sync,lookup,claims,ids}.go`,
  `internal/routes/papers.go` + `papers_lookup.go`, and their tests. Sequenced in
  `docs/adr`-adjacent plan; not a one-shot.
- **ADR `0007` refined.** Its "paper-of-record `paper.id`, which QA's asset pipeline versions" is
  now precise: the paper-of-record is the **surrogate `papers.paper_id`** (never versioned); PDF
  **versions live per-asset** in `paper_assets.arxiv_version`. The read-only SQL contract's table
  list becomes `papers` + `paper_assets` (+ `openalex_works`).
- **Read path (ADR `0011`).** By-id resolution walks `papers` (three UNIQUE columns) →
  `paper_default_asset_id` → `paper_assets.mineru_md_path`.
