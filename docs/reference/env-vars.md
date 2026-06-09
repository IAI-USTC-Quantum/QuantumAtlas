# 环境变量参考

> **本页只描述 server (`qatlasd`) 的 env / .env 字段**。client (`qatlas` Python CLI) 自 v0.17.0 起**不再读任何 env / .env**，所有配置写在平台原生 user-config 路径下的 `config.yaml`（Linux `~/.config/qatlas/`、macOS `~/Library/Application Support/qatlas/`、Windows `%APPDATA%\qatlas\`；首次运行自动创建），见 [`qatlas config` reference](cli-qatlas.md#qatlas-config)。

QuantumAtlas server（Go `qatlasd` 二进制）通过三入口读配置：

**CLI flag > OS env > `.env` 文件 > 内置 default**

server 端项目自有变量带 `QATLAS_` 前缀；第三方 SDK 标准名（`NEO4J_*` / `GITHUB_CLIENT_*`）保留原始命名。每个字段都有等价 CLI flag（除了 OAuth 4 字段，详见 [issue #6](https://github.com/IAI-USTC-Quantum/QuantumAtlas/issues/6)），完整 flag 列见 [cli-qatlasd.md §serve](cli-qatlasd.md#serve)。

> 完整 server `.env` 模板：[`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)

## 角色矩阵速查

| 字段 | client (YAML) | server (env / .env / flag) |
|---|---|---|
| `server_url` (client `config.yaml`) — "我要联系的 server" | ✅ 必填 | — |
| `QATLAS_PUBLIC_URL` (server env / flag) — "我对外公布的 canonical URL" | — | ✅ 必填 |
| `insecure` (client only) | ✅ | — |
| `wiki_dir` / `QATLAS_WIKI_DIR` | ✅（本地 wiki 命令）| ✅ |
| `mineru_*` / `MINERU_*` | ✅（本地跑 mineru）| ✅ 仅 self-hosted + 启用论文访问开关时 |
| `openai_api_key` / `anthropic_api_key` | ✅（本地跑 extractor）| — |
| `QATLAS_RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` | — | ✅ |
| `QATLAS_HTTP_ADDR` / `QATLAS_FORCE_TCP4` | — | ✅ |
| `NEO4J_*` | — | ✅ |
| `QATLAS_S3_*` | — | ✅ |
| `QATLAS_USER_HEADER` | — | ✅ |
| `QATLAS_PAPER_ACCESS_ENABLED` | — | ✅ self-hosted 可选 |
| `QATLAS_OPENALEX_MAILTO` | — | ✅（开了 PAPER_ACCESS 后要求填）|
| `QATLAS_ARXIV_FETCH_CONCURRENT` / `_RPS` | — | ✅ |
| `GITHUB_CLIENT_ID` / `SECRET` | — | ✅（只能走 env，无 CLI flag） |
| `QATLAS_SYSTEM_PAT` / `_SCOPES` | — | ✅ |
| `QATLAS_EDGE_NAME` | — | ✅ |

> **client 写入 hosts.yml，没有 `token:` YAML 字段**（v0.19.0 移除——它会静默盖住 hosts.yml 的所有 per-host token）。CI 路径用 `echo "$TOKEN" \| qatlas auth login -s <server> --with-token` 把 PAT 写进 hosts.yml。

> **没有 client/server 共用的 env 名了**（v0.19.0 起）。client 完全不读 env（v0.17.0 起所有 client 配置走 `~/.config/qatlas/config.yaml`，本表 `server_url` 那一列指的是 YAML 字段名，不是 env）。server 端 env 用 `QATLAS_PUBLIC_URL`——名字明确表达"我对外公布的"含义，跟 client 侧 YAML 的 `server_url:`（"我要联系的"）在概念上独立。

> 重要变化（v0.17.0+）：**client 端不再读任何 OS env**。如果之前在 shell 里 `export QATLAS_SERVER_URL=...` 给 client 用，现在那些 env 对 `qatlas` 不再生效——必须搬到 `~/.config/qatlas/config.yaml`（字段名小写化去前缀：`QATLAS_SERVER_URL` → `server_url`、`MINERU_API_TOKEN` → `mineru_api_tokens`（列表形式）等）。

## Server (qatlasd) 配置入口

### `QATLAS_PUBLIC_URL`

- **历史名**: `QATLAS_SERVER_URL` / `PUBLIC_BASE_URL`（v0.19.0 起服务端**不再读**）
- **格式**: 完整 URL，带 scheme（`https://atlas.example.com`）
- **作用**: server 自报的对外 canonical URL；用于构造 OAuth 回调、share link 等需要绝对 URL 的地方（反代场景下 server bind 在 localhost，无法从 request 推断对外 URL，必须显式告诉它）。改名是为了准确反映"我对外公布的 URL"语义

### `QATLAS_WIKI_DIR`

- **Alias**: `WIKI_DIR`（**⚠️ v0.17.0 移除**）
- **默认**: `<.env 所在目录>/../QuantumAtlas-Wiki`（兄弟 Git checkout）
- **作用**: server 的 wiki 读 endpoint 用这个路径

### `QATLAS_USER_HEADER`

- **Alias**: `USER_HEADER`（**⚠️ v0.17.0 移除**）
- **默认**: — （不启用 header-based 审计）
- **作用**: 反代注入的审计用户头名，如 `X-Token-Subject`（caddy-security 时代遗留，可与 PocketBase auth 并行）

## Server: 存储路径

三者都默认到 XDG 数据目录：

| 变量 | Alias（⚠️ v0.17.0 移除） | 默认 |
|---|---|---|
| `QATLAS_RAW_DIR` | `RAW_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw` |
| `QATLAS_DATA_DIR` | `DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data` |
| `QATLAS_PB_DATA_DIR` | `PB_DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data` |

- 想覆盖到挂载盘 / `/var/lib/`？显式赋绝对路径
- `QATLAS_PB_DATA_DIR` 被自动注入为 PocketBase `--dir=`——**不要**在 systemd `ExecStart` 里再硬写 `--dir=`
- **`QATLAS_RAW_DIR` 是 dev-only fallback**：启用 S3 / RustFS 时 server 完全不读它；生产部署强烈建议配 S3，把 LocalStore 留给 dev / CI

详见 [Migration: 存储布局](../deployment/migration-storage-layout.md)。

## Server: HTTP 绑定

| 变量 | Alias（⚠️ v0.17.0 移除） | 默认 |
|---|---|---|
| `QATLAS_HTTP_ADDR` | — | 用下面两个组装 |
| `QATLAS_SERVER_HOST` | `SERVER_HOST` | `127.0.0.1` |
| `QATLAS_SERVER_PORT` | `SERVER_PORT` | `4200` |
| `QATLAS_FORCE_TCP4` | — | `0`（off）—— 仅 WSL2 + Windows portproxy 场景启用 |

`--http=` flag 优先级更高。

## Server: PocketBase / OAuth

| 变量 | 必填 | 作用 |
|---|---|---|
| `GITHUB_CLIENT_ID` | OAuth 启用时必填 | GitHub OAuth App client id |
| `GITHUB_CLIENT_SECRET` | OAuth 启用时必填 | GitHub OAuth App secret |
| `QATLAS_ADMIN_GITHUB_LOGINS` | 否 | 逗号分隔 GitHub username 白名单（未来 admin 自动提权用，当前 handler 未实现）|

OAuth callback URL 必须填成 `https://<your-server>/api/oauth2-redirect`。

## Server: 论文访问开关 (self-hosted 可选)

QuantumAtlas qatlasd **默认不**通过 API 对外 serve PDF / Markdown 字节。
Self-hosted 部署在受控范围（私有团队、内部站点）可以启用对内下载——
**quantum-atlas.ai 等公开实例保持默认（关闭）**。

| 变量 | 默认 | 含义 |
|---|---|---|
| `QATLAS_PAPER_ACCESS_ENABLED` | `false` | 启用后：(1) 注册 `GET /api/papers/{id}/markdown` + `markdown/status` 端点（受 `papers:read` 保护）；(2) server 读下面的 server-side MinerU 字段；(3) `/markdown` 缓存未命中时按需触发 MinerU 转换（server 把 PDF 通过 presign URL 提供给配置的 MinerU 后端）。`false`（默认）时上述端点未注册（404），server 不读 MinerU 字段 |

启用前请阅 [License & Attribution · 论文访问开关](../about/license-and-attribution.md#论文访问开关-self-hosted)
——arxiv 论文版权归原作者，对外二次分发由部署方自负责任。
Contributor 流程（`qatlas contrib mineru` → `POST /api/papers/{id}/upload-mineru`）
与本开关**无关**，开关 OFF 时也照常工作。

### Server-side MinerU（仅当 `QATLAS_PAPER_ACCESS_ENABLED=true` 时生效）

启用论文访问开关后，server 在 `GET /api/papers/{id}/markdown` 缓存未命中时会
用下面这组**服务端**配置（独立于 contributor 端 `qatlas contrib mineru` 的 YAML 配置）
透明触发 MinerU 转换：

| 变量 | 默认 | 作用 |
|---|---|---|
| `MINERU_API_TOKENS` | — | **必填**（CSV：`tok-a,tok-b,...`；缺失或全空时 server-side conversion `Enabled() == false`，`/markdown` 缓存未命中时返回 503）|
| `MINERU_API_BASE_URL` | `https://mineru.net` | 自部署 MinerU 实例时改 |
| `MINERU_MODEL_VERSION` | `vlm` | `vlm` / `pipeline` |
| `MINERU_LANGUAGE` | `ch` | 主语言 hint |
| `MINERU_IS_OCR` | `false` | 强制 OCR |
| `MINERU_ENABLE_FORMULA` | `true` | 公式识别 |
| `MINERU_ENABLE_TABLE` | `true` | 表格识别 |
| `MINERU_POLL_INTERVAL` | `3.0` | 单 task 轮询间隔（秒）|
| `MINERU_TIMEOUT` | `1800` | 单篇总超时（秒，30 分钟）|
| `MINERU_MAX_CONCURRENT_JOBS` | `4` | 并发处理上限（必须 ≥ 1；超出阈值的请求排队，由 converter 调度。建议根据所配 token 数和 MinerU 单 key 配额拍）|

开关 `false` 时**这组字段全部被忽略**——server 不实例化 MinerU client，
也不会因为漏配而 fail-loud。

### Silent fetch from arxiv.org（仅当 `QATLAS_PAPER_ACCESS_ENABLED=true` 时生效）

启用论文访问开关后，server 在 `GET /api/papers/{id}/markdown` 或
`GET /api/papers/{id}/pdf` 缓存未命中时会**异步**从 arxiv.org 拉对应 PDF，
写入对象存储后再触发后续步骤（markdown 触发 MinerU；pdf 直接 serve）。
整个流程符合 Long-Running Operation 协议：

1. 首次 GET 立即返回 `202 Accepted` + `Operation-Location` + `Retry-After`。
2. Client poll `/markdown/status` 或 `/pdf/status` 拿到结构化进度
   （`state` / `phase` / `pdf_ready` / `md_ready` / `fetch.bytes_received` /
   `convert.stage` ...），side-effect-free。
3. `state == cached` 后重新 GET 拿字节（200）。

多并发同 id 请求被 server 内部去重为单次 fetch / convert，所有调用方
观察到同一份 Job snapshot。

OpenAlex DOI 解析（path 头匹配 `^10\.\d{4,9}/` 时自动触发）和 arxiv fetch
共用 `QATLAS_OPENALEX_MAILTO` 作为 polite-pool 联系邮箱；缺失时 DOI 端点
返回 503，silent fetch 仍可运行但 User-Agent 不带 mailto（不推荐）。

| 变量 | 默认 | 作用 |
|---|---|---|
| `QATLAS_OPENALEX_MAILTO` | — | OpenAlex polite-pool 联系邮箱；同时被 arxiv fetch User-Agent 共用。**缺失时 DOI 端点返回 503**（`detail: DOI resolution unavailable: QATLAS_OPENALEX_MAILTO is not configured`），日志中 emit WARN |
| `QATLAS_ARXIV_FETCH_CONCURRENT` | `2` | 并行 arxiv fetch 上限（与 `MINERU_MAX_CONCURRENT_JOBS` 独立——fetch 是 I/O bound，MinerU 是 API+GPU bound，两条管线互不阻塞）|
| `QATLAS_ARXIV_FETCH_RPS` | `0.33` | token-bucket 速率（req/s, 支持小数）。默认 ≈ 每 3 秒一次，配合 burst 2 严格满足 arxiv 「bulk_data#etiquette」要求。多 edge 共享同一公网 NAT 时应**调低**让聚合速率仍 ≤ 1/3s |

## Server: Neo4j

| 变量 | 必填 | 默认 |
|---|---|---|
| `NEO4J_URI` | Graph 启用时必填 | — |
| `NEO4J_USERNAME` / `NEO4J_USER`（alias）| ✅ | — |
| `NEO4J_PASSWORD` | ✅ | — |
| `NEO4J_DATABASE` | 否 | `neo4j` |

未配 → graph endpoint 返回 `{"error":"..."}` 200，`/api/health` 报 `neo4j: not_configured`，**不下拉聚合等级**。

## Server: S3 / RustFS（连接字段 + 三桶 all-or-nothing）

连接字段（endpoint + 双 key）**加上三个 asset bucket 必须同时填或同时不填**——半填启动直接报错。v0.7.0 起对象存储按 asset kind 拆成三个独立 bucket（`objstore.Router` 路由），旧的单桶 `QATLAS_S3_BUCKET` 已**废弃**（残留会让 server fail-loud 提示迁移）。

| 变量 | 必填 | 含义 |
|---|---|---|
| `QATLAS_S3_ENDPOINT` | ✅ | server↔RustFS 流量走的 endpoint，必含 scheme (`http://<rustfs-internal-host>:9000`)|
| `QATLAS_S3_BUCKET_PDF` | ✅ | PDF 桶（如 `qatlas-pdf`），object key = `<yymm>/<arxiv_id>.pdf` |
| `QATLAS_S3_BUCKET_MD` | ✅ | MinerU markdown 桶（如 `qatlas-md`）|
| `QATLAS_S3_BUCKET_IMAGES` | ✅ | 抽出图片桶（如 `qatlas-images`）|
| `QATLAS_S3_ACCESS_KEY_ID` | ✅ | svcacct access key（**不要用 root key**）|
| `QATLAS_S3_SECRET_ACCESS_KEY` | ✅ | svcacct secret |
| `QATLAS_S3_PUBLIC_ENDPOINT` | ❌（强烈建议）| client 端 presign URL 用的公网 host；留空 = 用 internal endpoint 签 |
| `QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT` | ❌ | OpenAlex snapshot 桶；仅 `openalex bootstrap` 用，不参与三桶 all-or-nothing |

启动 log 出三行 `raw store: S3 backend .../<bucket>` 各一桶确认启用；dual endpoint 模式额外有 `(presign via ...)`。`/api/health` 的 `rawstore` check 报 `backend: s3-router` + `buckets: [...]`。

> 注意：v0.7.0 删除了 RustFS notification webhook（`/api/_rustfs/event` + `QATLAS_RUSTFS_EVENT_TOKEN`）。应用对 bucket 独占写，catalog 由上传写路径直接同步进 Neo4j，无需外部事件回灌。

详见 [RustFS 部署](../deployment/rustfs.md)。

## Server: 写入留痕（T10）

| 变量 | 必填 | 默认 | 含义 |
|---|---|---|---|
| `QATLAS_EDGE_NAME` | ❌ | — | 这台 edge 的名字（如 `us-east` / `cn-shanghai`）；折进 S3 client UA `qatlasd/<ver>/<edge>`，让 RustFS notify 事件流里正规 server 写与直连 mc/boto3 一眼可分。**UA 可伪造，仅辅助标识，绝不用于鉴权** |

这是 qatlasd 端**唯一**与写入留痕相关的 env。sink 本身**不在我们的 binary / `.env` 里**——由一个通用、零后端约定的日志转发器（Fluent Bit）作为 sidecar 跑在 NAS 上 RustFS 旁边，接 RustFS notify webhook（per-bucket subscribe，5 个资产桶 PUT/DELETE 推到 sink）、写进 `qatlas-s3-events` 桶。sink 用的 svcacct key（`qatlas-s3-events-writer`）、桶名、订阅列表全在 NAS 侧 Fluent Bit / RustFS compose 配置里，与 server 解耦——这样 dumb 存储层不被我们演进中的后端约定绑死。判定主键是 SigV4 `accessKey`（不可伪造）。整套部署见 [RustFS 部署 · 写入留痕](../deployment/rustfs.md#写入留痕-audit-sink-t10)。

## Server: System PAT（运维兜底 bearer）

可选的、与 PocketBase 完全无关的 bearer token，**直接从 env 加载、永不落 pb_data**。给"pb_data 不可用 / 还没人登录 / CI 不想绑具体人"等运维兜底场景。完整设计见 [鉴权模型 § System PAT](../concepts/auth-model.md#system-pat)。

| 变量 | 必填 | 默认 | 含义 |
|---|---|---|---|
| `QATLAS_SYSTEM_PAT` | ❌ | unset（功能关闭） | 单个全局 bearer 的明文；HTTP 请求带 `Authorization: Bearer <这串>` 即过 authGuard。设了启动 log 会有 `system PAT enabled (length=N scopes=[...])` 一行（**不打明文**）|
| `QATLAS_SYSTEM_PAT_SCOPES` | ❌ | `*`（master，等价 session）| CSV 限定该 token 能调什么；词表跟 user PAT 一致，额外允许 `*`。少数运维想 least-privilege 时用，例如 `wiki:read,papers:read,graph:read`|

启动时长度 < 16 字符**直接 fatal**——防止有人填了 `secret` / 空格 / 之类 placeholder 上 prod。生成办法：

```bash
openssl rand -base64 32         # 推荐
python -c 'import secrets; print(secrets.token_urlsafe(32))'
uuidgen
```

前缀格式随意，不强制 `qats_` 之类。能读 .env 的人 = superuser-equivalent，但 .env 早就有 S3 / Neo4j / GitHub 同等敏感的 secret，新增 system PAT 不扩大现有攻击面。

## Server: 反代审计

| 变量 | Alias（⚠️ v0.17.0 移除） | 默认 | 作用 |
|---|---|---|---|
| `QATLAS_USER_HEADER` | `USER_HEADER` | — | 反代注入的审计用户头名，如 `X-Token-Subject` |

## 第三方 SDK 标准名

### MinerU（contributor client — `qatlas contrib mineru` 子命令读）

这组字段由 Python client (`qatlas/extractor/llm_interface.py` / `qatlas/client/mineru.py`) 在
contributor 本地跑 `qatlas contrib mineru` 时读取并转发给 MinerU API；走的是
contributor 自己的 MinerU 配额。**v0.17.0+ 只能放 `~/.config/qatlas/config.yaml`**，
不再支持 env / `MINERU_*` env var。

> server-side（qatlasd）启用论文访问开关后**也**会读 MinerU 字段，但走
> [独立的环境变量](#server-side-mineru仅当-qatlas_asset_downloads_enabledtrue-时生效)
> 而**不**读 contributor 的 YAML。两条路径互不影响：contributor 用自己的
> token 在自己机器上跑、把成品 upload 给 server；启用论文访问开关后 server
> 用部署方配置的 token 代客户端跑。

| YAML key | 默认 | 作用 |
|---|---|---|
| `mineru_api_tokens` | `[]` | **必填**（用户本地跑 `qatlas contrib mineru` 调 MinerU API 的 bearer JWT 池；CSV 字符串或 YAML 列表都接受）|
| `mineru_api_base_url` | `https://mineru.net` | 自部署 MinerU 实例时改 |
| `mineru_model_version` | `vlm` | `vlm` / `pipeline` |
| `mineru_language` | `ch` | 主语言 hint |
| `mineru_is_ocr` | `false` | 强制 OCR |
| `mineru_enable_formula` | `true` | 公式识别 |
| `mineru_enable_table` | `true` | 表格识别 |
| `mineru_poll_interval` | `3.0` | 轮询间隔（秒）|
| `mineru_timeout` | `1800` | 单篇总超时（秒，30 分钟）|

> 用户本地 `qatlas contrib mineru` 流程把自己机器上的 PDF 上传给 MinerU
> （contributor 拿自己的 MinerU 配额走完转换）。**默认部署的 qatlasd 不**
> 对外 serve PDF 字节，也**不**以服务端身份代客户做 MinerU 转换——
> contributor 流程是 server 获取 markdown 的唯一路径。
> Self-hosted 部署若启用 `QATLAS_PAPER_ACCESS_ENABLED`，则 server
> 会**额外**用自己配置的 `MINERU_API_TOKENS` 在 `GET /markdown` 缓存未命中时
> 透明触发转换；公开实例（quantum-atlas.ai）保持默认（关闭）。
> `QATLAS_S3_PUBLIC_ENDPOINT` 的用途是给已授权的内部工具签 presigned
> URL，与公开 MinerU 服务无关。

### LLM（client-only — 仅 `qatlas extractor` 子命令读）

| YAML key | 作用 |
|---|---|
| `openai_api_key` | client 侧 `qatlas extractor` 用 OpenAI 模型抽取算法描述（`qatlas/extractor/llm_interface.py`）；qatlasd server 不读 |
| `anthropic_api_key` | 同上但用 Anthropic 模型 |

> Extractor 是实验性 client 子命令——不跑 `qatlas extractor` 或用 `--no-extract` 时保持未设置即可。v0.17.0 起 SDK 标准 env 名（`OPENAI_API_KEY` / `ANTHROPIC_API_KEY`）**对 qatlas 不再有效**，只能写在 yaml。这是与 SDK 共享 env namespace 的取舍——qatlas 优先要"配置入口单一"，OpenAI / Anthropic SDK 自己仍读 env，但 qatlas client 不会再传 env 给它们，统一从 yaml 读 key 后显式 `api_key=` 注入。

## Client (`qatlas`) 配置（YAML-only，v0.17.0+）

client 现在**完全独立于 server**：

- **只读** 平台原生 user-config 路径下的 `config.yaml`：
  - **Linux**: `~/.config/qatlas/config.yaml`（honors `XDG_CONFIG_HOME`）
  - **macOS**: `~/Library/Application Support/qatlas/config.yaml`
  - **Windows**: `%APPDATA%\qatlas\config.yaml`
  - 由 [`platformdirs`](https://platformdirs.readthedocs.io/) 解析；不确定具体路径 → `qatlas config path`
- **首次跑任何 `qatlas <cmd>` 自动创建模板**——不用 `qatlas config init`
- **不**支持 CLI flag（`--base-url` / `--token` / `--insecure` 全删）
- **不**支持 OS env（`QATLAS_*` 等 env 对 client 不生效）
- **不**支持 `$QATLAS_DOTENV` / `$QATLAS_CONFIG`

完整优先级（高 → 低）：

1. 平台原生 user-config 路径下的 `config.yaml`（auto-created on first run；用 `qatlas config set/unset` 改）
2. 内置 Field default

要换文件位置 → 用平台标准 env（Linux `XDG_CONFIG_HOME`、Windows `APPDATA`；macOS 没标准 env，symlink 就好）。

具体子命令见 [`qatlas config` reference](cli-qatlas.md#qatlas-config)。

### YAML schema

**Flat snake_case** — 字段名一对一 derived from
[`ServerConfig.model_fields`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/qatlas/config.py)。
首次跑 `qatlas` 子命令时这个 yaml 模板会自动写到磁盘。

```yaml
# config.yaml — auto-created on first qatlas invocation (path per platform; see above)

# Server endpoint + auth
server_url: https://atlas.example.com
token: qat_...                          # sensitive, file is mode 0600
insecure: false

# Local Wiki repo (qatlas wiki list/show/lint/search)
wiki_dir: ../QuantumAtlas-Wiki

# MinerU (qatlas contrib mineru)
mineru_api_tokens: [jwt-a, jwt-b]
mineru_api_base_url: https://mineru.net
mineru_model_version: vlm
mineru_is_ocr: false
mineru_enable_formula: true
mineru_enable_table: true
mineru_poll_interval: 3.0
mineru_timeout: 1800
mineru_language: ch

# LLM extractor (qatlas extractor — third-party SDK names, no QATLAS_ prefix)
openai_api_key: sk-...
anthropic_api_key: sk-ant-...
```

设计要点：

- **扁平 schema**：所有字段在 yaml top level，不分 `server: / mineru: / extractor:` 嵌套——pydantic-settings 自带的 `YamlConfigSettingsSource` 直接 map 到 `ServerConfig` snake_case 字段，没有 hand-maintained 映射表。
- **第三方 SDK 标准名保留**：`mineru_api_tokens`（沿用 MinerU SDK）；`openai_api_key` / `anthropic_api_key`（OpenAI / Anthropic SDK 标准 env 等价名小写化）。
- **`qatlas config set` 重写整个文件**：PyYAML round-trip 不保留注释，跟 `gh` / `kubectl config set` 一致。要永久注释直接编辑文件不用 `set`。
- **schema 比 server `.env` 窄**：server-only 字段（`NEO4J_*` / `QATLAS_S3_*` / `GITHUB_*`）**不能**用 `qatlas config set` 设，会被 typo guard 拒绝。这些字段在 `qatlasd serve --neo4j-uri ...` flag 或 server 的 `.env` 里维护。

## 弃用的变量（不要再用）

| 旧名 | 状态 |
|---|---|
| `QATLAS_WRITE_TOKEN` | 已删——Phase-A 临时共享密钥，被 PocketBase auth 替代 |
| `QATLAS_SESSION_SECRET` | 已删 |
| `QATLAS_POCKETBASE_URL` | 已删——server 自带 PocketBase |
| `QATLAS_REQUIRE_RELEASE_TAG` | 已删——旧 FastAPI 的 release-tag 启动护栏 |
| `CLI_TOKEN_*` | 已删——更早的 token 字段族 |
| `QATLAS_SERVER_DEBUG` | 从未被读过的幽灵字段；v0.16.0 从 `.env.example` 清理 |
| 无 `QATLAS_` 前缀的 server alias（`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` / `SERVER_HOST` / `SERVER_PORT` / `USER_HEADER`） | **v0.17.0 移除**——用对应 `QATLAS_*` 名 |
| `PUBLIC_BASE_URL`（服务端）| **v0.19.0 移除**——服务端的"对外 canonical URL"改用 `QATLAS_PUBLIC_URL` |
| `QATLAS_SERVER_URL`（服务端）| **v0.19.0 重命名为 `QATLAS_PUBLIC_URL`**——服务端历史叫 `QATLAS_SERVER_URL` 但语义是"我对外公布的 canonical URL"，叫 `PUBLIC_URL` 更准确。Client 端从未读过 env，没有"client 的 QATLAS_SERVER_URL"这回事 |
| client 侧的所有 `QATLAS_TOKEN` / `QATLAS_INSECURE` 等 env | **v0.17.0 client 完全不读 env**——搬到 `~/.config/qatlas/config.yaml` |
| client 侧 `--base-url` / `--token` / `--insecure` CLI flag | **v0.17.0 移除**——搬到 config.yaml |
| client 侧 `qatlas auth login -s / --host` | **v0.19.0 重命名为 `-s` / `--server-url`**——跟其它工具（gh 的 `--hostname`）对齐，避免短形 `-H` 冲突 |
| client 侧 `qatlas config set token` / config.yaml `token:` 字段 | **v0.19.0 移除**——会静默盖住 hosts.yml 里所有 per-host token。改走 `echo "$T" \| qatlas auth login -s <host> --with-token`（写 hosts.yml） |
| client 侧 `--base-url` / `--token` / `--insecure` CLI flag | **v0.17.0 移除**——搬到 config.yaml |
| `$QATLAS_DOTENV` / `$QATLAS_CONFIG`（client 端） | **v0.17.0 移除**——client 不再有 dotenv / config 路径 override；用 `XDG_CONFIG_HOME=` 切位置 |
| `qatlas config init` 子命令 | **v0.17.0 移除**——首次跑任何 `qatlas` 子命令自动创建 |

设这些字段（已删类）**没有效果**也**不报错**——纯 noop。
