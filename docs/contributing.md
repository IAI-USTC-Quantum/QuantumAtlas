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

### Commitizen 与发版

```bash
# 用 commitizen 交互式建 commit（不写错格式）
uv run cz commit

# Bump version + 更新 CHANGELOG.md + 打 tag（推荐 4 步法见下）
uv run cz bump
```

`cz bump` 默认行为：

1. 算下个版本号（按 commit type；feat 是 minor，fix 是 patch，feat! 是 major）
2. 改 `pyproject.toml` 的 `version`
3. 在 `CHANGELOG.md` 顶部插新版本段
4. `git commit + git tag`
5. **不会自动 push**——你 review 完 `git push --follow-tags`

**release.yml 只在 `git push origin v<X.Y.Z>`（push tag）时触发**，所以不 push tag 就不会发版。push tag 后 [`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 自动 build wheel/sdist + 4 个平台 Go binary + 发 PyPI + 发 GitHub Release。

!!! tip "推荐 4 步法（防 cz bump 把工作区脏文件卷进 bump commit）"

    多 agent / 多 PR 同时进行时，`cz bump` 默认会 `git add -A` 把工作区所有改动塞进 bump commit，污染 git blame + tag 范围。隔离工作区改动：

    ```bash
    # 0. 隔离 WIP（防 cz bump 卷进无关改动）
    git stash push --include-untracked -m "pre-bump WIP"

    # 1. cz bump --files-only → 只改 pyproject + CHANGELOG，不 commit、不 tag
    uv run cz bump --files-only --yes
    #    ↑↑↑ 看 cz 输出！它会说 "bumped to 0.X.Y"——这个版本号是接下来 tag 要用的。
    #    cz 按你的 commit 算 PATCH/MINOR/MAJOR，不一定是你预期的那个。

    # 2. 显式 add 三个文件（不要 git add -A）
    git add pyproject.toml CHANGELOG.md uv.lock

    # 3. commit + tag（VERSION 从 pyproject 读，防 typo）
    VERSION="$(python -c 'import tomllib; print(tomllib.load(open("pyproject.toml","rb"))["project"]["version"])')"
    git commit -m "bump: version <旧> → ${VERSION}"
    git tag "v${VERSION}"

    # 4. push main + push tag（push tag 才触发 release.yml）
    git push origin main
    git push origin "v${VERSION}"

    # 5. 恢复 WIP
    git stash pop
    ```

    bump commit 已经卷进无关文件、tag 没 push 出去时还能补救：

    ```bash
    git reset --soft HEAD~1   # 撤销 commit 但保留改动
    git tag -d v<n>           # 删本地 tag
    git reset HEAD            # unstage
    # 然后按 4 步法重做
    ```

    tag 已经 push 出去再撤销很麻烦（需要 `git push origin :refs/tags/v<n>` 删远端 tag + force-push 修正后的 main），能避免就避免。

!!! tip "bump 前必须本地跑 CI mirror（防 push 后 CI 红）"

    CI 有三个独立 workflow（`go.yml` / `pytest.yml` / `release.yml`）平行跑。**release.yml 不跑 vet/pytest**，所以不本地跑 vet 直接 push → release artifact 发了但 go.yml 红着，得 push fix commit 才能消红。bump 前先：

    ```bash
    pixi run vet \
      && pixi run test-go \
      && pixi run build \
      && uv run pytest -m "not network and not e2e"
    ```

    全绿再 bump。

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

# Go 测试（必须通过 pixi 跑，自带 cgo + 工具链）
pixi run test-go
# 或：pixi run -- go test ./internal/... ./cmd/...

# 前端 build + type check
cd web && npm run build

# Lint Wiki（如果你改了 atlas/wiki/）
qatlas wiki lint
```

!!! warning "Go 必须 CGO_ENABLED=1（2026-05 起）"
    自 paperindex 包引入 `marcboeker/go-duckdb` 后，**整个 qatlasd build 强制需要 cgo**（libduckdb 是 C++ 库）。`pixi run build/test-go/vet` 已经在 `[tool.pixi.activation.env]` 里 export `CGO_ENABLED=1`，直接用 pixi 就行。

    如果你想脱离 pixi 直接 `go build`，先确保用户级 env 不强制关 cgo：

    ```bash
    go env -u CGO_ENABLED   # 清掉 ~/.config/go/env 里 CGO_ENABLED=0（如果之前设过）
    # 或直接 go env -w CGO_ENABLED=1
    ```

    Conda gcc (`gxx` 包) 在 `pixi shell` 里在 PATH，但脱离 pixi 时不在，需要系统装 `gcc` 才能跑 cgo build。

### 仓库结构

```
atlas/                 Python client + Wiki + 电路工具
internal/              Go server 内部包（route / auth / store / config）
cmd/qatlasd/     Go server 入口 (main + cobra subcommands)
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
- **server 新 endpoint** → 在 `internal/routes/` 加 handler，wire 在 `cmd/qatlasd/main.go::registerRoutes`
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

合并到 main 后，触发 server 端 fast-forward pull（需 `wiki:write` scope 的 PAT 或 session token）：

```bash
curl -X POST https://quantum-atlas.ai/api/wiki/sync/pull \
    -H "Authorization: Bearer $QATLAS_TOKEN"
```

即使是 fast-forward only，它仍会在服务端跑 git + 重建缓存，因此和其它写口一样需要鉴权，防匿名滥用。

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
    curl https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version vX.Y.Z
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
