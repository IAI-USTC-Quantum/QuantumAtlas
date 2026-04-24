# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) during pre-1.0 development with Commitizen bump rules.

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
