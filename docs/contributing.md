# 贡献指南

欢迎给 QuantumAtlas 贡献——代码、文档、Wiki 内容、bug 报告都欢迎。

## 四种贡献路径

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

-   :material-server-network:{ .lg .middle } **[贡献 MinerU 额度](#mineru-quota)**

    ---

    挂 `qatlas contrib mineru --watch` 把自家 MinerU 账号每天 5000 篇的免费配额导给 catalog，
    把待解析队列里的 PDF 转成 markdown。零代码贡献路径。

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
feat(client): support --no-poll for qatlas contrib mineru queue mode
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

我们用 [Commitizen](https://commitizen-tools.github.io/commitizen/) 自动算下个版本号 + 写 CHANGELOG + 打 tag。配置在 `pyproject.toml [tool.commitizen]`：

```toml
[tool.commitizen]
name = "cz_conventional_commits"
tag_format = "v$version"
version_scheme = "pep440"           # PEP 440 版本号格式（支持 0.12.0a1 / 0.12.0.dev3 等 Python 标记）
version_provider = "pep621"         # 从 [project] version 字段读写（不是 [tool.poetry]）
update_changelog_on_bump = true
major_version_zero = true           # 0.x 期间 feat 也只 bump minor
annotated_tag = true                # 创建 annotated tag（默认 lightweight，git push --follow-tags 不推 lightweight）
```

`version_provider = "pep621"` 这条意思是 cz **读写 `pyproject.toml` 顶层 `[project] version`**——PEP 621 标准位置。`uv` 不参与，因为 `uv.lock` 里的 self-package version 字段在工程上无人依赖（详见下"为什么 uv.lock 不用同步"）。

```bash
# 写 commit 不会格式（type / scope / subject）
uv run cz commit

# 算下个版本 + 改 pyproject + 改 CHANGELOG + commit + tag（一条命令搞定）
uv run cz bump
```

`cz bump` 默认行为：

1. 按 git log 算下个版本号（feat → minor，fix → patch，`feat!` / `BREAKING CHANGE` → major；0.x 期间 feat 仍 minor）
2. 改 `pyproject.toml [project] version` + 在 `CHANGELOG.md` 顶部插新版本段
3. **`git commit -a` —— 卷入所有 modified tracked files**，不只 pyproject + CHANGELOG。源码 `commitizen/commands/bump.py::Bump._get_commit_args` 永远返回 `["-a"]`。所以 bump 前**必须先 `git stash` 工作树里所有未完成 WIP**，或者把它们 commit 完再 bump，否则 bump commit 会卷进 unrelated 改动 + commit message 失实
4. `git commit -m "bump: version <旧> → <新>"` + `git tag v<新>`
5. **不 push**——你 review 完手动 push

**release.yml 只在 `git push origin v<X.Y.Z>`（push tag）时触发**。不 push tag 就不会发版。push tag 后 [`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 自动 build wheel/sdist + 3 个平台 Go binary + 发 PyPI + 发 GitHub Release + 签 SLSA attestation。

#### 标准发版流程（推荐 — 跟社区主流一致）

```bash
# 1. 本地跑全部 CI mirror，全绿再 bump
pixi run vet \
  && pixi run test-go \
  && pixi run build \
  && uv run pytest -m "not network and not e2e"

# 2. 算版本 + 改文件 + commit + tag 一条搞定
uv run cz bump

# 3. Review commit 和 tag 内容
git show HEAD                               # 看 bump commit diff
git show --stat $(git describe --tags --abbrev=0)  # 看 tag 指向

# 4. Push branch + tag（--follow-tags 同时推 main 和 annotated tag）
git push --follow-tags
```

!!! info "为什么必须先本地跑 CI mirror"

    CI 有三个独立 workflow（`go.yml` / `pytest.yml` / `release.yml`）平行跑。**`release.yml` 不跑 vet/pytest**，所以本地不跑 vet 就直接 push tag → release artifact 照常发了，但 `go.yml` 红着，得 push fix commit 才能消红——一次 release 留个红 badge 在 commit history 不好看。

!!! info "为什么 uv.lock 不用同步"

    cz 改 pyproject `[project] version` 后，`uv.lock` 里 `[[package]] name = "quantum-atlas"` 块的 `version` 字段会 stale 一拍。但：

    - `release.yml` 用 `python -m build`，**不读 uv.lock**
    - `pytest.yml` 用 `uv sync --frozen`，`--frozen` 检查 dep tree 但 **self-package 是 editable install，不参与 dep resolve**，不会失败
    - dev 运行时直接读 pyproject

    所以 uv.lock 里 self-version 字段过期**不影响任何 CI / build / runtime**，纯粹是 cosmetic。强迫症想清，bump 后单跑：

    ```bash
    uv lock && git add uv.lock && git commit --amend --no-edit && git tag -f v$(cz version --project)
    ```

    （`tag -f` 是因为 amend 改了 commit hash，原 tag 还指向旧 hash 需要重指。）

    uv.lock 也不能挂到 cz 的 `version_files` 自动更新——cz 单行 regex 替换，uv.lock 里几十个 dep 都有 `version = "..."` 行，迟早撞车（今天 0.12.0 不撞，明天 0.13.0 可能跟某 dep 撞）。

!!! warning "bump 出错怎么撤"

    tag **没** push 出去：

    ```bash
    git reset --soft HEAD~1   # 撤 commit 保留改动
    git tag -d v<n>           # 删本地 tag
    git reset HEAD            # unstage
    ```

    tag 已经 push 出去：

    ```bash
    git push origin :refs/tags/v<n>           # 删远端 tag
    git push origin main --force-with-lease   # force push 修正后的 main
    ```

    后者会让任何 fetch 过该 tag 的 client 看到不一致，能避免就避免——所以 push 前一定 `git show HEAD` review 一次。

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
TOKEN=$(qatlas config get token)  # 从 client yaml 读
curl -X POST https://quantum-atlas.ai/api/wiki/sync/pull \
    -H "Authorization: Bearer $TOKEN"
```

即使是 fast-forward only，它仍会在服务端跑 git + 重建缓存，因此和其它写口一样需要鉴权，防匿名滥用。

---

## 贡献 MinerU 额度 { #mineru-quota }

MinerU 给每个注册账号送 **5000 篇 / 天** 的免费 PDF→Markdown 解析配额。个人用户基本用不完，
catalog 里却永远有几千篇 PDF 在 `/api/papers/needs-mineru` 队列里等着。
把闲置配额挂给项目，就把这些 PDF 变成可全文搜索 / 可被 LLM 抽取 / 可被 wiki 引用的 markdown——
**零代码贡献路径**。

完整使用指南、错误码分类、daily-limit 退避语义、claim 原子租约模型见
[用 MinerU 解析 PDF（贡献你的额度）](guides/parse-with-mineru.md)。
最简流程：

```bash
# 1. PAT —— 浏览器登录 quantum-atlas.ai 后访问 /pat，勾 papers:write
qatlas auth login -s quantum-atlas.ai

# 2. MinerU JWT —— mineru.net 注册 → API 管理后台复制（eyJ... 开头）
#    无 value 触发隐藏粘贴框，JWT 不进 shell history / ps aux。
qatlas config set mineru_api_token

# 3. 挂着持续贡献。多人并发不会撞配额（每篇 30 分钟原子 claim）。
qatlas contrib mineru --watch
```

想跨终端 / 跨开关机持续跑（systemd unit、tmux、agent CLI 后台 shell 等方案）见指南里的
[把 daemon 挂久一点](guides/parse-with-mineru.md#把-daemon-挂久一点)。

---

## Release 流程

仅 maintainer 关心。

1. 确认 CI 全绿（pytest + go test + 前端 build）
2. `uv run cz bump` 算下版本 + 改 pyproject + 改 CHANGELOG + tag
3. Review：`git show HEAD` / `git show --stat <tag>`
4. `git push --follow-tags`
5. [`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 自动：
    - Cross-compile 3 平台 binary（`linux/{amd64,arm64}` + `darwin/arm64`；Intel Mac 故意不发，`macos-13` runner 太慢，详见 `release.yml::binary-build` 注释）
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
