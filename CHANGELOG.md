# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) during pre-1.0 development with Commitizen bump rules.

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
