# Upload API & sha256-aware idempotency

> Users uploading PDFs / markdown / metadata to QuantumAtlas server,
> via the `qatlas upload …` CLI or direct HTTP calls. Covers the
> request shape, response shape, status code contract, sha256
> deduplication semantics, conflict handling, and `?expected_sha256=`
> in-transit guard.
>
> Storage / ops perspective (RustFS env vars, IAM policy, bucket
> versioning lifecycle, `quantumatlas storage prune` operator guide)
> lives in [storage-rustfs.md](storage-rustfs.md).

## Endpoints

| Method  | Path                                          | Scope          |
| ------- | --------------------------------------------- | -------------- |
| `POST`  | `/api/papers/{arxiv_id}/upload-pdf`           | `papers:write` |
| `POST`  | `/api/papers/{arxiv_id}/upload-markdown`      | `papers:write` |

Both routes require auth: either a browser session token or a PAT
(`Authorization: Bearer qat_…`) whose scopes include `papers:write`.
See [contribution-workflow.md](contribution-workflow.md) for how to
mint a PAT.

`{arxiv_id}` MUST include the explicit `vN` version suffix. Both
schemes are accepted:

- new style: `2501.00010v1` (post April 2007)
- old style with category prefix: `quant-ph/9508027v1` (pre April 2007)

The server rejects bare ids without `vN` to keep on-disk paths and
listings deterministic.

## CLI quick start

```bash
# Upload a fresh PDF (and optional metadata JSON sibling)
qatlas upload pdf 2501.00010v1 \
    --pdf ./paper.pdf \
    --metadata ./paper.json

# Re-upload the same bytes → 200 OK unchanged (no S3 write)
qatlas upload pdf 2501.00010v1 --pdf ./paper.pdf

# Re-upload DIFFERENT bytes → 409 Conflict (refuses to overwrite)
qatlas upload pdf 2501.00010v1 --pdf ./paper-v2.pdf

# Force overwrite (old version preserved by bucket versioning,
# recoverable until the next storage prune)
qatlas upload pdf 2501.00010v1 --pdf ./paper-v2.pdf --overwrite

# Markdown (e.g. MinerU output) uses the same flag shape
qatlas upload markdown 2501.00010v1 \
    --markdown ./paper.md \
    --source mineru
```

The CLI streams the file once through sha256 before posting and
attaches `?expected_sha256=<hex>` automatically. Same for
`--metadata` → `?expected_metadata_sha256=<hex>`. So in-transit byte
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
| Different bytes                                     | `--overwrite`   | Overwrite. Prior version becomes noncurrent (recoverable via `mc cp --version-id …` or `quantumatlas storage prune --keep-last N` policy)       | 201  |
| Object exists but has NO sha256 metadata (legacy)   | no `--overwrite`| Treat as "unknown content" → 409. We can't verify equality without downloading and re-hashing, so we require explicit confirm.                  | 409  |

Why bother with sha256 when RustFS already has versioning? See the
trade-off discussion at the end of this file (TL;DR: idempotency
across network retries, conflict detection across users, and
in-transit corruption guard — none of which RustFS provides on its
own).

## Response body (POST /api/papers/{arxiv_id}/upload-pdf)

The handler returns the same JSON shape regardless of branch — caller
inspects `unchanged` + `overwritten` to know what happened:

```json
{
  "arxiv_id": "2501.00010v1",
  "key": "2501.00010v1",
  "pdf_path": "pdf/2501/2501.00010v1.pdf",
  "pdf_bytes": 92606,
  "pdf_sha256": "d1f79cb5b6a0a5466848c2389a549355cb1d6be6caf02dfa197b065b48576ffc",
  "pdf_unchanged": false,
  "metadata_path": "json/2501/2501.00010v1.json",
  "metadata_bytes": 538,
  "metadata_sha256": "a7d48eabac9d70f75a2656bd6a8199dd…",
  "metadata_unchanged": false,
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
| `metadata_*`                 | populated when the `metadata` multipart part was provided                                                     |
| `overwritten`                | `true` only when `?overwrite=true` was set                                                                    |
| `unchanged`                  | `true` only when EVERY part was a no-op (full idempotent retry)                                               |
| `uploaded_by`                | filled when the deployment configures `QATLAS_USER_HEADER` for a reverse-proxy-injected identity              |

Status code:

- `201 Created` — at least one part was written.
- `200 OK` — full idempotent no-op (`unchanged: true`).
- `400 Bad Request` — schema / sha256 / size validation failed (see
  error body for details).
- `409 Conflict` — content collision; body includes
  `existing_sha256`, `new_sha256`, `existing_path` so the caller
  can decide whether to `--overwrite`.
- `403 Forbidden` — PAT lacks the `papers:write` scope.
- `413 Request Entity Too Large` — file exceeded the per-kind cap
  (PDF 100 MiB, markdown 25 MiB, metadata JSON 2 MiB).
- `500 Internal Server Error` — store I/O failed; body has the
  underlying error message.

## 409 Conflict response

When `?overwrite=true` is NOT set and the existing object has different
content, the body carries both hashes so the caller can show the user
a meaningful diff prompt:

```json
{
  "detail": "PDF already exists at pdf/2501/2501.00010v1.pdf with different content; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
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

For `upload-pdf` with metadata, the metadata part has its own param:
`?expected_metadata_sha256=<hex>`.

## Multipart atomicity

`POST /api/papers/.../upload-pdf` accepts two form parts: `pdf` and
optional `metadata`. The server stages BOTH parts and runs ALL
conflict checks BEFORE any PutObject. So a metadata-side 409 never
leaves a half-uploaded PDF on the bucket. Either both writes happen
or neither does.

This matters when retrying after a partial failure: the second
attempt sees a clean "neither exists" state, not "PDF exists but
metadata doesn't" which would force confusing `--overwrite` semantics.

## Recovering an overwritten version (operator side)

Bucket versioning is enabled (see [storage-rustfs.md](storage-rustfs.md)).
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

Or alter retention policy with `quantumatlas storage prune
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
