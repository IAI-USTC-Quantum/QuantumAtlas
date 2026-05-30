# 环境变量参考

QuantumAtlas client 和 server **共享同一份 `.env`**，按字段角色不同各取所需。所有项目自有变量带 `QATLAS_` 前缀；第三方 SDK 标准名（`NEO4J_*` / `MINERU_*` / `OPENAI_*` / `ANTHROPIC_*`）保留原始命名。

> 完整模板：[`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)

## 角色矩阵速查

| 变量族 | client | server |
|---|---|---|
| `QATLAS_SERVER_URL` | ✅ 必填 | ✅ 用来生成 share URL |
| `QATLAS_TOKEN` | ✅ 写操作必填 | — |
| `QATLAS_INSECURE` | ✅ | — |
| `QATLAS_WIKI_DIR` | ✅（用本地 wiki 命令时）| ✅ |
| `MINERU_API_TOKEN` 等 `MINERU_*` | ✅（本地跑 mineru 时）| ✅（server-side ingest 用）|
| `QATLAS_RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` | — | ✅ |
| `QATLAS_SERVER_HOST` / `PORT` / `HTTP_ADDR` | — | ✅ |
| `NEO4J_*` | — | ✅ |
| `QATLAS_S3_*` | — | ✅ |
| `QATLAS_SHARE_*` | — | ✅ |
| `QATLAS_USER_HEADER` | — | ✅ |
| `GITHUB_CLIENT_ID` / `SECRET` | — | ✅ |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | — | ✅（LLM extraction）|

## Client + Shared

### `QATLAS_SERVER_URL`

- **Alias**: `PUBLIC_BASE_URL`（旧名）
- **必填（纯 client）**
- **格式**: 完整 URL，带 scheme（`https://quantum-atlas.ai`）
- **作用**: client 默认 `--base-url`；server 端用来生成 share URL prefix

### `QATLAS_TOKEN`

- **写操作必填**
- **格式**: bearer token 明文（PAT 以 `qat_` 开头；或 PocketBase JWT）
- **作用**: 所有 `qatlas` 命令的默认 Authorization
- **优先级**: `--token` flag > `QATLAS_TOKEN` env > `~/.config/qatlas/hosts.yml`

### `QATLAS_INSECURE`

- **格式**: `1` / `true` / `yes` 启用
- **作用**: client 跳过 TLS 证书校验，等价于 `--insecure`
- **何时用**: 远端用 self-signed cert（如 Caddy `tls internal` 或阿里云直 IP）

### `QATLAS_WIKI_DIR`

- **Alias**: `WIKI_DIR`
- **默认**: `<.env 所在目录>/../QuantumAtlas-Wiki`（兄弟 Git checkout）
- **作用**: client 的 `qatlas wiki list/show/search/lint` 跑在这个本地 repo 上；server 的 wiki 读 endpoint 也用这个路径

## Server: 存储路径

三者都默认到 XDG 数据目录：

| 变量 | Alias | 默认 |
|---|---|---|
| `QATLAS_RAW_DIR` | `RAW_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw` |
| `QATLAS_DATA_DIR` | `DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/data` |
| `QATLAS_PB_DATA_DIR` | `PB_DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/pb_data` |

- 想覆盖到挂载盘 / `/var/lib/`？显式赋绝对路径
- `QATLAS_PB_DATA_DIR` 被自动注入为 PocketBase `--dir=`——**不要**在 systemd `ExecStart` 里再硬写 `--dir=`
- 启用 S3 / RustFS 时 `RAW_DIR` 不再被读写（fallback 占位）

详见 [Migration: 存储布局](../deployment/migration-storage-layout.md)。

## Server: HTTP 绑定

| 变量 | Alias | 默认 |
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
| `QATLAS_S3_ENDPOINT` | ✅ | server↔RustFS 流量走的 endpoint，必含 scheme (`http://10.144.18.10:9000`)|
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

## Server: Share URL

| 变量 | Alias | 默认 | 作用 |
|---|---|---|---|
| `QATLAS_SHARE_ACCESS_TOKEN` | `SHARE_ACCESS_TOKEN` | — | 可选常驻 share token（永不过期，靠改 env 撤销）|
| `QATLAS_DEFAULT_SHARE_EXPIRES_IN` | `DEFAULT_SHARE_EXPIRES_IN` | 0 (不过期)| 创建 share 不指定 `expires_in` 时的默认 TTL（秒）|
| `QATLAS_USER_HEADER` | `USER_HEADER` | — | 反代注入的审计用户头名，如 `X-Token-Subject` |

## 第三方 SDK 标准名

### MinerU

| 变量 | 默认 | 作用 |
|---|---|---|
| `MINERU_API_TOKEN` | — | **必填**（client 跑 `qatlas mineru` 或 server 端 ingest）|
| `MINERU_API_BASE_URL` | `https://mineru.net` | 自部署 MinerU 实例时改 |
| `MINERU_MODEL_VERSION` | `vlm` | `vlm` / `pipeline` |
| `MINERU_LANGUAGE` | `ch` | 主语言 hint |
| `MINERU_IS_OCR` | `false` | 强制 OCR |
| `MINERU_ENABLE_FORMULA` | `true` | 公式识别 |
| `MINERU_ENABLE_TABLE` | `true` | 表格识别 |
| `MINERU_POLL_INTERVAL` | `3` | 轮询间隔（秒）|
| `MINERU_TIMEOUT` | `1800` | 单篇总超时（秒，30 分钟）|
| `QATLAS_MINERU_FETCH_ENDPOINT` | —（空=用 `QATLAS_S3_PUBLIC_ENDPOINT`）| 给 MinerU 拉取 PDF 用的 presign 公网 endpoint（含 scheme）。server 端静默转换时 MinerU **主动拉取** presigned PDF 直链；该直链必须被 MinerU 爬虫**可达且 TLS 受信**。RackNerd 自身公网入口（LE 证书 `https://raw.quantum-atlas.ai`）已受信 → 留空即可。Alibaba 自身公网入口是自签 `https://<ip>:9000`（MinerU 实测拒绝，报 `failed to read file`）→ 设为 MinerU 受信的 endpoint（如 `https://raw.quantum-atlas.ai`）。两 edge 共享同一 svcacct，任一 edge 签 raw.quantum-atlas.ai 直链 MinerU 都能拉。仅 server 端、S3 后端、MinerU 启用时有效。|

### LLM

| 变量 | 作用 |
|---|---|
| `OPENAI_API_KEY` | server 端 LLM extraction（当前 Go server 暂未接入，Python extractor 用）|
| `ANTHROPIC_API_KEY` | 同上 |

## Server 启动时 .env 解析

server 启动时按顺序找 `.env`：

1. `$QATLAS_DOTENV`（显式指定，systemd 推荐）
2. `./.env`（CWD）
3. 否则没有 .env，纯靠 process env

找到后用 `godotenv.Load(path)` 加载（**不覆盖已有 env 变量**）。

`.env` 所在目录被用作**相对路径锚点**：`WIKI_DIR=../QuantumAtlas-Wiki` 相对该目录解析，不是相对 CWD 或 systemd `WorkingDirectory`。

## 弃用的变量（不要再用）

| 旧名 | 状态 |
|---|---|
| `QATLAS_WRITE_TOKEN` | 已删——Phase-A 临时共享密钥，被 PocketBase auth 替代 |
| `QATLAS_SESSION_SECRET` | 已删 |
| `QATLAS_POCKETBASE_URL` | 已删——server 自带 PocketBase |
| `QATLAS_REQUIRE_RELEASE_TAG` | 已删——旧 FastAPI 的 release-tag 启动护栏 |
| `CLI_TOKEN_*` | 已删——更早的 token 字段族 |

设这些字段**没有效果**也**不报错**——纯 noop。新代码请用 PAT。
