# QuantumAtlas — Context

QuantumAtlas is the knowledge + verification host for quantum-algorithm papers: it catalogs
papers, surfaces a reviewable knowledge base, and hosts plugins (graph, RAG, wiki, theorems)
that read through their respective backends — the last two via server-side `git pull --ff-only`
of upstream content repos.

## Language

### Formalization pipeline (`paper → claim → theorem → verification`)

**Paper**:
A source of record (arXiv / DOI work) that results are extracted from.

**Claim**:
A single pre-proof natural-language statement extracted from a Paper — a candidate for
formalization (paper-of-record + near-verbatim NL + stated/implicit assumptions). Owns the
namespace `<paper_id>:<slug>`.
_Avoid_: unit, unit_id (retired — a Claim's id is the former unit_id), "theorem" (a Claim is
pre-proof), proposition.

**Theorem**:
A machine-checked Lean 4 declaration that faithfully formalizes a Claim (a `registry.json` entry
in qatlas-lean). Always post-proof.
_Avoid_: using "theorem" for the pre-proof statement (that is a Claim), lemma (Theorem covers the
named result; its proof-path lemmas are the dependency closure).

**Verification**:
A plugin's verdict that a Theorem faithfully formalizes its Claim (verdict + evidence_url +
per-plugin payload, keyed by `plugin_id`).
_Avoid_: validation, check, audit-result.

### Plugin platform

**Host core**:
The plugin-agnostic spine of qatlasd: the plugin registry/manifest/lifecycle, the **Paper**
catalog (shared by every Plugin), auth/PAT, the events bus, PocketBase itself, and truly
domain-neutral host capabilities (`papers/*`, `events/publish`). The Host core **does not** carry
domain words from any specific Plugin (no `claim`, `theorem`, `verification`, `page` in
`internal/`'s host-level packages or `hostapi/core.go`).

**Plugin**:
A capability module the host loads via a manifest. A Plugin **owns its domain end-to-end**: its
vocabulary, its persistent collections (PocketBase migrations the plugin's package registers),
its REST routes under `/api/<plugin_id>/*`, its hostapi methods (under the plugin's namespace,
e.g. `theorems.verifications.submit`), and its in-memory caches. The four first-party Plugins are
graph, rag, wiki, theorems.

**Builtin**:
A first-party Plugin compiled into qatlasd, running in-process (Go). The `kind` of all four core
plugins.
_Avoid_: in-process-go (former manifest value), in-process.

**External**:
A third-party Plugin running as a separate process, speaking JSON-RPC to the host. Its
`transport` is either **socket** (the plugin dials into the host's WebSocket) or **stdio** (the
host spawns the plugin and talks over its stdin/stdout, à la an LSP language server).
_Avoid_: jsonrpc-ws (now transport=socket), jsonrpc-stdio (now transport=stdio).

### Paper processing

**Lease（处理租约）**:
A time-bounded exclusive hold on a Paper while a worker (e.g. MinerU) converts it, so two workers
don't process the same Paper. Carried by `lease_id` / `leased_by` / `lease_expires_at` on the
paper catalog.

> 中文：**MinerU 处理租约**——一份限时独占凭证，授权某个 worker 把一篇有 PDF、还没 Markdown
> 的论文转成 Markdown。同一时刻只能一个 worker 持有；过期后自动作废，其他 worker 可重抢。
> 在 PostgreSQL `paper_works` 表上用 row lock + `lease_id`/`leased_by`/`lease_expires_at`
> 三个字段实现。

_Avoid_: claim, claim_id, claimed_by (renamed — "claim" now means the formalization statement above).

### Paper catalog & reference data

**Paper catalog**:
The host-core index of the Papers QuantumAtlas hosts — asset status (PDF / Markdown / DOI),
MinerU lease — derived from and rebuildable off the asset buckets. Backed by the `paper_works`
table in the central PostgreSQL.
_Avoid_: "the database" / "the catalog" used loosely (the same PostgreSQL also holds the OpenAlex
corpus, a different thing).

**OpenAlex corpus**:
A locally-held copy of OpenAlex works (each record stored verbatim as `jsonb`, only filtered,
never modified), used for citation context, batch analysis, and vector joins against our own
assets. Far larger than the Paper catalog and mostly **not** Papers we host; lives as separate
tables in the same central PostgreSQL. See ADR `0006`.
_Avoid_: conflating with the Paper catalog / `paper_works`; "OpenAlex mirror" (we never re-serve
it as an outbound API).

**Reference**:
A bibliographic pointer from a Claim to a cited work, stored as a namespaced, unversioned `kind:id`
string (`arxiv:2208.06941`, `openalex:W4406693713`, `doi:10.22331/…`) in the Claim's `references`
field and the gitea issue body. Resolved to metadata on demand via `GET /api/papers/lookup` against
the OpenAlex corpus. See ADR `0007`.
_Avoid_: reference lemma (a lean in-corpus Lean reuse-shortlist entry — a different thing), proof
citation (the proof-prose citation carrying a `citation_string`), "full citation" (a Reference is
just the id, not a rendered citation).
