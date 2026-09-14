# 贡献指南

欢迎给 QuantumAtlas 贡献——代码、文档、bug 报告都欢迎。

## 三种贡献路径

<div class="grid cards" markdown>

-   :material-code-tags:{ .lg .middle } **[贡献代码](#code)**

    ---

    本仓改 Go server / React 前端；Python CLI 请到 [qatlas-cli 独立仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。从 fork 到 PR 的完整流程。

-   :material-text-box-edit:{ .lg .middle } **[贡献文档](#docs)**

    ---

    改这份你正在看的 mkdocs site。本地预览 + RTD PR preview。

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
feat(routes): add paper metadata endpoint
fix(routes): preserve metadata sha256 on conditional PUT 412 retry
docs(deployment): add Caddy template for dual-endpoint RustFS
chore(deps): bump pocketbase to v0.38.2
```

**BREAKING CHANGE** 用 footer 或 type 后的 `!` 标记；版本调整由 maintainer 按发布对象审核：

```
feat(api)!: rename /api/papers/upload to /api/papers/upload-pdf

BREAKING CHANGE: clients before 0.2.0 must update to use the new path.
```

### 版本与发布边界

Conventional Commits 是提交约定，不会自动触发发布。服务端使用根目录 `VERSION` + `v<version>` tag；旧 PyPI 包仅保留一次性最终迁移版 `quantum-atlas 0.21.0`，使用独立 tag `quantum-atlas-v0.21.0`。不要再用 `cz bump` 驱动服务端或继续递增旧包版本。发布命令统一见下方 [Release 流程](#release)。

---

## 贡献代码 { #code }

### 环境

```bash
# clone
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas

# 一次性同步全栈依赖（Python + npm + 前端 build + Go build）
pixi run build
# 单独装主仓 Python 开发 / 测试工具
uv sync --locked --group dev
```

Python 开发依赖由 `pyproject.toml [dependency-groups].dev` 管理，不再使用 `quantum-atlas[dev]` 项目 extra，也不是最终迁移包的运行时依赖。主仓不含 `qatlas/` 客户端代码；安装主仓不会提供 `qatlas` 命令。需要服务端联调时单独安装 `qatlas-cli`；修改客户端代码、测试或发版请到 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。

### 跑测试

```bash
# 主仓 Python 工具 / 契约测试（不是 CLI 实现测试）
uv run --group dev pytest

# Go 测试（必须通过 pixi 跑，自带 cgo + 工具链）
pixi run test-go
# 或：pixi run -- go test ./internal/... ./cmd/...

# 前端 build + type check
cd web && npm run build
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
internal/              Go server 内部包（registry / search / ingest / objstore / auth / config）
cmd/qatlasd/           Go server 入口 (main + cobra subcommands)
web/                   React SPA (Vite + TanStack Router)
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

- **client 新命令** → 到 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)开发；本仓只保留服务端 API / 集成文档
- **server 新 endpoint** → 在 `internal/routes/` 加 handler，并在 `cmd/qatlasd/main.go::registerRoutes` 中注册
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
| `docs/concepts/` | 架构 / 思想（跨组件共享）|
| `docs/client/` | Python 客户端 `qatlas`：how-to + 客户端 CLI |
| `docs/server/` | Go 服务端 `qatlasd`：部署运维 + REST API + 服务端 CLI |
| `docs/reference/` | 跨组件数据格式 ref（env vars / arXiv ID）|
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

## 贡献 MinerU 额度 { #mineru-quota }

MinerU 给每个注册账号送 **5000 篇 / 天** 的免费 PDF→Markdown 解析配额。个人用户基本用不完，
catalog 里却永远有几千篇 PDF 在 `/api/papers/needs-mineru` 队列里等着。
把闲置配额挂给项目，就把这些 PDF 变成可被 catalog 检索 / 可被语义索引的 markdown——
**零代码贡献路径**。

（服务端自己的 token 池另有一套口径：qatlasd 的夜间批处理调度器**自限 4000 篇 / 天**，
给交互式流量留 ~1000 篇余量；`GET /markdown` 缓存未命中触发的单篇 on-demand 转换
**不计入**这 4000 篇，只与批处理共享同一上游 token 池——某 token 额度耗尽（-60018）
时冷却到次日零点，全部耗尽才 503。贡献者走的是**自己的**账号配额，与上述服务端
数字互不相干。）

完整使用指南、错误码分类、daily-limit 退避语义、claim 原子租约模型见
[用 MinerU 解析 PDF（贡献你的额度）](client/parse-with-mineru.md)。
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
[把 daemon 挂久一点](client/parse-with-mineru.md#把-daemon-挂久一点)。

---

## Release 流程 { #release }

仅 maintainer 操作。发布前先确认目标 commit 的 CI 全绿并 review 变更；**推送 tag 才是发布动作**，普通分支提交不会自动发布。不要使用 `--tags` / `--follow-tags` 顺带推送未经审核的 tag。

| 发布对象 | 版本来源 | 唯一对应 tag | 产物 |
|---|---|---|---|
| 服务端 `qatlasd` | 根目录 `VERSION`（当前 `0.34.0`） | `v<version>` | Go binaries、服务端 GitHub Release、Docker 镜像 |
| 旧 PyPI 包最终迁移版 | `pyproject.toml [project].version = "0.21.0"` | **仅 `quantum-atlas-v0.21.0`** | metadata-only wheel / sdist、独立迁移 GitHub Release、PyPI |
| 客户端 `qatlas-cli` | [独立仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli) | 由该仓库管理 | [PyPI `qatlas-cli`](https://pypi.org/project/qatlas-cli/) |

### 服务端发版

根目录 `VERSION` 是服务端版本唯一来源；最终迁移包的 `0.21.0` **不能写回** `VERSION`，此次退役不改变服务端 `0.34.0`。以后发布服务端时，先按需更新 `VERSION` 和服务端 changelog、提交并 review，再执行：

```bash
# 在已审核的 release commit 上；tag 必须与该 commit 的 VERSION 一致
SERVER_VERSION="$(tr -d '[:space:]' < VERSION)"
git tag -a "v${SERVER_VERSION}" -m "Release qatlasd ${SERVER_VERSION}"
git show --stat "v${SERVER_VERSION}"
# 确认后只推这个 tag
git push origin "refs/tags/v${SERVER_VERSION}"
```

[`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 校验 tag 与 `VERSION` 一致，构建服务端文档 / 前端、三个平台的 Go binary（`linux/{amd64,arm64}` + `darwin/arm64`），生成 GitHub Release / checksum / provenance，并发布服务端 Docker 镜像。**服务端 `v*` tag 不构建或发布 `quantum-atlas`，也不发布 `qatlas-cli`。**

完成后检查 Actions、GitHub Release 与镜像产物，并在测试环境验证 `qatlasd --version` 与健康检查；不要通过安装旧 PyPI 包验证服务端。

### 一次性最终迁移包发版

`quantum-atlas 0.21.0` 只保留退役说明和发行元数据，**不含 `qatlas` 模块、parser 库或任何 console entry，没有运行时依赖，也不通过依赖自动安装 `qatlas-cli`**。主仓残留 Python helpers 已退役，客户端用户按[迁移指南](getting-started.md#migrate-quantum-atlas)手动切换。

在已审核的退役 commit 上确认最终包版本、构建产物内容和迁移安装测试通过，且根目录 `VERSION` 仍为 `0.34.0`。只在准备正式发布这一次迁移版时执行：

```bash
git tag -a quantum-atlas-v0.21.0 -m "Retire quantum-atlas on PyPI at 0.21.0"
git show --stat quantum-atlas-v0.21.0
# 确认后仅推最终迁移 tag，不推服务端 tag
git push origin refs/tags/quantum-atlas-v0.21.0
```

此 tag 仍由 **`.github/workflows/release.yml`** 处理，但走独立 Python 构建 / 发布 job，不运行服务端 binary、文档或 Docker 构建。保留现有 PyPI Trusted Publisher 的 workflow filename **`release.yml`** 和 GitHub environment **`pypi`**（OIDC 身份不变）。迁移 GitHub Release 设置 **`make_latest: false`**，不抢占服务端 Latest，也不影响默认 `install-qatlasd.sh` 下载。

发布后核对 PyPI 项目页的迁移说明、wheel / sdist 确实无 Python 模块 / 命令入口 / `Requires-Dist`，且服务端 Latest 未变化。不要为重试修改已经发布的版本或移动已推送 tag；需要重试时在 Actions 针对同一最终 tag 重跑。旧包没有后续常规发版流程，CLI 后续开发和发版只在 `qatlas-cli` 仓库进行。

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
