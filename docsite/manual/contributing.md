# 贡献指南

```{admonition} 文档框架迁移说明
:class: important

本页已从 MkDocs 迁入 Sphinx/MyST。正文中的旧文档目录、MkDocs 双体系、插件与命令说明保留为迁移前记录，不再作为当前构建步骤：平台参考正文现在位于 `docsite/manual/`，由 `manual/contents.rst` 统一编入 Sphinx。新增或修改文档使用原生 MyST；当前完整构建与预览步骤以 [Go 原生开发入门](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/docsite/dev/development.rst) 为准。不要按下方历史说明重新启用 MkDocs、Material 插件或旧 hooks。
```

欢迎给 QuantumAtlas 贡献——代码、文档、bug 报告都欢迎。

## 三种贡献路径

- **[贡献代码](#贡献代码)**

  本仓改 Go server / React 前端；Python CLI 请到 [qatlas-cli 独立仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。从 fork 到 PR 的完整流程。

- **[贡献文档](#贡献文档)**

  改 `docsite/` 的 Sphinx/Furo 站点。本地严格构建后由现有 `/doc` 与 `/devdoc` 提供。

- **[贡献 MinerU 额度](#贡献-mineru-额度)**

  挂 `qatlas contrib mineru --watch` 把自家 MinerU 账号每天 5000 篇的免费配额导给 catalog，
  把待解析队列里的 PDF 转成 markdown。零代码贡献路径。

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

**Breaking change 必须在提交标题的 type/scope 后用 `!` 标记**，可用 `BREAKING CHANGE` footer 补充背景，但迁移步骤应写入对应文档。GoReleaser 默认正文只展示标题与 SHA、不展示 footer；版本调整由 maintainer 按发布对象审核：

```
feat(api)!: rename /api/papers/upload to /api/papers/upload-pdf

BREAKING CHANGE: clients before 0.2.0 must update to use the new path.
```

### 版本与发布边界

Conventional Commits 是提交约定，不会自动推导或 bump 版本，也不会触发发布。主仓只发布服务端，所有正式版本仅从 Git tag 派生：维护者人工选择 SemVer，完成待发源码的检查、提交与 review 后，在已审核 SHA 上创建 annotated `vX.Y.Z[-rc.N]`，只推这个 tag。无需根版本文件、版本文件 bump commit，或为版本专门改 Go 源码/版本字段；CLI 按 `qatlas-cli` 独立仓库的发布流程维护和发版。旧 PyPI 包的最终迁移版 `quantum-atlas 0.21.0` 已发布，不再有后续版本或 main 上的发布入口。服务端发布命令见下方 [Release 流程](#release-流程)。

---

<a id="code"></a>

## 贡献代码

### 环境

完整的中文 [Go 原生开发入门](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/docsite/dev/development.rst)覆盖隔离测试、完整 UI 和本地联调。下面命令在仓库根目录的 Bash 中执行。

- Go 工具链门槛**唯一取自 `go.mod` 的 `go` 指令**；CI 同样读取该文件，不另维护版本要求。普通 Go 构建和测试不需要 Node、Sphinx 或 UI 产物。
- 前端与完整分发资源使用 `web/.node-version` 指定的 Node、npm 和 `web/package-lock.json`。
- Python 仅用于独立文档工具及 CI 辅助脚本：Sphinx 用 `uv run --locked --script .github/scripts/build_docs.py`（uv 0.11.30、Python 3.12）。

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas.git
cd QuantumAtlas
go version
go mod download
CGO_ENABLED=0 go build -o build/qatlasd ./cmd/qatlasd
./build/qatlasd --version
# 本地 dev 版本要 serve，先执行下方「完整 UI 构建」，不能直接省略 embedui
```

主仓不再有根 `pyproject.toml`、`uv.lock`、`pixi.lock`，不执行 `uv sync` 或 `pip install .`，不引入 Makefile、Taskfile、Pixi 或新的构建框架。保留 Sphinx 和 CI Python 标准库 helper 不代表仍有 Python 应用。主仓不含 `qatlas/` 客户端代码；客户端开发、测试与发版请到 [qatlas-cli 独立仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)。

<a id="full-ui-build"></a>

### 完整 UI 构建

Git **只保存源码**。`web/dist`、`web/public/doc`、`web/public/devdoc`、根 `dist/` / `build/`、缓存和 ELF 可执行文件不提交 Git。Go 输出显式放在 `build/`，不要放到仓库根再强制 add。安装采用新格式且附件已公开的精确 tag：`go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@vX.Y.Z`（替换为实际 tag）时不运行 npm；首次 `serve` 自动下载同一 Release 的 UI ZIP 与 SHA256 清单，校验 SHA256、包内版本与完整性，缓存到 `os.UserCacheDir()/qatlas/ui/v<version>`，后续仍校验缓存。没有对应 Release 的 dev/伪版本不会回退 latest，必须完成 Sphinx 两站、npm 构建，再使用 `-tags embedui`；不要假定旧 `v0.34.0` 已有新格式 UI。

开发/发布资源使用 `web/.node-version` 的 Node 版本、`web/package-lock.json` 和文档脚本锁；不要顺手升级依赖。Sphinx 生成 `/doc` 与 `/devdoc` 两套站点。

```bash
# 先选择 web/.node-version 指定的 Node
python3 .github/scripts/docs_sources.py checkout --transport ssh
(cd web && npm ci)
export SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
export TZ=UTC PYTHONHASHSEED=0
# 清理的都是可再生输出；在仓库根执行
rm -rf web/public/doc web/public/devdoc web/dist build/doctrees web/node_modules/.tmp
uv run --locked --script .github/scripts/build_docs.py
# 打包环境及会加载的 .env 文件不得携带 VITE_DEV_API_PAT 等 token
(cd web && npm run build)
CGO_ENABLED=0 go build -tags embedui -o build/qatlasd ./cmd/qatlasd
# 无正式 tag 时，用仅限本地/CI 验证的临时 SemVer 打包；不是发布版本
UI_VERSION="0.0.0-ci.g$(git rev-parse HEAD)"
go run ./internal/cmd/uibundle -version "$UI_VERSION" -output build/ui
```

`uibundle -version` 必须显式传入 SemVer；临时标识包含完整 Git SHA，`g` 前缀避免全数字 SHA 成为带前导零的非法数值标识。它只用于本地/普通 branch、PR CI 的打包与恢复验证，不创建 tag、版本文件，也不发布或改变普通 Go 构建的运行时版本。若正式 `TAG` 已核验、checkout 干净且该 tag 指向的 commit 正是候选 SHA / 当前 `HEAD`，才可用 `UI_VERSION="${TAG#v}"` 打正式 UI 包；不得拿最近的旧 tag 冒充待发版本。

提交前只提交源码。CI 执行两次干净的完整构建、比较所有路径及字节，检查不得跟踪生成目录；不靠忽略 diff、dirty 发布或只比较现有文件来掩盖漂移。文档改完后也必须重新生成用于验收的 UI。开发文档会随公开 Release 发布，HTTP 管理员鉴权不是内容保密机制，不要放入凭据或私有部署信息。

### 跑测试

服务端、部署结构与安装器测试使用 Go：`tests/compose_test.go` 检查部署结构，`tests/e2e/` 包含本地 `httptest` 冒烟 fixture。PG/Fleet/MinerU/OpenAlex 真目标测试通过 `testutil.IntegrationEnabled` 编译期开关门控，需 `-tags integration` **及**原有显式环境目标/flag；混合文件中的离线测试仍默认运行。S3 文件已有 `integration` tag。真实生产检查位于独立 `e2e` tag 的 `production_smoke_test.go`，不能与普通 fixture 混为一谈。

即使默认测试已有门禁，仍要清除 `QATLAS_TEST_PG_DSN`、`TEST_DOWNLOADFLEET_DATABASE_URL`、`MINERU_LIVE_TEST`、`MINERU_API_TOKEN`、`QATLAS_TEST_LIVE`、`QATLAS_S3_TEST_*`、`QATLAS_SERVER_TARGETS` 及其引用的凭据；不加载部署 `.env`，不借用默认业务配置。以下 `env -i` 白名单只保留 PATH 与 Go 缓存，临时 HOME/XDG 隔离默认配置和数据。工具链/模块下载仍可能联网，但这些测试只使用离线数据或本地 HTTP fixture，不访问真实业务目标。

```bash
(
  set -euo pipefail
  TEST_HOME="$(mktemp -d)"
  trap 'rm -rf "$TEST_HOME"' EXIT
  env -i PATH="$PATH" HOME="$TEST_HOME" \
    XDG_CONFIG_HOME="$TEST_HOME/.config" \
    XDG_DATA_HOME="$TEST_HOME/.local/share" \
    XDG_STATE_HOME="$TEST_HOME/.local/state" \
    XDG_CACHE_HOME="$TEST_HOME/.cache" \
    GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" \
    GOMODCACHE="$(go env GOMODCACHE)" CGO_ENABLED=0 \
    bash -eu -c '
      go test ./internal/... ./cmd/... ./web ./tests/...
      go vet ./internal/... ./cmd/... ./web ./tests/...
      # 只检查 integration 编译，不运行真目标测试
      go test -tags=integration ./internal/... ./cmd/... ./web ./tests/... -run "^$"
      # e2e 只选本地 fixture；不可去掉 -run 后作为默认检查
      go test -tags=e2e ./tests/e2e -run "^TestSmokeFixture" -count=1
      # 已完成完整 UI 构建时，在这个隔离块内额外运行：
      # go test -tags embedui ./web ./cmd/qatlasd/...
    '
)
```

真实集成测试可能迁移、写入或清理数据库、修改 bucket、消耗外部服务额度；只在明确授权的可丢弃测试资源上单独开启，不以残留环境变量代替授权。

生产冒烟只能由操作者显式启用，nightly workflow 沿用仓库 secret `QATLAS_SERVER_TARGETS`：逗号/换行分隔的 `URL[|insecure][|token=...][|token-env=NAME]`。不要把真实目标和 token 提交到 Git。授权健康详情需要 system PAT 或 session JWT，普通用户 PAT 不授予该层；未提供 token 时会明确跳过授权详情子项。可选 `QATLAS_EXPECTED_VERSION`（nightly 中为 repository variable）用于精确核对部署版本；未设置时只检查非空、非 dev，不代表已核对最新发布版。

```bash
# 仅在明确配置测试目标并获准访问后执行；缺目标会失败
# -count=1 禁止生产检查使用测试缓存
go test -tags=e2e ./tests/e2e -count=1 -timeout=10m
```

当前主仓不再依赖旧 DuckDB/cgo 路径，正式发布使用 `CGO_ENABLED=0`。无需为运行上述测试修改全局 `go env`；Sphinx 仍只是文档构建工具。

`.github/scripts` 的少量 CI 专用 Python 资源校验脚本只用标准库 fixture；它们不需要 pytest 或 Sphinx requirements，也不是服务端 Python 包或新的构建框架。一次性旧包检查仅保留在历史 tag。

### 格式化与 OpenAPI

```bash
# 无输出表示格式符合要求；修复时改为 -w，并审核 diff
# 新建未跟踪文件请另外 gofmt -w path/to/new.go
git ls-files -z -- '*.go' | xargs -0 gofmt -l

# 直接使用 go.mod 登记的工具，无需额外任务封装或全局安装 swag
go tool swag init -g main.go -d ./cmd/qatlasd,./internal/routes \
  -o internal/apidocs --parseInternal --parseDepth 1
git diff -- internal/apidocs
git diff --exit-code -- internal/apidocs
python3 -m unittest discover -s .github/scripts -p 'test_*.py' -v
git diff --check
```

有意修改 API 时同步提交 `internal/apidocs` 的生成源码/spec；CI 检查生成后无漂移。`web/src/routeTree.gen.ts` 也按约定跟踪并由前端构建生成，这两类生成源码不属于禁止提交的分发目录。

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
7. CI 跑（gofmt、Go test/vet、integration 仅编译、OpenAPI、CI 标准库 fixture、Sphinx 两站与前端双干净构建）
8. review + 修改
9. squash merge

### 添加新功能注意

- **client 新命令** → 到 [qatlas-cli 仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli)开发；本仓只保留服务端 API / 集成文档
- **server 新 endpoint** → 在 `internal/routes/` 加 handler，并在 `cmd/qatlasd/main.go::registerRoutes` 中注册
- **加 PAT scope** → 改 `internal/pat/scopes.go`（必须重新部署，**不可热加载**）
- **数据库变更** → PostgreSQL goose migration 放 `internal/registry/migrations/`；PocketBase 集合变更参考对应领域包的 `migrations.go` 与 bootstrap hooks，勿误建不存在的统一 `pb_migrations/` 流程
- **前端新页面** → 在 `web/src/routes/` 加 file，TanStack Router 自动生成路由
- **文档** → 改 `docsite/`（用户指南、组件聚合与平台参考）或 `docsite/dev/`（开发文档）

---

<a id="docs"></a>

## 贡献文档

当前文档站是 **Sphinx + Furo**，不使用 MkDocs，也不发布到 Read the Docs。
线上入口是现有 qatlasd 的 `/doc`（使用文档）与 `/devdoc`（开发文档，管理员入口）。

### 本地构建

组件文档按 `docsite/components.lock.json` 的固定提交检出后再构建。需要 Git 能读取三个组件仓库；私有仓使用本机已有凭据。

```bash
python3 .github/scripts/docs_sources.py checkout --transport ssh
SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)" \
  uv run --locked --script .github/scripts/build_docs.py
python3 -m http.server 8765 --bind 127.0.0.1 --directory web/public
```

打开 <http://127.0.0.1:8765/doc/> 与 <http://127.0.0.1:8765/devdoc/dev/>。
不要只用 `file://` 打开首页，搜索依赖 HTTP。正式命令与隔离环境说明见开发文档。

### 文档结构

| 目录 | 写什么 |
|---|---|
| `docsite/index.rst`、`docsite/guide/` | 使用文档入口与操作指南 |
| `docsite/manual/` | 平台参考、运维、ADR（原 Markdown 站迁入的完整正文） |
| `docsite/dev/` | 开发/发布文档，只进入 `/devdoc` |
| `docsite/components.lock.json` | 聚合所用的组件文档提交 |

组件正文在各自仓库的 `docs/` 中维护；主仓构建时按锁定 SHA 收集，不在主仓复制第二份正文。
提示、折叠、Mermaid 使用 MyST 指令；目录用 `toctree`。不要重新启用 Material 插件或旧 MkDocs hooks。

### PR

文档 PR 跟代码 PR 同流程。GitHub Actions 的 `docs` workflow 会严格构建两站；合并到 `main` 后发布内容镜像，再用 `deploy/update-docs.sh` 更新现有文档目录。

---

<a id="mineru-quota"></a>

## 贡献 MinerU 额度

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

## Release 流程

仅 maintainer 操作。发布前先确认目标 commit 的 CI 全绿并 review 变更；**推送 tag 或手动在 tag 上运行 release workflow 都是发布动作**，普通分支提交不会自动发布。不要使用 `--tags` / `--follow-tags` 顺带推送未经审核的 tag。

| 发布对象 | 版本来源 | 唯一对应 tag | 产物 |
|---|---|---|---|
| 服务端 `qatlasd` | 唯一从 Git release tag 去 `v` 派生 | annotated `vX.Y.Z[-rc.N]` | Go binaries、服务端 GitHub Release、Docker 镜像 |
| 客户端 `qatlas-cli` | [独立仓库](https://github.com/IAI-USTC-Quantum/qatlas-cli) | 由该仓库管理 | [PyPI `qatlas-cli`](https://pypi.org/project/qatlas-cli/) |

### 服务端发版

主仓所有正式版本仅从 Git release tag 派生，不维护根版本文件，不需要版本文件 bump commit，也不为版本专门改 Go 源码/版本字段。最终迁移包的 `0.21.0` 属于已退役的独立发布线；这次流程调整不代表已发布新服务端版本，历史 `v0.34.0` 与旧 Python tag 均保持不变。后续发版由维护者执行：

1. 人工审核变更与兼容性，选择尚未发布的 SemVer（`MAJOR.MINOR.PATCH[-prerelease]`，例如 `0.35.0-rc.1`；不使用 PEP 440 或 `+build` 元数据）。提交类型不自动决定或 bump 版本。
2. 将必要迁移步骤写入对应功能/部署文档，完成待发布源码的检查、提交与 review；确认候选 commit 的 CI 全绿，记录已审核的完整 SHA。源码已就绪时，不另造只改版本的 commit。
3. 在该已审核 SHA 上创建 annotated `vX.Y.Z[-rc.N]` tag，核对指向后只推这个 tag，触发现有 `release.yml` + GoReleaser：

```bash
set -euo pipefail
TAG=vX.Y.Z # 替换为本次人工选定的未发布 tag，例如 v0.35.0-rc.1
REVIEWED_SHA="<已review且CI全绿的完整提交SHA>" # 必须替换；不是浮动分支名
# 当前 checkout 必须就是该候选提交，且所有待发源码已提交
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$REVIEWED_SHA"
git tag -a "$TAG" "$REVIEWED_SHA" -m "Release qatlasd ${TAG#v}"
git show --stat "$TAG"
test "$(git rev-parse "${TAG}^{commit}")" = "$REVIEWED_SHA"
```

检查上面的 tag 内容与指向，人工确认后只推这个 tag：

```bash
git push origin "refs/tags/$TAG"
```

[`release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 由 push tag `v*.*.*` 或手动选择 tag 运行触发：先复用同 SHA 的 `go.yml` checks，再调用 GoReleaser。正式 UI 包仍从精确 `release_tag` 去掉前导 `v` 派生版本，并检查 tag 所指提交 SHA = source SHA = `HEAD`；普通 branch/PR CI 使用 `0.0.0-ci.g<完整Git提交SHA>` 做打包和恢复验证，不创建 tag、版本文件，也不发布。

固定 GoReleaser **v2.18.1**，通过 `GORELEASER_CURRENT_TAG` 绑定真实触发 tag，不从最近旧 tag 或同 SHA 的其他 tag 猜测版本。发布校验由 GoReleaser 原生 Git/SemVer 检查与 preflight 接管；preflight 沿用默认只警告，不设置 `fail_on_error: true`。移除自定义 gate 不代表任意 tag 都能用于 Go 模块、UI 或 Docker；这些消费者仍有各自约束，推荐标准 `vX.Y.Z[-rc.N]`，不用 PEP 440 或 `+build` 标签。

GoReleaser 默认直接发布三个平台归档（`linux/{amd64,arm64}` + `darwin/arm64`）、`qatlasd_<version>_web.zip` 与默认 `*_checksums.txt`，保留 `prerelease: auto`。不再维护手动 draft/public/latest 状态机、独立 Docker/report job、三平台原生 smoke 或 GitHub attestation；发布不等待外部 smoke。GitHub Latest 交给 GoReleaser/平台默认处理，不另写晋升逻辑。**不构建或发布旧 Python 包，也不发布独立 `qatlas-cli`。**

Docker 发布改用 `dockers_v2`，默认 `linux/amd64` + `linux/arm64`，镜像为 `ghcr.io/iai-ustc-quantum/qatlasd`。`Dockerfile.goreleaser` 复用已内嵌 UI 的 GoReleaser 二进制，保留 distroless nonroot 与运行参数；原 `Dockerfile` 保留为本地源码构建路径。标签使用 `.Tag`、`.Version`；仅非 prerelease 且非 snapshot 时，在同一次镜像发布中更新 `latest`。这不是按版本大小排序，也不等待后续外部验证；RC 不更新 `latest`。真实多架构镜像仍需单独实际构建、拉取并验证运行。

**Release 正文唯一由 GoReleaser 生成**：`.goreleaser.yaml` 显式使用 `changelog.use: git`，按 previous..current tags 的 Git commit 标题和 SHA 生成正文及构建产物 `dist/CHANGELOG.md`。主仓不再维护或读取根 `CHANGELOG.md`，不另写手工 release notes、header/footer，也不覆盖生成正文。默认正文不包含 commit body/footer，因此 breaking change 必须在提交标题中用 `!` 等明确标记；迁移前置条件和步骤写入对应功能/部署文档供读者按提交追溯，不能只藏在 `BREAKING CHANGE` footer 中。升级时查阅 [GitHub Releases](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases) 及对应版本文档。现行和后续发版统一走此生成链，已经发布的历史 Release 与 Git tags 保持不变。

GoReleaser 的 `.goreleaser.yaml` 使用 `git.ignore_tags` **精确匹配**忽略 `quantum-atlas-v0.21.0`，不是 OSS glob；不移动或删除历史 tag。继续使用默认 archive/checksum names、flags 和 ldflags，tar.gz 中内嵌的 UI 与独立 ZIP 来自同一份验证过的资源树。

本地验证（先完成上面的完整 UI 构建和打包）：

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=docker # 使用 v2.18.1；不发布、不构建镜像
```

`--skip=docker` 跳过本地 buildx 镜像构建；否则 snapshot 虽不发布，仍需要 Docker/buildx 与基础镜像。离线运行还需预先缓存工具链与依赖，此命令不构成镜像验收。无正式 tag 时复用上面的临时 UI 包，GoReleaser 自行生成 snapshot 运行版本，不用于普通源码安装的 Release UI 发现。正式流程仍需核对 tag、运行版本及 UI 一致。未获发布授权前，不推 tag、不改生产。新归档格式从未来新版本启用，不能重发 `v0.34.0`；首次迁移安装器必须从同一新 tag 的仓库路径取，不要使用旧服务返回的安装脚本。

**发布边界**：Git tag 推送后 Go 模块可能已经可安装，不等待 GitHub/GHCR 完成；UI 附件尚不可下载时首次 `serve` 明确失败，可用后重试。必须在打 tag 前验证源码可编译、UI 可重建。移除旧保护后，不再承诺拒绝所有已公开 Release 重传；preflight 的警告也不是无副作用保证。GitHub/GHCR 不是原子事务，失败时分别核对 Release、附件、镜像和标签状态，再决定恢复方案，不盲目重跑、移动 tag 或自动升级生产。

完成后检查 Actions、GitHub Release 与实际双架构镜像产物，并在隔离测试环境验证 `qatlasd --version`、UI 与健康检查；这些外部验证不再是默认发布门禁，不要通过安装旧 PyPI 包验证服务端。

### 旧 PyPI 包退役记录

[`quantum-atlas 0.21.0` 最终迁移版](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/tag/quantum-atlas-v0.21.0) 已发布到 PyPI 和 GitHub。它只含退役说明和发行元数据，不含 Python 模块、parser 库、命令入口或运行时依赖，也不会自动安装 `qatlas-cli`。用户仍可按[迁移指南](getting-started.md#从旧包-quantum-atlas-迁移)手动切换，保留已有配置。

固定历史 tag [`quantum-atlas-v0.21.0`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/tree/quantum-atlas-v0.21.0) 保留发布时的包元数据、一次性检查器、测试及 workflow，供追溯和审计；不要移动、删除或覆盖该 tag。main 保留 `PYPI_README.md` 退役说明入口，但移除一次性工具、旧包发布入口和根 Python 项目/uv/Pixi 锁文件，不再维护旧包版本或构建后端。**没有后续旧包发版流程，也不会再发布新的 `quantum-atlas` 版本。**

旧包最终发布没有发布服务端或容器，当时服务端最新发行及 GitHub Latest 均保持为 `v0.34.0`；本次版本流程调整也不构成新发行或授权重发旧 tag。服务端后续按上面的 tag-only 流程发布，CLI 后续开发和发版只在 `qatlas-cli` 仓库进行。

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
