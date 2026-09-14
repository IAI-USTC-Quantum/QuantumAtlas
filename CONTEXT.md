# QuantumAtlas — Context

QuantumAtlas is a Go paper-collection, search and registry host with a React Web UI.
It keeps paper identities/assets in PostgreSQL, raw content in object storage, and auth
in PocketBase. Search/RAG/matching apps and Lean/Wiki content have independent owners;
do not recreate retired Python, graph, wiki or theorem implementations in this module.

## Development and distribution boundary

- `go.mod` / `go.sum` define the Go toolchain/dependency graph; `web/.node-version`
  and the npm lock define frontend inputs. There is no root Python/Pixi project.
- Python files still present are documentation or CI helpers, not a Python package.
  `PYPI_README.md` remains the permanent migration notice to independent `qatlas-cli`.
- Git stores source, not executables or generated `web/dist`. Normal Go builds work
  without Node/Sphinx; release builds use `embedui` after generating complete UI/docs.
  Tagged `go install` binaries fetch and verify ONLY their matching Release UI.
- Normal tests skip real-service paths even if live environment flags are inherited.
  `-tags integration` plus explicit test targets enables DB/API fixtures; `-tags e2e`
  separately enables production smoke. Never use real deployment targets by default.
- Start at [the developer guide](docsite/dev/development.rst) or
  [contributing](docs/contributing.md). Current config is YAML-only, not `.env`.
- Historical tags/ADRs are audit records. The final Python tag is excluded from
  GoReleaser tag selection; it is not deleted, moved or republished.

## Language

### External formalization terminology (`paper → claim → theorem → verification`)

These terms describe the external Lean/content integration and historical design,
not active claim-authoring collections in this Go host. ADR 0008 moved authoring to
the Lean repository; preserve the vocabulary without restoring the removed subsystem.

**Paper**:
A source of record (arXiv / DOI work) that results are extracted from.

**Claim**:
A single pre-proof natural-language statement extracted from a Paper — a candidate for
formalization (paper-of-record + near-verbatim NL + stated/implicit assumptions). Owns the
namespace `claim_id = <paper_id>:<slug>`. A Paper may yield several Claims; a Claim formalizes
into 0..1 Theorem. Claim authoring, issue filing, prover work units and proof closure now
belong to qatlas-lean/content tooling, not this Go host. QuantumAtlas supplies the paper
catalog and lookup API; it does not create new host-level Claim storage.
_Avoid_: "theorem" (a Claim is pre-proof), proposition, unit / unit_id (a prover-side work/registry
id that lives after the issue — not a QA concept; most lean registry entries are proof-closure
lemmas/definitions with no Claim at all).

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
e.g. an external verification capability), and its in-memory caches. Current runtime
builtin manifests are `search-remote`, `rag-remote`, `match-remote`, and `downloader`
(registered in `cmd/qatlasd/main.go`); they are not the removed graph/wiki/theorems modules.

**Builtin**:
A first-party Plugin compiled into qatlasd, running in-process (Go). A builtin may be
an in-process CLIENT to an independently deployed app; it does not move that app's
implementation or release into this repository.
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
> 当前保留的是论文默认资产的处理租约，按事务与 row lock 协调竞争；持久化契约见
> `internal/registry/lease.go` 与相应 migration，而不是旧的 `paper_works` 单表模型。

_Avoid_: claim, claim_id, claimed_by (renamed — "claim" now means the formalization statement above).

### Paper catalog & reference data

**Paper catalog**:
The host-core index of the Papers QuantumAtlas hosts — asset status (PDF / Markdown / DOI),
MinerU lease — derived from and rebuildable off the asset buckets. Backed by `papers`,
`paper_assets`, `paper_identities` and related tables in the central PostgreSQL (ADR 0009).
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
