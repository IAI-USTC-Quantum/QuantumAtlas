# Upload API & sha256-aware idempotency

> Users uploading PDFs / markdown to QuantumAtlas server, via the
> `qatlas upload …` CLI or direct HTTP calls. Covers the request
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

> **v0.8.0 BREAKING CHANGE**: the legacy `POST upload-markdown`
> endpoint that accepted a bare `.md` file has been removed. Use
> `upload-mineru` with the **entire MinerU result zip** instead —
> the server unpacks the zip and writes both the markdown and every
> referenced image into their respective per-kind buckets, so
> contributions no longer silently drop figures. See
> [upgrade notes](#breaking-change-v080) at the bottom for the
> migration recipe.

## CLI quick start

```bash
# Upload a fresh PDF
qatlas upload pdf 2501.00010v1 --pdf ./paper.pdf

# Re-upload the same bytes → 200 OK unchanged (no S3 write)
qatlas upload pdf 2501.00010v1 --pdf ./paper.pdf

# Re-upload DIFFERENT bytes → 409 Conflict (refuses to overwrite)
qatlas upload pdf 2501.00010v1 --pdf ./paper-v2.pdf

# Force overwrite (old version preserved by bucket versioning,
# recoverable until the next storage prune)
qatlas upload pdf 2501.00010v1 --pdf ./paper-v2.pdf --overwrite

# Upload a MinerU result bundle (full.md + images/*) — push the
# raw zip MinerU returned at its `full_zip_url`, the server unzips it.
qatlas upload mineru 2501.00010v1 \
    --zip ./mineru-result.zip \
    --source mineru-client-v0.8
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

## Breaking change v0.8.0

The pre-v0.8 `POST /api/papers/{arxiv_id}/upload-markdown` endpoint
took a single `.md` file as the multipart part `markdown`. It is
**gone** as of v0.8.0 — calling it returns `404 no POST handler`.

Migration:

```diff
- POST /api/papers/2501.00010v1/upload-markdown
-   multipart "markdown": full.md
+ POST /api/papers/2501.00010v1/upload-mineru
+   multipart "mineru_zip": full MinerU result zip (full.md + images/*)
```

CLI:

```diff
- qatlas upload markdown 2501.00010v1 --markdown full.md --source mineru
+ qatlas upload mineru   2501.00010v1 --zip path/to/mineru.zip --source mineru
```

Why the change: the old endpoint only stored the markdown and
silently dropped every figure in the bundle, leaving detail pages
with broken image references. `upload-mineru` keeps the bundle
intact; the server unpacks it so the same parser used for
server-side silent conversion writes both the markdown and every
image to their respective buckets.

Client-server version skew: the `X-Qatlas-Server-Version` response
header (added in v0.8.0) lets the `qatlas` CLI detect when it's
talking to a newer server and refuse writes (hard fail) / warn on
reads. Old clients that don't inspect the header simply ignore it.
