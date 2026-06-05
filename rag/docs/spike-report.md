# Phase 2.5 / Phase 6 spike report

> Generated 2026-06-04 12:10. Build still in progress (37k / 89k processed).

## What this document is

End-to-end validation of the qatlas-rag pipeline against a real subset
of the production corpus. Confirms:

1. The pipeline works end-to-end.
2. Retrieval quality is good on real quantum-computing queries.
3. The Internal Go server's `/api/rag/*` reverse_proxy works through PB
   middleware + scope guards.

## Subject under test

- **Collection**: `qatlas_papers_v1_dryrun` (hybrid dense+sparse,
  bge-m3, 1024-d, Qdrant 1.12.4 on 1810)
- **Coverage**: ~37 k papers from `qatlas-md` bucket processed so far
  (build still walking remaining prefixes). 89019 total expected.
- **Stack** in the validated request path:
  - curl with system PAT
  - qatlasd (Go, in-tree build of v0.15.0+internal.2-pre, includes the
    LimitReader fix in `internal/routes/rag.go`)
  - `authGuard` + `scopeGuard("rag", "read")`
  - `httputil.ReverseProxy` → `127.0.0.1:8802`
  - sidecar (FastAPI, hybrid RRF retrieval, top 50 → cross-encoder
    rerank → top 5)
  - embed-worker (FastAPI on RTX 5080, bge-m3 + bge-reranker-v2-m3)
  - Qdrant gRPC (mesh `10.144.18.10:6334`)

## Performance baseline

From the 2k sanity build (29 min) and ongoing 89k build (rate stable at
1.3-1.4 papers/s steady-state).

| metric | value | notes |
|---|---|---|
| papers/s end-to-end | **1.3 – 1.4** | includes S3 GET, parse, chunk, embed (dense+sparse), upsert |
| embed throughput | **68 chunks/s** | bge-m3 fp16 on RTX 5080, batch 200 |
| per-paper p50 (full pipe) | 0.69 s | of which embed=0.53s, s3=0.19s, upsert=0.10s, parse=0.04s |
| /search end-to-end | **0.5 – 2 s** | dominated by reranker on 50 candidates |
| 502 / error rate | **0.003 %** | 1 / 36k after EMBED_BATCH=200 fix; was 0.2% pre-fix |
| VRAM peak | **2.66 GB** | bge-m3 + reranker fp16, plenty of headroom on 5080 16 GB |

## Retrieval quality

Tested 8 representative queries against the partial corpus. Reranker
scores: positive = good match, negative = weak / no match.

| query | top hit | score | verdict |
|---|---|---|---|
| Shor algorithm polynomial-time integer factoring | 1008.0010v1 §"3.2. Shor's Factoring Algorithm" | **+5.13** | ✅ exact |
| Grover amplitude amplification speedup | 1409.3305v2 "Fixed-point quantum search with an optimal..." | **+4.05** | ✅ |
| surface code quantum error correction threshold | 1202.4316v3 "**High Threshold Error Correction for the Surface code**" | **+5.96** | ✅ exact title |
| Bell inequality CHSH experimental violation | 1210.5291v2 §"V. VIOLATION OF THE CHSH-BELL INEQUALITY" | **+5.62** | ✅ exact section |
| QAOA Max-Cut performance analysis | 2105.11946v3 "QAOA with Adaptive…" | +2.40 | ✅ |
| Higgs boson discovery LHC ATLAS CMS (off-topic) | 1406.5636v1 (Higgs paper) | +2.79 | ✅ recalls correctly even off the quant-ph core |
| transformer self-attention scaling laws (not in corpus) | (best score is negative) | **−2.00** | ✅ correctly signals "no good match" |
| variational quantum eigensolver convergence proof | 1707.06408v1 "Robust determination of molecular spectra" | +0.17 | ⚠️ weak, retest after full 89k |

**Hybrid > dense**: spot checks showed hybrid (dense+sparse RRF) typically
returns more on-topic papers when the query contains specific terms
(algorithm names, acronyms like "CHSH"). Dense-only kept missing the
1409.3305v2 fixed-point Grover paper that hybrid surfaces in top 5.

## Issues found and fixed during the spike

1. **embed-worker `EmbedRequest.texts` capped at 256 items** (FastAPI
   pydantic validation) → 0.2 % of papers with > 256 chunks failed with
   422. Fixed by batching embed calls at 200 chunks in
   `scripts/spike/full_build.py::process_one`. Verified by re-running
   the 4 originally-failed papers (265-308 chunks each) — all succeed.

2. **systemd `EnvironmentFile=` overrides `Environment=`** for the same
   key, opposite of textual-order intuition. The sidecar was reading
   `QATLAS_RAG_QDRANT_COLLECTION=qatlas_papers_spike` from .env even
   though the unit file had `Environment=...=qatlas_papers_v1_dryrun`
   later. Fixed by updating `.env` to the production collection name.

3. **PB middleware (Echo) re-buffers `r.Body` and may hand out more
   bytes than the original Content-Length advertises**, causing
   `net/http transport connection broken: ContentLength=N with Body
   length 2N` from `httputil.ReverseProxy`. Fixed in
   `internal/routes/rag.go` by capturing the declared ContentLength
   before any other mutation and wrapping `r.Body` in
   `io.LimitReader(r.Body, declaredLen)` inside the Director.
   Regression test: `TestRAGProxyDefendsAgainstOversizedBody`.

4. **Mihomo TUN proxy intercepts mesh IPs** for boto3 and qdrant-client
   even though they're 10.144.18.10. Always set
   `NO_PROXY=10.144.18.10,127.0.0.1,localhost` for any qatlas-rag
   process; the systemd unit already does this.

## Known gaps (post-build)

- Payload fields `title`, `authors`, `categories` are `null` — ingester
  doesn't read arxiv metadata. SPA will fall back to `{canonical}v{n}`
  for the displayed title. Filling these requires joining against
  `paperindex.parquet` or arxiv API; deferred.
- `qatlas_papers_v1_dryrun` collection name is provisional. After 89k
  completes and quality is signed off, either (a) rename via
  `qdrant.create_collection_alias` if Qdrant supports it, or (b) just
  update sidecar `.env` to keep using `_dryrun`. Most pragmatic: keep
  the name, document that "dryrun" is a misnomer post-completion.
- Build time per re-index: ~22 h for full 89 k. Incremental re-index
  (delta only) via manifest is the supported path going forward —
  the runner already short-circuits when etag is unchanged.
