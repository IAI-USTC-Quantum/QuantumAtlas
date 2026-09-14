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

Conventional Commits 是提交约定，不会自动触发发布。主仓只发布服务端，使用根目录 `VERSION` + `v<version>` tag；CLI 由 `qatlas-cli` 独立仓库维护和发版。旧 PyPI 包的最终迁移版 `quantum-atlas 0.21.0` 已发布，不再有后续版本或 main 上的发布入口。不要用 `cz bump` 驱动服务端或递增旧包版本。服务端发布命令见下方 [Release 流程](#release)。

---

## 贡献代码 { #code }

### 环境

```bash
# clone
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas

# 普通源码构建/测试不需要 Node、Sphinx 或 web/dist
CGO_ENABLED=0 go build -o build/qatlasd ./cmd/qatlasd
# 本地 dev 版本要启动 UI，请先执行下方「完整 UI 构建」
# 可选：剩余 Python 文档辅助脚本的格式化 / 类型检查工具
uv sync --locked --group dev
```

根目录 `pyproject.toml` 是仅用于开发环境、不分发 Python 包的 uv 项目：`[dependency-groups].dev` 只保留 Python 辅助脚本的开发工具，不再依赖 pytest。文档构建依赖仍分别由 `docsite/requirements.txt`（Sphinx）和 `docs/requirements.txt`（MkDocs）管理。`uv sync` 不构建或安装主仓为 Python 包。主仓不含 `qatlas/` 客户端代码；需要服务端联调时单独安装 `qatlas-cli`，修改客户端代码、测试或发版请到 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。

### 完整 UI 构建 { #full-ui-build }

Git **只保存源码**。`web/dist`、`web/public/doc`、`web/public/devdoc` 和根 `dist` 都是忽略的构建产物。普通 `go install ...@vX.Y.Z` 安装时不运行 npm；首次 `serve` 自动下载同一版本的 Release UI 并校验缓存。没有对应 Release 的 dev/伪版本不能自动选择其他版本。

开发/发布资源使用 `web/.node-version` 的 Node 版本、`web/package-lock.json` 和 `docsite/requirements.txt`；不要顺手升级依赖。Sphinx 仍生成两套站点，MkDocs 是独立文档体系。

```bash
# 先用你的 Node 版本管理器选择 web/.node-version 指定的版本
export SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD)
export TZ=UTC PYTHONHASHSEED=0
# 清理的都是可再生输出；在仓库根执行
rm -rf web/public/doc web/public/devdoc web/dist build/doctrees
uv run --with-requirements docsite/requirements.txt -- sphinx-build \
  -b html -d build/doctrees/public docsite web/public/doc
uv run --with-requirements docsite/requirements.txt -- sphinx-build \
  -b html -d build/doctrees/dev -t devdocs -D root_doc=dev/index \
  -D html_title="QuantumAtlas 开发文档" docsite web/public/devdoc
(cd web && npm ci && npm run build)
CGO_ENABLED=0 go build -tags embedui -o build/qatlasd ./cmd/qatlasd
# 在相同树上打包；与程序内嵌的资源内容完全一致
SERVER_VERSION=$(tr -d '[:space:]' < VERSION)
go run ./internal/cmd/uibundle -version "$SERVER_VERSION" -output build/ui
```

提交前只提交源码。CI 执行两次干净的完整构建、比较所有路径及字节，检查不得跟踪生成目录；不靠忽略 diff、dirty 发布或只比较现有文件来掩盖漂移。文档改完后也必须重新生成用于验收的 UI。开发文档会随公开 Release 发布，HTTP 管理员鉴权不是内容保密机制，不要放入凭据或私有部署信息。

### 跑测试

服务端、部署结构与安装器测试使用 Go：`tests/compose_test.go` 检查部署结构，`tests/e2e/` 包含本地 `httptest` 冒烟 fixture；真实生产检查位于带 `e2e` build tag 的 `production_smoke_test.go`。普通测试不会读取部署 `.env` 或访问线上服务。一次性旧包检查只保留在最终历史 tag，不再加入日常 CI。

```bash
# 部署结构 + 本地 HTTP fixture（离线，不启动 Docker/业务服务）
go test ./tests/...

# 完整 Go 测试与静态检查（不需要先构建 UI）
CGO_ENABLED=0 go test ./internal/... ./cmd/... ./web ./tests/...
CGO_ENABLED=0 go vet ./internal/... ./cmd/... ./web ./tests/...
# 完整 UI 构建之后，再测内嵌资源与下载包字节一致
CGO_ENABLED=0 go test -tags embedui ./web

# 只编译 e2e 路径并运行本地 fixture，仍不访问生产
go test -tags=e2e ./tests/e2e -run '^TestSmokeFixture' -count=1
```

生产冒烟只能由操作者显式启用，nightly workflow 沿用仓库 secret `QATLAS_SERVER_TARGETS`：逗号/换行分隔的 `URL[|insecure][|token=...][|token-env=NAME]`。不要把真实目标和 token 提交到 Git。授权健康详情需要 system PAT 或 session JWT，普通用户 PAT 不授予该层；未提供 token 时会明确跳过授权详情子项。可选 `QATLAS_EXPECTED_VERSION`（nightly 中为 repository variable）用于精确核对部署版本；未设置时只检查非空、非 dev，不代表已核对最新发布版。

```bash
# 仅在明确配置测试目标并获准访问后执行；缺目标会失败
# -count=1 禁止生产检查使用测试缓存
go test -tags=e2e ./tests/e2e -count=1 -timeout=10m
```

当前主仓不再依赖旧 DuckDB/cgo 路径，正式发布使用 `CGO_ENABLED=0`。无需为运行上述测试修改全局 `go env`；Sphinx/MkDocs 仍只是文档构建工具。

`.github/scripts` 的少量 CI 专用 Python 归档/发布门禁脚本另用标准库 fixture 检查：`python3 -m unittest discover -s .github/scripts -p 'test_*.py'`。它们不需要 pytest，也不是服务端 Python 包或新的构建框架。

### 仓库结构

```
internal/              Go server 内部包（registry / search / ingest / objstore / auth / config）
cmd/qatlasd/           Go server 入口 (main + cobra subcommands)
web/                   React SPA (Vite + TanStack Router)
scripts/               运维脚本
tests/                 Go 部署结构测试、离线 HTTP fixture 与显式启用的生产冒烟
docs/                  这份文档
```

### Pull Request 流程

1. 在 GitHub 上 fork 仓库
2. 本地建分支：`git checkout -b feat/some-thing`
3. 改 + commit（按 Conventional Commits）
4. 跑相关测试 + 现有测试别 break
5. push 你 fork：`git push -u origin feat/some-thing`
6. 在 GitHub 网页发 PR 到 `IAI-USTC-Quantum/QuantumAtlas:main`
7. CI 跑（Go 单元/部署/离线 fixture 测试 + 前端 build）
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

[`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 校验 SemVer tag 与 `VERSION` 一致，并调用同一 SHA 的 Go CI。固定 GoReleaser **v2.18.1**，默认命名的三个平台归档（`linux/{amd64,arm64}` + `darwin/arm64`）、`qatlasd_<version>_web.zip` 与默认 `*_checksums.txt` 进入 draft；checksum 和 attestation 覆盖归档/UI 包。原生 runner 只下载、校验、解包并运行 `--version`，不重新编译。Docker 继续单独发布并验证 `linux/amd64` 镜像，复用同一 UI 包。全部通过才公开 Release，仅稳定版更新 Latest 和镜像 latest；已公开同 tag 拒绝重新上传。**不构建或发布旧 Python 包，也不发布独立 `qatlas-cli`。**

本地验证（先完成上面的完整 UI 构建和打包）：

```bash
goreleaser check
goreleaser release --snapshot --clean # 使用 v2.18.1；不发布
```

Snapshot 版本由 GoReleaser 生成，不等于 `VERSION`；真实 tag 与 runtime 版本在正式流程中精确核对。未获发布授权前，不推 tag、不改生产。新归档格式从未来新版本启用，不能重发 `v0.34.0`；首次迁移安装器必须从同一新 tag 的仓库路径取，不要使用旧服务返回的安装脚本。

**双发布边界**：Git tag 推送后 Go 模块可能已经可安装，但 draft UI 附件对普通用户尚不可见；此时首次 `serve` 明确失败，发布完成后重试即可。必须在打 tag 前验证源码可编译、UI 可重建。GitHub/GHCR 不是原子事务，失败时分别报告 draft/镜像状态，不移动 tag 或自动升级生产。

完成后检查 Actions、GitHub Release 与镜像产物，并在测试环境验证 `qatlasd --version` 与健康检查；不要通过安装旧 PyPI 包验证服务端。

### 旧 PyPI 包退役记录

[`quantum-atlas 0.21.0` 最终迁移版](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/tag/quantum-atlas-v0.21.0) 已发布到 PyPI 和 GitHub。它只含退役说明和发行元数据，不含 Python 模块、parser 库、命令入口或运行时依赖，也不会自动安装 `qatlas-cli`。用户仍可按[迁移指南](getting-started.md#migrate-quantum-atlas)手动切换，保留已有配置。

固定历史 tag [`quantum-atlas-v0.21.0`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0) 保留发布时的包元数据、一次性检查器、测试及 workflow，供追溯和审计；不要移动或覆盖该 tag。main 已移除这些一次性工具与旧包发布入口，根目录 `pyproject.toml` 只管理不分发的开发环境，不维护旧包版本或构建后端。**没有后续旧包发版流程，也不会再发布新的 `quantum-atlas` 版本。**

旧包最终发布未改变服务端 `VERSION` 或 GitHub Latest（分别为 `0.34.0`、`v0.34.0`）；服务端继续按上面的常规流程发布，CLI 后续开发和发版只在 `qatlas-cli` 仓库进行。

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
