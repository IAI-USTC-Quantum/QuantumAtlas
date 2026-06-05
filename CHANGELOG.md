# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) during pre-1.0 development with Commitizen bump rules.

## v0.19.0a0 (2026-06-05)

### Feat

- **client,papers,spa**: qatlas paper subcommand + /papers/search RAG UI
- **papers,rag**: paper-access expansion (PDF/RAG/DOI/resolution) + rename two server env vars
- **qatlasd**: storage migrate-layout subcommand for pre-A1 old-style PDFs
- **arxiv,openalex**: server-side PDF fetcher + DOI resolver

### Refactor

- **auth**: OAuth device-code login + drop client-side token shadows
- **paperassets**: structured ParsedArxivID + per-category storage layout

## v0.18.2 (2026-06-04)

### Fix

- **test**: align commitizen-config assertion with pyproject.toml

## v0.18.1 (2026-06-04)

### Fix

- **mineru**: make image-zip build deterministic so re-uploads are idempotent

## v0.18.1a1 (2026-06-04)

### Refactor

- **mineru**: client-side multi-key rotation + expose converter counters in /api/health

## v0.18.1a0 (2026-06-04)

### Refactor

- **storage**: write images as a single zip archive instead of scattered files

## v0.18.0 (2026-06-04)

### BREAKING CHANGE

- **mineru**: `MINERU_API_TOKEN` (singular) renamed to `MINERU_API_TOKENS`
  (plural, CSV list — `tok-a,tok-b,tok-c`) so the server can rotate
  through multiple keys when one hits today's daily limit. The singular
  name is still accepted for one minor cycle (Load emits a slog.Warn and
  promotes it to a single-element list); it will be removed in v0.19.0.

### Feat

- **mineru**: multi-key token pool with automatic daily-limit fail-over

## v0.17.0 (2026-06-04)

### BREAKING CHANGE

- cross-origin ?from= values, protocol-relative
references (//host) and non-http(s) schemes are silently coerced to
"/" instead of being navigated to. No legitimate caller in-tree
passes those, but a downstream integration that piggy-backs on the
search param to bounce out of the SPA will need to handle the bounce
itself.

### Feat

- **auth**: OAuth 2.0 Device Authorization Grant (RFC 8628) + SPA loopback flow
- **papers**: re-introduce /api/papers/{id}/markdown[/status] behind QATLAS_ASSET_DOWNLOADS_ENABLED (#8)
- **service**: tolerate missing .env and announce the pinned dotenv path

### Fix

- **web**: reject cross-origin ?from= destinations on /login and /auth/callback
- **qatlasd**: make non-serve subcommands tolerate broken .env
- **client**: use platformdirs for the user config path (cross-platform)

### Refactor

- remove /api/lint placeholder endpoint and SPA Lint button
- **config**: drop the quantum-atlas/ legacy XDG fail-loud guard

## v0.17.0a0 (2026-06-03)

### Feat

- **web**: inject dev system PAT into vite /api proxy
- **serve**: add 20 qatlasd-specific CLI flags with [env: QATLAS_FOO=] tags
- **server**: refuse to coexist with another qatlasd on the same pb_data
- **papers**: re-introduce GET /api/papers/{id}/markdown + /markdown/status behind QATLAS_ASSET_DOWNLOADS_ENABLED master switch (default off); when also configured with MINERU_API_TOKEN the server transparently triggers MinerU on cache miss, otherwise it serves cached markdown only and returns 503 on miss

### Fix

- **qatlasd**: address rubber-duck findings on v0.17.0 config redesign

### Refactor

- client config is now YAML-only with auto-init (no env/CLI/dotenv)
- rewrite client YAML config on pydantic-settings YamlConfigSettingsSource
- rename XDG sub-namespace from quantum-atlas to qatlasd

## v0.16.0 (2026-06-03)

### Feat

- **config**: hide token input when value omitted from `qatlas config set`
- **docker**: add Dockerfile and docker-compose templates (full-stack + standalone)
- **client**: migrate user config from .env to YAML at ~/.config/qatlas/config.yaml
- **qatlasd**: add config init/show/path subcommands and env-alias flag hints

### Fix

- **mineru-client**: keep claim on per-paper fatal so bad PDFs don't poison the queue
- **mineru**: classify per-paper page/size limits as Fatal, not DailyLimit

## v0.15.0 (2026-06-03)

### Fix

- align CLI help and docs with v0.15.0a* removals

## v0.15.0a5 (2026-06-03)

### Feat

- **client**: drop ./.env cwd fallback, XDG-only config

## v0.15.0a4 (2026-06-02)

### Fix

- **client**: bootstrap_env() mirrors dotenv values into os.environ

## v0.15.0a3 (2026-06-02)

### Feat

- **client**: XDG-style user config + `qatlas config` subcommand

### Refactor

- **config**: drop server-side MinerU config surface

## v0.15.0a2 (2026-06-02)

### Fix

- **client/mineru**: accept RustFS presign URLs alongside arxiv URLs

## v0.15.0a1 (2026-06-02)

### Fix

- **papers**: mineru-claim works for pre-2007 papers + serves RustFS presign URLs

## v0.15.0a0 (2026-06-02)

### BREAKING CHANGE

- Endpoints GET /api/papers/{id}/markdown, GET /api/papers/{id}/resources, /api/shares/*, /share/{token}/* are removed. PAT scopes shares:read and shares:write are no longer issued and existing tokens with those scopes silently lose access (re-mint needed). qatlas markdown CLI command is gone.

### Feat

- **mineru**: queue mode uses MinerU batch API + daily-limit back-off
- **mineru**: add Python batch submission API (submit_url_batch / get_batch)
- **mineru**: add Go batch submission API (SubmitURLBatch / GetBatch)
- **mineru**: classify API errors (retryable / fatal / daily-limit)

### Refactor

- drop shares + server-side mineru + outbound asset endpoints

## v0.14.1 (2026-06-02)

### Fix

- **papers**: mineru-claim PDF URL now uses RustFS presign, not arxiv.org
- **spa**: commit /pat redirect route source (was untracked)

## v0.14.0 (2026-06-02)

### BREAKING CHANGE

- POST /api/papers/{id}/upload-markdown is removed
without a transition period; calls return 404. CLI subcommand
'qatlas upload markdown' is removed and prints an explicit migration
error pointing at 'qatlas upload mineru --zip ...'. Client and server
binaries must be upgraded together — the new
X-Qatlas-Server-Version header lets older clients hard-fail clearly
instead of dying at a 404 mid-upload.

### Feat

- **papers**: replace upload-markdown with upload-mineru (zip bundle, no image loss)

## v0.13.0 (2026-06-02)

### Feat

- surface upstream data source attribution in SPA footer and docs

### Fix

- **healthz**: redact Error string on anonymous /api/health to close topology leak

## v0.12.0 (2026-06-02)

### BREAKING CHANGE

- the public installer URL has moved from
`https://quantum-atlas.ai/install-server.sh` to
`https://quantum-atlas.ai/install-qatlasd.sh`. The old URL now
404s on every edge running v0.12.0+. Any external bookmarks /
documentation / chat-history one-liners pointing at the old URL
will break and must be updated. The release artefact filename
(`qatlasd-<os>-<arch>`) is unchanged.
- PyMuPDF is no longer a supported PDF parser backend.
The `--parser pymupdf` option on `qatlas ingest`, the
`qatlas parser parse-pdf` subcommand, and the
`qatlas.parser.pdf_parser` Python module have been removed. Convert
callers to MinerU (`qatlas mineru <id>` for one-off conversions, or
`qatlas ingest <id> --parser mineru` from a pipeline).

### Feat

- **parser**: drop pymupdf, MinerU is the sole PDF backend

### Fix

- **ci,nightly**: fail-loud on missing secrets

## v0.11.0 (2026-06-02)

### Feat

- **health**: privacy-tier split for /api/health response

### Refactor

- **auth**: purge phantom '/token' page references

## v0.10.0 (2026-06-02)

### BREAKING CHANGE

- the published Go binary is now named `qatlasd`. Both
its filename and the systemd / launchd / SCM service unit it installs
change. Edges running an old installation MUST migrate explicitly —
neither install-server.sh nor the binary's `service install` will
remove the old artefacts.

### Feat

- **auth**: env-loaded system PAT for ops paths without a user
- rename server binary qatlas-server → qatlasd
- **pat**: point user-lookup errors at 'users list'
- **cli**: add 'users list' for non-browser user discovery

## v0.9.2 (2026-06-01)

### Fix

- **sync**: listImageCounts must propagate list errors

## v0.9.1 (2026-06-01)

### Fix

- **cli**: make --version / version short-circuit before init

## v0.9.0 (2026-06-01)

### BREAKING CHANGE

- 之前匿名可调的读口（/api/pages、/api/search、
/api/stats、/api/lint、/api/wiki/sync/status、/api/graph/{stats,
schema,query}、/api/papers/stats、/api/papers/needs-mineru、
/api/papers/{id}/{resources,markdown,markdown/status}）现在要 401。
SPA 需同步更新调用时带 Authorization 头。

### Feat

- **auth**: collapse wiki/papers/graph reads behind PAT scopes
- **audit**: T10 notify webhook → Fluent Bit → qatlas-s3-events
- **audit**: T10 RustFS 写入留痕 — UA edge-label + Fluent Bit sink

### Fix

- **cli**: drop hardcoded operation timeouts in long-running commands
- **review**: mkdocs strict broken links + stale auth.go doc

## v0.8.1 (2026-05-30)

### Fix

- **mineru**: presign PDF via each edge's own public endpoint; drop QATLAS_MINERU_FETCH_ENDPOINT

## v0.8.0 (2026-05-30)

### Feat

- **share**: serve real PDF bytes via presign; MinerU pulls private direct link

## v0.7.2 (2026-05-30)

### Fix

- atomic mineru-claim under concurrency + async catalog schema bootstrap

## v0.7.1 (2026-05-30)

### Fix

- store mineru images before markdown so md presence implies images stored

## v0.7.0 (2026-05-30)

### BREAKING CHANGE

- QATLAS_S3_BUCKET replaced by QATLAS_S3_BUCKET_{PDF,MD,
IMAGES}; /api/_rustfs/event webhook and QATLAS_RUSTFS_EVENT_TOKEN
removed; paper catalog now requires Neo4j (NEO4J_URI).

### Feat

- move paper catalog to Neo4j, split object storage into 3 buckets

## v0.6.0 (2026-05-30)

### Feat

- **api**: generate OpenAPI spec + serve interactive /swagger UI

## v0.5.0 (2026-05-29)

### Feat

- **graph**: gate /api/graph/* with authGuard + graph:read scope
- **wiki**: Wikipedia-style refactor — concept-only model, source hiding, paper stats, math + wikilinks
- **mineru**: server-side silent markdown conversion with async status resource
- **parser**: restore DOI resolver package and wire into wiki enrich-doi

### Fix

- **wiki**: gate POST /api/wiki/sync/pull behind wiki:write scope
- **ci**: switch release.yml trigger from path-push to tag-push (qalgo style)

### Refactor

- **web**: remove dead in-page ingest button
- **qatlas**: remove client-side Neo4j coupling

## v0.4.0 (2026-05-29)

### Feat

- **paperindex**: Bootstrap subcommand + concurrent reads + helper consolidation

### Fix

- **server**: switch Version default sentinel from "0.2.2-go" to "dev"
- **paperindex**: drop json tag on blank-identifier struct field

### Refactor

- remove Python FastAPI server, consolidate on Go server
- rename Python package atlas → qatlas

## v0.3.0 (2026-05-29)

### Feat

- **paperindex**: Phase 2 — Upsert + CAS flush + RustFS event webhook

### Fix

- **docs**: unify wiki sync section to single #wiki-neo4j anchor
- **docs**: swap anchor order so wiki-neo4j wins
- **docs**: correct wiki-neo4j anchor in neo4j.md
- **docs**: restore landing-page nav + use built-in atom logo
- **docs**: add .gitkeep so docs/snippets/ exists on RTD checkout
- **rtd**: pin Python to 3.12

## v0.2.9 (2026-05-29)

### Fix

- **release**: use manylinux_2_28 + static-libstdc++ instead of manylinux2014

## v0.2.8 (2026-05-29)

### Feat

- **wiki**: add DOI enrichment fields to frontmatter schema
- **web**: vite dev proxies API + dev-only fake-auth bypass
- **web**: drop /token page, PAT covers all bearer use cases
- **server**: cache wiki catalog in memory for sub-ms reads

### Fix

- **release**: build linux binaries in manylinux2014 container, drop -static hack

## v0.2.7 (2026-05-29)

### Fix

- **release**: statically link Linux binaries to portably support older libstdc++

## v0.2.6 (2026-05-29)

### Fix

- **install**: drop curl|sh service-install chain, dash incompatible

## v0.2.5 (2026-05-29)

### Fix

- **server**: surface main.Version to PocketBase RootCmd --version output

## v0.2.4 (2026-05-28)

### Feat

- **release+docs**: matrix-build 4 platforms + install docs for both server and CLI
- **paperindex**: in-process Parquet+DuckDB catalog for fast metadata queries

## v0.2.3 (2026-05-28)

### Feat

- **release**: PyPI publishing + cross-compiled Go binaries + /install-server.sh
- **server**: race-safe uploads + concurrency/stability hardening
- **objstore**: dual-endpoint S3 client for split internal/public presign
- **go-server**: rename binary to qatlas-server + add `service install` subcommand
- **go-server**: storage prune CLI + versioned-delete IAM perms
- **test,ci**: per-target tokens in QATLAS_SERVER_TARGETS for active-active
- **go-server**: sha256 dedup + qatlas-managed bucket versioning
- **client**: qatlas auth login/logout/status/token with per-host store
- **server**: qatlas-server pat CLI + default PAT rate-limit rules
- **go-server**: migrate-raw-to-s3 one-shot tool
- **go-server**: P15 objstore abstraction + S3/RustFS backend
- **web**: redirect-flow GitHub OAuth + relocate SPA embed to web/
- **go-server**: P14 GitHub-style fine-grained PAT scopes (casbin)
- **go-server**: P13 Personal Access Tokens
- **go-server**: P11 CLI bearer-token + e2e regression suite + sudoless deploy docs
- **go-server**: P9 gate write endpoints on auth (interim QATLAS_WRITE_TOKEN + PocketBase token)
- **go-server**: P8 SPA PocketBase OAuth login UI + bearer auth header
- **go-server**: P7-prep force tcp4 listener on IPv4 bind addr
- **go-server**: P6 embed React SPA into binary
- **go-server**: P5 papers + shares + mineru-claim
- **go-server**: P4 Neo4j graph endpoints
- **go-server**: P3 wiki + pages + stats + search + lint stub
- **go-server**: P2 GitHub OAuth provider auto-injection
- **go-server**: P1 PocketBase skeleton + config + minimal routes
- **contrib**: raw asset uploads + MinerU claim/lease workflow
- **paper-assets**: sharded RAW paths and resilient asset resolution

### Fix

- **service install**: refuse `sudo ... --mode user` upfront
- **service install**: sudo-aware home resolution for ReadWritePaths
- **pat**: close zero-expiry bypass, harden helpers, fix last_used_at race
- **go-server**: make tcp4-force opt-in via QATLAS_FORCE_TCP4
- **nightly-smoke**: require explicit scheme in QATLAS_SERVER_TARGETS
- 修复 token 暴露和图谱页面回归

### Refactor

- **go-server**: route papers/shares through objstore
- **go-server**: post-storage-refactor cleanup
- **go-server**: default storage paths outside git checkout (XDG + sibling)
- **go-server**: P12 drop QATLAS_WRITE_TOKEN backdoor, godotenv-based .env loading
- **server,cli**: enforce ff-only ingest + split CI into push/nightly
- **config**: introduce QATLAS_* env namespace with legacy aliases
- enforce server wiki content boundary
- move UI into web app

## v0.2.2 (2026-04-25)

### Added

- CLI bearer token support for authenticated server access.

### Changed

- Externalized wiki repository consumption and refreshed documentation around architecture, deployment, and development.

## 0.2.0

### Added

- Runtime code-version metadata (manifest under raw/data stores) and optional production guard for release tags.
- Release workflow aligned with the QuantumAlgorithm (`qalgo`) build and GitHub Release pattern: build artifacts, GitHub Release from `CHANGELOG.md` + generated notes (PyPI publish deferred).

## v0.2.1 (2026-04-24)

### Feat

- platform upgrade with runtime version metadata and release workflow
- add qatlas cli entrypoint
- **server**: 协作 API、ingest 异步化与 uv 标准打包
- Issue #18 - Web界面（Wiki浏览器 + 图可视化）
- Issue #18 - 分层式知识库架构（Wiki + Neo4j 双轨）
- Issue #8 - Validator - 电路验证器（Phase 1 最后一个模块）
- Issue #7 - Resource Estimator - 资源估计器
- Issue #6 - Code Generator - 代码生成器
- Issue #5 - Circuit Designer - 电路设计器
- Issue #4 - Algorithm Extractor - LLM 算法提取模块
- Phase 1 MVP - Paper Parser + Knowledge Graph Skeleton

### Fix

- **systemd**: 生成 unit 路径勿加引号；system 安装提示默认含 enable --now
- **server**: 默认监听 localhost:4200 而非 0.0.0.0:8000
- 修复 Extractor 模块 Bug，添加集成测试和 Demo
- Address QA review for PR #15
- Address QA review issues for PR #1

### Refactor

- unify raw asset handling and versioned arxiv ids
