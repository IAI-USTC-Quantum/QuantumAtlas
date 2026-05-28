# 贡献指南

欢迎给 QuantumAtlas 贡献——代码、文档、Wiki 内容、bug 报告都欢迎。

## 三种贡献路径

<div class="grid cards" markdown>

-   :material-code-tags:{ .lg .middle } **[贡献代码](#code)**

    ---

    改 Python client / Go server / React 前端。从 fork 到 PR 的完整流程。

-   :material-text-box-edit:{ .lg .middle } **[贡献文档](#docs)**

    ---

    改这份你正在看的 mkdocs site。本地预览 + RTD PR preview。

-   :material-notebook-edit:{ .lg .middle } **[贡献 Wiki 内容](#wiki-content)**

    ---

    在独立的 [QuantumAtlas-Wiki](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) repo 写算法 / 论文 / 原语页面。

</div>

## 通用约定

### Conventional Commits

所有 commit message 用 [Conventional Commits](https://www.conventionalcommits.org/) 格式：

```
<type>(<scope>): <subject>

[optional body]
[optional footer(s)]
```

常用 type：

| type | 用 |
|---|---|
| `feat` | 新功能 |
| `fix` | bug 修复 |
| `docs` | 文档（不影响代码）|
| `refactor` | 重构（不改行为）|
| `test` | 测试 |
| `chore` | 杂项 / 工具链 |
| `build` | build 配置 |
| `ci` | CI / release 流水线 |
| `perf` | 性能优化 |

例子：

```
feat(client): support --no-poll for qatlas mineru queue mode
fix(routes): preserve metadata sha256 on conditional PUT 412 retry
docs(deployment): add Caddy template for dual-endpoint RustFS
chore(deps): bump pocketbase to v0.38.2
```

**BREAKING CHANGE** 用 footer 标记，`!` 会让 commitizen 算成 major bump：

```
feat(api)!: rename /api/papers/upload to /api/papers/upload-pdf

BREAKING CHANGE: clients before 0.2.0 must update to use the new path.
```

### Commitizen

```bash
# 用 commitizen 交互式建 commit（不写错格式）
uv run cz commit

# Bump version + 更新 CHANGELOG.md + 打 tag
uv run cz bump
```

`cz bump` 跑完会：

1. 算下个版本号（按 commit type；feat 是 minor，fix 是 patch，feat! 是 major）
2. 改 `pyproject.toml` 的 `version`
3. 在 `CHANGELOG.md` 顶部插新版本段
4. `git commit + git tag`
5. **不会自动 push**——你 review 完 `git push --follow-tags`

push 上去后 [`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 自动 build + 发 PyPI + 发 GitHub Release。

---

## 贡献代码 { #code }

### 环境

```bash
# clone
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas

# 一次性同步全栈依赖（Python + npm + 前端 build + Go build）
pixi run build
# 或单独装 Python deps
uv sync
```

### 跑测试

```bash
# Python 测试
uv run pytest

# Go 测试（必须 CGO_ENABLED=0，详见 [Go 工具链]）
CGO_ENABLED=0 go test ./...

# 前端 build + type check
cd web && npm run build

# Lint Wiki（如果你改了 atlas/wiki/）
qatlas wiki lint
```

!!! warning "Go 必须 CGO_ENABLED=0"
    PocketBase 选用纯 Go 的 SQLite (`modernc.org/sqlite`) 是为了避免 cgo。pixi go env 默认 `CC=x86_64-conda-linux-gnu-cc` 但 conda gcc 不在 PATH，会让 `go vet ./...` 卡死。**永久 fix**：

    ```bash
    go env -w CGO_ENABLED=0
    ```

    写到 `~/.config/go/env`，再也不挂。

### 仓库结构

```
atlas/                 Python client + Wiki + 电路工具
internal/              Go server 内部包（route / auth / store / config）
cmd/qatlas-server/     Go server 入口 (main + cobra subcommands)
web/                   React SPA (Vite + TanStack Router)
examples/              可独立 demo
scripts/               运维脚本
tests/                 Python 测试
docs/                  这份文档
```

### Pull Request 流程

1. 在 GitHub 上 fork 仓库
2. 本地建分支：`git checkout -b feat/some-thing`
3. 改 + commit（按 Conventional Commits）
4. 跑相关测试 + 现有测试别 break
5. push 你 fork：`git push -u origin feat/some-thing`
6. 在 GitHub 网页发 PR 到 `IAI-USTC-Quantum/QuantumAtlas:main`
7. CI 跑（pytest + go test + 前端 build）
8. review + 修改
9. squash merge

### 添加新功能注意

- **client 新命令** → 在 `atlas/cli.py::COMMANDS` 加条目，新建 `atlas/client/<name>.py`
- **server 新 endpoint** → 在 `internal/routes/` 加 handler，wire 在 `cmd/qatlas-server/main.go::registerRoutes`
- **加 PAT scope** → 改 `internal/pat/scopes.go`（必须重新部署，**不可热加载**）
- **新 PocketBase migration** → 放 `pb_migrations/`，下次启动自动跑
- **前端新页面** → 在 `web/src/routes/` 加 file，TanStack Router 自动生成路由
- **文档** → 改 `docs/`（详见下面）

---

## 贡献文档 { #docs }

### 本地预览（推荐）

```bash
uv run --with-requirements docs/requirements.txt -- mkdocs serve
```

打开 <http://127.0.0.1:8000>。改 `.md` 立刻 hot reload。

### 文档结构

| 目录 | 写什么 |
|---|---|
| `docs/index.md` | 欢迎页 |
| `docs/getting-started.md` | 入门（不分子目录）|
| `docs/concepts/` | 架构 / 思想 |
| `docs/guides/` | "怎么做某件事" how-to |
| `docs/reference/` | 完整 API / CLI / 配置 ref |
| `docs/deployment/` | 部署运维 |
| `docs/about/` | 项目背景 |

每个子目录有自己的 `.pages` 文件控制侧栏 nav。

### Material 特性

可以用：

- :material-checkbox-marked: `!!! note/tip/warning/danger` admonitions
- :material-checkbox-marked: `=== "Tab"` 内容标签
- :material-checkbox-marked: Mermaid `\`\`\`mermaid` 流程图
- :material-checkbox-marked: KaTeX `$\LaTeX$` 公式
- :material-checkbox-marked: `<div class="grid cards" markdown>` 卡片网格
- :material-checkbox-marked: `:material-icon:` 图标

参考已有页面学语法。

### PR

文档 PR 跟代码 PR 同流程。RTD 会**自动 build preview**——PR 页面会出 `docs/readthedocs.org:quantum-atlas` 检查项，点 Details 看预览。

### 改 mkdocs config

改 `mkdocs.yml` 不需要重启 `mkdocs serve`——它自动 reload。

---

## 贡献 Wiki 内容 { #wiki-content }

Wiki 在独立 repo：<https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki>

完整模板和写作指南：[写 Wiki 页面](guides/write-wiki-pages.md)。

简版流程：

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki.git
cd QuantumAtlas-Wiki
git checkout -b add-grover

# 写 wiki/entities/primitives/prim-grover.md
qatlas wiki create prim-grover --title "Grover's Search" --type entity --category primitive

# 编辑文件
$EDITOR wiki/entities/primitives/prim-grover.md

# 本地 lint
qatlas wiki lint

# 提交
git add wiki/entities/primitives/prim-grover.md
git commit -m "feat: add prim-grover"
git push origin add-grover
# 在 GitHub 发 PR
```

合并到 main 后，触发 server 端 fast-forward pull：

```bash
curl -X POST https://quantum-atlas.ai/api/wiki/sync/pull
```

无需 auth——fast-forward only 没法搞破坏。

---

## Release 流程

仅 maintainer 关心。

1. 确认 CI 全绿（pytest + go test + 前端 build）
2. `uv run cz bump` 算下版本 + 改 pyproject + 改 CHANGELOG + tag
3. Review：`git show HEAD` / `git show --stat <tag>`
4. `git push --follow-tags`
5. [`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 自动：
    - Cross-compile 4 平台 binary（`linux/{amd64,arm64}` + `darwin/{amd64,arm64}`）
    - 发到 GitHub Release（含 SHA256 checksum）
    - PyPI 发 Python wheel + sdist
6. 验证：
    ```bash
    pip install --upgrade quantum-atlas
    curl https://quantum-atlas.ai/install-server.sh | sh -s -- --version vX.Y.Z
    ```

## 行为准则

按 [Contributor Covenant 2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/)。简言之：

- 尊重他人，假设善意
- 拒绝骚扰 / 歧视
- 有分歧用证据 + 论证，不是人身攻击

---

## 找不到答案

- 看 [FAQ](about/faq.md)
- 提 [GitHub issue](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues)
- 开 [Discussion](https://github.com/IAI-USTC-Quantum/QuantumAtlas/discussions)

谢谢贡献！
