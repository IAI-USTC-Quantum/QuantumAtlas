# Upload API & sha256-aware idempotency

> Users uploading PDFs / markdown to QuantumAtlas server, via the
> `qatlas contrib …` CLI or direct HTTP calls. Covers the request
> shape, response shape, status code contract, sha256 deduplication
> semantics, conflict handling, and `?expected_sha256=` in-transit
> guard.
>
> Storage / ops perspective (RustFS env vars, IAM policy, bucket
> versioning lifecycle, `qatlasd storage prune` operator guide)
> lives in [storage-rustfs.md](../deployment/rustfs.md).
>
> Paper metadata (title / authors / abstract / DOI / citations) is
> sourced upstream from OpenAlex into the Neo4j catalog as of
> v0.7.0; the upload endpoint no longer accepts a metadata JSON
> sidecar.

## Endpoints

| Method  | Path                                          | Scope          |
| ------- | --------------------------------------------- | -------------- |
| `POST`  | `/api/papers/{arxiv_id}/upload-pdf`           | `papers:write` |
| `POST`  | `/api/papers/{arxiv_id}/upload-mineru`        | `papers:write` |

Both routes require auth: either a browser session token or a PAT
(`Authorization: Bearer qat_…`) whose scopes include `papers:write`.
See [contribute-content.md](../guides/contribute-content.md) for how to
mint a PAT.

`{arxiv_id}` MUST include the explicit `vN` version suffix. Both
schemes are accepted:

- new style: `2501.00010v1` (post April 2007)
- old style with category prefix: `quant-ph/9508027v1` (pre April 2007)

The server rejects bare ids without `vN` to keep on-disk paths and
listings deterministic.

> **Since v0.21**: the `{arxiv_id}` slot ALSO accepts a **DOI**
> (`10.<registrant>/<suffix>`) so contributors can upload a *published*
> version that may have no arXiv preprint. See
> [Contributing by DOI](#contributing-by-doi) below.

## CLI quick start

```bash
# Upload a fresh PDF
qatlas contrib pdf 2501.00010v1 --pdf ./paper.pdf

# Re-upload the same bytes → 200 OK unchanged (no S3 write)
qatlas contrib pdf 2501.00010v1 --pdf ./paper.pdf

# Re-upload DIFFERENT bytes → 409 Conflict (refuses to overwrite)
qatlas contrib pdf 2501.00010v1 --pdf ./paper-v2.pdf

# Force overwrite (old version preserved by bucket versioning,
# recoverable until the next storage prune)
qatlas contrib pdf 2501.00010v1 --pdf ./paper-v2.pdf --overwrite

# Parse a paper's PDF with your own MinerU quota and push the full
# bundle (full.md + images/*) back to the server in one shot.
qatlas contrib mineru 2501.00010v1

# Upload a pre-made MinerU result bundle for a DOI-only published paper
# (full.md + images/*) — push the raw zip MinerU returned at its
# `full_zip_url`; the server unzips it. This direct-zip form is DOI-only
# (arXiv papers use the runner above). Title and authors are auto-fetched
# from OpenAlex — the contributor never supplies them.
qatlas contrib mineru 10.1103/PhysRevLett.123.070501 \
    --zip ./mineru-result.zip \
    --source mineru-client-v0.8 \
    --verify warn
```

The CLI streams the file once through sha256 before posting and
attaches `?expected_sha256=<hex>` automatically. So in-transit byte
corruption is caught by the server BEFORE any object-store write —
the client doesn't need to do anything extra.

## sha256-aware idempotency (the core behavior change)

Every upload stages the body, computes sha256, then decides what to
do based on what already exists at the target key:

| What's at the target key                            | Flag passed?    | What the server does                                                                                                                            | HTTP |
| --------------------------------------------------- | --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- | ---- |
| Nothing                                             | (any)           | Write new object, attach `sha256` user metadata                                                                                                 | 201  |
| Same bytes (existing `x-amz-meta-sha256` matches)   | (any)           | **Skip the PutObject entirely**; return `unchanged: true`                                                                                       | 200  |
| Different bytes                                     | no `--overwrite`| Reject with both hashes in the body so the caller can decide                                                                                    | 409  |
| Different bytes                                     | `--overwrite`   | Overwrite. Prior version becomes noncurrent (recoverable via `mc cp --version-id …` or `qatlasd storage prune --keep-last N` policy)       | 201  |
| Object exists but has NO sha256 metadata (legacy)   | no `--overwrite`| Treat as "unknown content" → 409. We can't verify equality without downloading and re-hashing, so we require explicit confirm.                  | 409  |

Why bother with sha256 when RustFS already has versioning? See the
trade-off discussion at the end of this file (TL;DR: idempotency
across network retries, conflict detection across users, and
in-transit corruption guard — none of which RustFS provides on its
own).

## Response body (POST /api/papers/{arxiv_id}/upload-pdf)

```json
{
  "arxiv_id": "2501.00010v1",
  "key": "2501.00010v1",
  "pdf_path": "pdf/2501/2501.00010v1.pdf",
  "pdf_bytes": 92606,
  "pdf_sha256": "d1f79cb5b6a0a5466848c2389a549355cb1d6be6caf02dfa197b065b48576ffc",
  "pdf_unchanged": false,
  "overwritten": false,
  "unchanged": false,
  "uploaded_by": "TMYTiMidlY"
}
```

Field semantics:

| Field                        | Meaning                                                                                                       |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------- |
| `arxiv_id`, `key`            | normalised form of the path id                                                                                |
| `pdf_path`                   | object key inside the bucket (`<kind>/<arxiv-prefix>/<arxiv_id>v<n>.<ext>`)                                   |
| `pdf_bytes`                  | bytes actually staged on the server (post-validation)                                                         |
| `pdf_sha256`                 | hex digest of the staged bytes (always present; clients can persist for later integrity audits)               |
| `pdf_unchanged`              | `true` if the existing object had the same sha256 and the server skipped the PutObject                       |
| `overwritten`                | `true` only when `?overwrite=true` was set                                                                    |
| `unchanged`                  | `true` only when the PDF was a no-op (full idempotent retry)                                                  |
| `uploaded_by`                | filled when the deployment configures `QATLAS_USER_HEADER` for a reverse-proxy-injected identity              |

Status code:

- `201 Created` — PDF was written.
- `200 OK` — full idempotent no-op (`unchanged: true`).
- `400 Bad Request` — schema / sha256 / size validation failed (see
  error body for details).
- `409 Conflict` — content collision; body includes
  `existing_sha256`, `new_sha256`, `existing_path` so the caller
  can decide whether to `--overwrite`.
- `403 Forbidden` — PAT lacks the `papers:write` scope.
- `413 Request Entity Too Large` — file exceeded the per-kind cap
  (PDF 100 MiB, markdown 25 MiB).
- `500 Internal Server Error` — store I/O failed; body has the
  underlying error message.

## 409 Conflict response

When `?overwrite=true` is NOT set and the existing object has different
content, the body carries both hashes so the caller can show the user
a meaningful diff prompt:

```json
{
  "detail": "upload conflict; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
  "existing_path": "pdf/2501/2501.00010v1.pdf",
  "existing_sha256": "d1f79cb5b6a0a5466848c2389a549355cb1d6be6caf02dfa197b065b48576ffc",
  "new_sha256": "1af8383a1d54750ad881f54ed1ceff5de98f5d54c00db1a01a064acee76675b0"
}
```

Legacy objects (uploaded before sha256 metadata was added) return the
same 409 with `"existing_sha256": null` and a `"note"` explaining
that equality couldn't be verified.

## `?expected_sha256=` in-transit guard

Pre-computed sha256 from the client gets compared to the server-side
sha256 of the staged bytes BEFORE any S3 write. Mismatch → `400 Bad
Request` with the two hashes:

```json
{
  "detail": "expected_sha256 mismatch — upload may be corrupt in transit",
  "expected_sha256": "deadbeef…",
  "actual_sha256":   "1af8383a…"
}
```

This catches single-byte flips, truncated TLS records, broken middle
proxies — anything that mangles the body between client and server.
The `qatlas` CLI does this for you automatically; raw `curl` callers
who care should compute the hash on their side and pass it:

```bash
SHA=$(sha256sum paper.pdf | awk '{print $1}')
curl -X POST \
    -H "Authorization: Bearer $QATLAS_TOKEN" \
    -F "pdf=@paper.pdf;type=application/pdf" \
    "https://quantum-atlas.ai/api/papers/2501.00010v1/upload-pdf?expected_sha256=$SHA"
```

## Concurrency &amp; race-safety

Two clients uploading the SAME `arxiv_id` at the same time (different
bytes, neither passing `--overwrite`) must get a clean winner-loser
outcome — never silent last-writer-wins. The handler enforces this by
sending S3 `If-None-Match: "*"` conditional PUTs instead of the
naïve "Stat first, then Put if absent" pattern (which has a TOCTOU
window large enough to lose data in practice).

Wire-level guarantees:

- **Concurrent SAME bytes**: ≤ 1 client gets `201`, the rest get
  `200 {unchanged: true}`. Never 409, never 500.
- **Concurrent DIFFERENT bytes**: exactly 1 client gets `201`, the
  rest get `409` with both hashes in the body. The bytes that end up
  in the bucket always belong to the `201` winner.
- **Overwrite path** (`?overwrite=true`): unconditional PUT after a
  sha-match short-circuit check. Bucket versioning preserves the prior
  version as a recoverable noncurrent version (see operator section
  below). Two simultaneous `--overwrite`s land as two versions, last
  one wins as current, neither is lost.

Behind the scenes this rides on RustFS' S3 conditional-write semantics
(`If-None-Match: *` returning `412 PreconditionFailed` when the key
already exists). The dev-mode `LocalStore` emulates the same contract
with atomic `os.Link` so test suites running against either backend
see identical concurrency behavior.

## Recovering an overwritten version (operator side)

Bucket versioning is enabled (see [storage-rustfs.md](../deployment/rustfs.md)).
When `--overwrite` replaces an object, the prior version becomes
noncurrent and stays on disk. Ops can recover it:

```bash
# List all versions of the path
mc ls --versions qatlas/qatlas-raw/pdf/2501/2501.00010v1.pdf

# Copy a specific version-id back as the new current
mc cp --version-id <vid> \
    qatlas/qatlas-raw/pdf/2501/2501.00010v1.pdf \
    qatlas/qatlas-raw/pdf/2501/2501.00010v1.pdf
```

Or alter retention policy with `qatlasd storage prune
--keep-last N` so the prior version is preserved long-term.

## Why bother — RustFS already has versioning, right?

RustFS gives us the storage primitives (durable PUT/GET, version
preservation, delete markers). It does NOT give us:

- **Application-level idempotency**: a network retry of the same
  bytes creates two RustFS versions; sha256 short-circuit collapses
  them to one.
- **Conflict detection**: two users uploading different content to
  the same arxiv_id is silently accepted by RustFS — the second
  PutObject just becomes a new version. Our handler returns 409
  with both hashes so the second uploader notices.
- **In-transit corruption guard**: RustFS only validates bytes once
  they reach the server. `?expected_sha256=` catches mangling on
  the wire BEFORE the bytes hit RustFS.
- **Content-equality auditing**: ListObjectVersions gives ETags
  (MD5-based, sometimes composite for multipart). Our
  `x-amz-meta-sha256` is full-content sha256 of the original
  bytes — directly comparable to what a client computed locally.

The 200-odd lines of handler logic are the "UPSERT policy" on top
of RustFS's "INSERT" primitive — same relationship a typical app
has with a SQL store. See README.md for the wider design rationale.

## 客户端 / 服务端版本偏移

Client-server version skew: the `X-Qatlas-Server-Version` response
header (added in v0.8.0) lets the `qatlas` CLI detect when it's
talking to a newer server and refuse writes (hard fail) / warn on
reads. Old clients that don't inspect the header simply ignore it.

## Contributing by DOI

Since v0.21 the `{arxiv_id}` path slot also accepts a **DOI**
(`10.<registrant>/<suffix>`). This exists for *published* versions —
the Physical Review / Nature / etc. PDF — which is a different artifact
from any arXiv preprint, and which may have no arXiv id at all.

DOI is a **second, independent identity** alongside arXiv (still a
single unique index per asset). It is NOT collapsed onto the arXiv
preprint: a DOI upload is always stored and recorded under the DOI,
even when OpenAlex links that DOI to an arXiv work.

### Storage layout

DOI-indexed assets live in a namespace disjoint from arXiv's `<yymm>/`
shards, so an arXiv id and a DOI can never collide on the same key:

```text
<kind>/doi/<registrant>/<safe-suffix>.<ext>

pdf/doi/10.1103/physrevlett.123.070501.pdf
markdown/doi/10.1103/physrevlett.123.070501.md
images/doi/10.1103/physrevlett.123.070501.zip
```

The DOI is lower-cased (DOIs are case-insensitive); nested slashes in
the suffix become `__`. In the Neo4j catalog the contribution is a
`:PaperWork` node keyed `arxiv_id = "doi:<doi>"` (reusing the
`arxiv_id` UNIQUE constraint for atomic, race-safe MERGE) with
`identifier_scheme = 'doi'`, `source = 'doi-upload'`, and the asset
pointers + verification fields below.

> **Canonical resolution (DOI wins)**: a `:PaperWork` node with
> `identifier_scheme='doi'` ALWAYS takes precedence over its arxiv twin
> when both exist. `GET /api/papers/<id>/...` serves the DOI bytes
> whether the caller supplied the DOI or the linked arxiv id; DOI is
> the canonical identity of the *published* version, the arxiv preprint
> is a secondary artifact. The DOI fast path skips OpenAlex entirely
> when the local catalog already has the DOI node.
>
> Two shapes hit this rule:
>
> 1. **Caller passes a DOI** — `LookupDOI` resolves to the local DOI
>    namespace first; on miss, the request falls through to OpenAlex,
>    which may surface an arxiv twin to serve.
> 2. **Caller passes an arxiv id** — the dispatcher reverse-looks-up
>    `doi_arxiv_id = <bare arxiv id>` to find any DOI twin. Hit → the
>    request is redirected to the DOI handlers; miss → regular arxiv
>    path. Redirects are observable via the `X-QAtlas-Canonical-DOI`
>    response header and a `served_as_doi_canonical (…; pass
>    ?force_arxiv=1 to opt out)` entry in `X-QAtlas-Defaults-Applied`.
>
> **Per-request opt-out**: append `?force_arxiv=1` (or `?force_arxiv=true`)
> to bypass the DOI-canonical rule and force the arxiv path. With DOI
> input + `force_arxiv=1` + no arxiv twin in OpenAlex you get `409
> Conflict` (caller asked for arxiv, none exists). With arxiv input +
> `force_arxiv=1` the reverse lookup is skipped and the request lands on
> the arxiv handlers directly.

### Metadata enrichment (auto-fetched from OpenAlex)

Because nothing binds raw PDF bytes to a DOI, the server resolves the
DOI's **OpenAlex metadata** (the same resolver used for DOI→arXiv on
the download path) and persists the canonical title / authors / linked
arxiv id on the catalog node. **Contributors cannot supply or override
this metadata** — by design, the only metadata input is the DOI itself,
and a typo'd DOI surfaces either as a different paper's record (the
contributor sees it via the response header / JSON block and can resubmit
under the correct DOI) or as `doi-not-found`.

```bash
qatlas contrib pdf 10.1103/PhysRevLett.123.070501 --pdf ./published.pdf
```

Direct HTTP — no `title`/`authors` form fields:

```bash
curl -X POST \
    -H "Authorization: Bearer $QATLAS_TOKEN" \
    -F "pdf=@published.pdf;type=application/pdf" \
    "https://quantum-atlas.ai/api/papers/10.1103/PhysRevLett.123.070501/upload-pdf"
```

The outcome is **recorded** on the node (`verification_status`,
`doi_title`, `doi_authors`, `doi_arxiv_id`, `verified_at`) and returned
whenever the upload actually writes bytes:

- response header `X-QAtlas-Verification: <status>`
- response body `verification` block (`status`, `title`, `authors`,
  `arxiv_id`)

> A no-op re-upload (the object's sha256 already matches) does **not**
> re-run the OpenAlex lookup: the header and `verification` block are
> omitted and the catalog node is left untouched. The prior metadata is
> still on the node. This keeps repeated uploads from hitting OpenAlex.
>
> Resolved metadata is cached in-process for a few minutes (same LRU as
> DOI→arXiv resolution, keyed separately), so a PDF-then-MinerU pair for
> one DOI costs a single OpenAlex lookup.
>
> **Metadata preservation under failure:** when the server cannot resolve
> the DOI on a given upload (transient OpenAlex outage, server unconfigured,
> or `doi-not-found`), the catalog write preserves any previously-stored
> `doi_title` / `doi_authors` / `doi_arxiv_id` rather than overwriting them
> with empty values — a transient failure during a re-upload must never
> erase a prior verified record. `verification_status` itself is always
> overwritten so operators see the latest attempt.

| `verification_status`  | Meaning                                                             |
| ---------------------- | ------------------------------------------------------------------- |
| `verified`             | OpenAlex returned a record; title / authors / linked arxiv id stored |
| `doi-not-found`        | OpenAlex confirmed the DOI does not exist                           |
| `metadata-unavailable` | OpenAlex was unreachable / errored                                  |
| `unconfigured`         | server has no `QATLAS_OPENALEX_MAILTO`, so enrichment is disabled   |

### `?verify=` policy

The DOI's OpenAlex resolution is **advisory by default** (`verify=warn`):
the outcome is recorded and surfaced, but the upload still succeeds even
when OpenAlex cannot confirm the DOI — coverage is incomplete and blocking
would reject legitimate contributions.

Pass `?verify=strict` to make resolution mandatory:

- `doi-not-found` → `409 Conflict` (contributor-correctable: typo'd DOI)
- `metadata-unavailable` / `unconfigured` → `503 Service Unavailable`
  (server-side, can't verify)

`verified` proceeds.

All the arXiv-path guarantees still apply to DOI uploads: sha256
idempotency (200 / 409), `?expected_sha256=` in-transit guard, `%PDF-`
magic check, `?overwrite=true`, and `upload-mineru`'s `?pdf_sha256=`
source-PDF cross-check (against the stored DOI PDF).

### Client coverage

CLI support for DOI uploads covers **both** `pdf` and `mineru` subcommands:

- `qatlas contrib pdf <DOI> --pdf ...` accepts a DOI in the ID slot and
  exposes `--verify`; the verification status is printed to stderr from
  the `X-QAtlas-Verification` response header.
- `qatlas contrib mineru <DOI> --zip ...` likewise accepts a DOI and the
  same `--verify` flag. The flag is honoured by the server's DOI path
  and a no-op on the arXiv path (the arxiv catalog is enriched through
  a separate sync pipeline), so the same invocation works for either
  identity.
- The arXiv-keyed `qatlas contrib mineru` runner mode (claim queue →
  MinerU → upload) is arxiv-only end-to-end: it does not pick DOI work
  out of the queue. For a published PDF, drive MinerU manually and push
  the result with the DOI direct-zip form above (`qatlas contrib mineru
  <DOI> --zip ...`), or POST the bundle directly to
  `/api/papers/<DOI>/upload-mineru` with `?verify=` / `?pdf_sha256=` as
  described above.
