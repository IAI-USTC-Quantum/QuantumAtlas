# 环境变量参考

> **Server-only 字段** 见本页 [§Server](#server-存储路径) 起；**client 用 YAML 配置文件**，不是 .env。client 字段映射见 [§Client 配置文件](#client-qatlas-配置文件解析)，子命令见 [`qatlas config` reference](cli-qatlas.md#qatlas-config)。

QuantumAtlas server（Go `qatlasd` 二进制）通过 `.env` / process env 读配置；自 v0.16.0 起 client（Python `qatlas` CLI）已切换到 `~/.config/qatlas/config.yaml` YAML 格式 —— 两边**不再共享同一份 .env**。

server 端项目自有变量带 `QATLAS_` 前缀；第三方 SDK 标准名（`NEO4J_*` / `GITHUB_CLIENT_*`）保留原始命名。

> 完整 server 模板：[`.env.example`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.env.example)
> 客户端 YAML schema：见下方 [§Client](#client-qatlas-配置文件解析)

## 角色矩阵速查

| 变量族 | client（YAML） | server（.env） |
|---|---|---|
| `server.url` / `QATLAS_SERVER_URL` | ✅ 必填 | ✅ |
| `server.token` / `QATLAS_TOKEN` | ✅ 写操作必填 | — |
| `server.insecure` / `QATLAS_INSECURE` | ✅ | — |
| `wiki.dir` / `QATLAS_WIKI_DIR` | ✅（用本地 wiki 命令时）| ✅ |
| `mineru.*` / `MINERU_*` | ✅（本地跑 mineru 时）| — |
| `extractor.openai_api_key` / `OPENAI_API_KEY` | ✅（本地跑 extractor 时）| — |
| `extractor.anthropic_api_key` / `ANTHROPIC_API_KEY` | ✅（本地跑 extractor 时）| — |
| `QATLAS_RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` | — | ✅ |
| `QATLAS_SERVER_HOST` / `PORT` / `HTTP_ADDR` | — | ✅ |
| `NEO4J_*` | — | ✅ |
| `QATLAS_S3_*` | — | ✅ |
| `QATLAS_USER_HEADER` | — | ✅ |
| `GITHUB_CLIENT_ID` / `SECRET` | — | ✅ |
| `QATLAS_SYSTEM_PAT` / `_SCOPES` | — | ✅ |
| `QATLAS_EDGE_NAME` | — | ✅ |

> 上面"client (YAML)"列那几个 `QATLAS_*` / `MINERU_*` / `OPENAI_*` env var 名仍然有效（OS env 优先级最高），但**不会再出现在 server 的 `.env.example` 模板里** —— 它们的 canonical 配置入口是 `~/.config/qatlas/config.yaml`。

## Client + Shared

### `QATLAS_SERVER_URL`

- **Alias**: `PUBLIC_BASE_URL`（旧名，**⚠️ deprecated，v0.17.0 移除**）
- **必填（纯 client）**
- **格式**: 完整 URL，带 scheme（`https://atlas.example.com`）
- **作用**: client 默认 `--base-url`

### `QATLAS_TOKEN`

- **写操作必填**
- **格式**: bearer token 明文（PAT 以 `qat_` 开头；或 PocketBase JWT）
- **作用**: 所有 `qatlas` 命令的默认 Authorization
- **优先级**: `--token` flag > `QATLAS_TOKEN` env > `~/.config/qatlas/hosts.yml`

### `QATLAS_INSECURE`

- **格式**: `1` / `true` / `yes` 启用
- **作用**: client 跳过 TLS 证书校验，等价于 `--insecure`
- **何时用**: 远端用 self-signed cert（如 Caddy `tls internal` 或直 IP 入站）

### `QATLAS_WIKI_DIR`

- **Alias**: `WIKI_DIR`（**⚠️ deprecated，v0.17.0 移除**）
- **默认**: `<.env 所在目录>/../QuantumAtlas-Wiki`（兄弟 Git checkout）
- **作用**: client 的 `qatlas wiki list/show/search/lint` 跑在这个本地 repo 上；server 的 wiki 读 endpoint 也用这个路径

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

### MinerU（**纯 client 字段**，YAML 段 `mineru:`）

下面这组变量由 Python `qatlas/config.py` 在贡献者本地跑 `qatlas mineru` 时读取并转发给 MinerU API；Go server (`qatlasd`) **不读这些字段**。canonical 配置入口是 `~/.config/qatlas/config.yaml` 的 `mineru:` 段；env var 名仍然有效（OS env 优先级高于 YAML）。

| YAML key | env var | 默认 | 作用 |
|---|---|---|---|
| `mineru.api_token` | `MINERU_API_TOKEN` | — | **必填**（贡献者本地跑 `qatlas mineru` 时调 MinerU API 的 bearer）|
| `mineru.api_base_url` | `MINERU_API_BASE_URL` | `https://mineru.net` | 自部署 MinerU 实例时改 |
| `mineru.model_version` | `MINERU_MODEL_VERSION` | `vlm` | `vlm` / `pipeline` |
| `mineru.language` | `MINERU_LANGUAGE` | `ch` | 主语言 hint |
| `mineru.is_ocr` | `MINERU_IS_OCR` | `false` | 强制 OCR |
| `mineru.enable_formula` | `MINERU_ENABLE_FORMULA` | `true` | 公式识别 |
| `mineru.enable_table` | `MINERU_ENABLE_TABLE` | `true` | 表格识别 |
| `mineru.poll_interval` | `MINERU_POLL_INTERVAL` | `3` | 轮询间隔（秒）|
| `mineru.timeout` | `MINERU_TIMEOUT` | `1800` | 单篇总超时（秒，30 分钟）|

> 贡献者本地 `qatlas mineru` 流程会把自己机器上的 PDF 上传给 MinerU
> （contributor 拿自己的 MinerU 配额走完转换）。服务端**不**对外 serve PDF
> 字节，也**不**有以服务端身份代客户做 MinerU 转换的 endpoint——下游
> 拓扑里没有"MinerU 主动拉服务端 PDF"的链路，因此也无需为 MinerU 公网
> 可达性维护 RustFS public endpoint。`QATLAS_S3_PUBLIC_ENDPOINT` 的用途
> 是给已授权的内部工具签 presigned URL，与公开 MinerU 服务无关。

### LLM（**纯 client 字段**，YAML 段 `extractor:`）

| YAML key | env var | 作用 |
|---|---|---|
| `extractor.openai_api_key` | `OPENAI_API_KEY` | client 侧 `qatlas extractor` 用 OpenAI 模型抽取算法描述（`qatlas/extractor/llm_interface.py`）；qatlasd server 不读 |
| `extractor.anthropic_api_key` | `ANTHROPIC_API_KEY` | 同上但用 Anthropic 模型 |

> Extractor 是实验性 client 子命令——不跑 `qatlas extractor` 或用 `--no-extract` 时保持未设置即可。`OPENAI_API_KEY` / `ANTHROPIC_API_KEY` 沿用 SDK 标准名（不加 `QATLAS_` 前缀），同一份 yaml + 现有 OpenAI / Anthropic SDK 共用。

## Server (qatlasd) 启动时 .env 解析

server 启动时按顺序找 `.env`：

1. `$QATLAS_DOTENV`（显式指定，systemd 推荐）
2. `./.env`（CWD）
3. 否则没有 .env，纯靠 process env

找到后用 `godotenv.Load(path)` 加载（**不覆盖已有 env 变量**）。

`.env` 所在目录被用作**相对路径锚点**：`WIKI_DIR=../QuantumAtlas-Wiki` 相对该目录解析，不是相对 CWD 或 systemd `WorkingDirectory`。

## Client (qatlas) 配置文件解析

client 跟 server 不同：

- **不读 cwd `.env` / `config.yaml`**（跟 `gh` / `docker` / `kubectl` / `aws` 同款约定 —— user-level CLI 不能让任意 cwd 静默覆盖用户配置）
- **v0.16.0 起切到 YAML 格式**：`~/.config/qatlas/config.yaml`（legacy `.env` 仍工作但启动会 warn，下次 `qatlas config init` 自动迁移；v0.17.0 完全下线）

完整优先级（高 → 低）：

1. CLI flag（`--base-url` / `--token` / `--insecure` ...）
2. OS env var（`QATLAS_*` / `MINERU_*` / `OPENAI_*` / `ANTHROPIC_*` 直接 export）
3. `$QATLAS_CONFIG` 显式 YAML 路径（systemd / docker / k8s 场景）
4. `$QATLAS_DOTENV` 显式 .env 路径（legacy；⚠️ deprecated，v0.17.0 移除）
5. `~/.config/qatlas/config.yaml`（XDG，用 `qatlas config init/set` 管）
6. `~/.config/qatlas/.env`（legacy XDG；首次 `qatlas config init` 自动迁移到 yaml）
7. 内置 Field default

具体子命令见 [`qatlas config` reference](cli-qatlas.md#qatlas-config)。

### YAML schema

```yaml
# ~/.config/qatlas/config.yaml — generated by `qatlas config init`
server:
  url: https://atlas.example.com         # → QATLAS_SERVER_URL
  token: qat_...                         # → QATLAS_TOKEN（敏感，0600）
  insecure: false                        # → QATLAS_INSECURE

wiki:
  dir: ../QuantumAtlas-Wiki              # → QATLAS_WIKI_DIR

mineru:                                  # qatlas mineru 子命令用
  api_token: jwt_...                     # → MINERU_API_TOKEN
  api_base_url: https://mineru.net       # → MINERU_API_BASE_URL（可选）
  model_version: vlm                     # → MINERU_MODEL_VERSION（可选）
  # is_ocr / enable_formula / enable_table / poll_interval / timeout / language 等可选

extractor:                               # qatlas extractor 实验性子命令用
  openai_api_key: sk-...                 # → OPENAI_API_KEY
  anthropic_api_key: sk-ant-...          # → ANTHROPIC_API_KEY
```

完整 yaml ↔ env 映射表是 [`qatlas/config_yaml.py::YAML_TO_ENV`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/qatlas/config_yaml.py)。

设计要点：

- **嵌套 yaml key → env var 不简单 join**：server.url → `QATLAS_SERVER_URL`（不是 `QATLAS_SERVER_URL` 巧合凑对），mineru.api_token → `MINERU_API_TOKEN`（不是 `QATLAS_MINERU_*`，沿用 MinerU SDK 标准名），extractor.openai_api_key → `OPENAI_API_KEY`（OpenAI SDK 标准名）。映射表写死，缺失即报错。
- **`qatlas config set` 会重写整个文件，抹掉用户手写注释**（PyYAML round-trip 不保留注释，跟 gh/kubectl/k8s 行为一致）；要永久注释请编辑文件 + 不用 `qatlas config set`。文件顶部 header 也明确告知。
- **客户端 schema 比 server `.env` 窄**：只含 client 用得到的字段；server-only 字段（`NEO4J_*` / `QATLAS_S3_*` / `GITHUB_*` 等）**不能**用 `qatlas config set` 设，会被 typo guard 拒绝。这些字段在 server 的 `.env` / `qatlasd config init` 模板里维护。

### 一次性 override

跟 server 一样，临时切配置不用改文件：

```bash
# 用别的 yaml
QATLAS_CONFIG=/path/to/other.yaml qatlas upload pdf foo.pdf

# 单个字段 override（OS env 优先级高于文件）
QATLAS_TOKEN=qat_temp_token qatlas mineru arxiv-2501.12345

# 把全部文件 loading 关掉，只走 process env
QATLAS_SKIP_DOTENV=1 qatlas ...
```

## 弃用的变量（不要再用）

| 旧名 | 状态 |
|---|---|
| `QATLAS_WRITE_TOKEN` | 已删——Phase-A 临时共享密钥，被 PocketBase auth 替代 |
| `QATLAS_SESSION_SECRET` | 已删 |
| `QATLAS_POCKETBASE_URL` | 已删——server 自带 PocketBase |
| `QATLAS_REQUIRE_RELEASE_TAG` | 已删——旧 FastAPI 的 release-tag 启动护栏 |
| `CLI_TOKEN_*` | 已删——更早的 token 字段族 |
| `QATLAS_SERVER_DEBUG` | 从未被读过的幽灵字段；v0.16.0 从 `.env.example` 清理 |
| 无 `QATLAS_` 前缀的 alias（`WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` / `SERVER_HOST` / `SERVER_PORT` / `PUBLIC_BASE_URL` / `USER_HEADER`） | **v0.17.0 移除**——v0.16.0 起 server 启动会按字段 emit `slog.Warn`，给运维一个 minor 的迁移窗口；用对应的 `QATLAS_*` 名 |
| **client 侧 `.env` 格式 + `$QATLAS_DOTENV`** | **v0.16.0 deprecated，v0.17.0 移除**——v0.16.0 起客户端切到 `config.yaml`，旧文件首次 `qatlas config init` 自动迁移并重命名为 `.env.migrated-from-v0.16.0.<timestamp>`（不删，可回滚）。systemd / docker 单元里 `Environment=QATLAS_DOTENV=...` 临时仍工作但会 warn —— 切到 `QATLAS_CONFIG=path/to/config.yaml` |

设这些字段（已删类）**没有效果**也**不报错**——纯 noop。新代码请用 PAT。设标了 v0.17.0 移除的 alias 会**继续工作但启动会有 warn**，看到 warn 请尽快改 `.env`。

> `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` 仍然有效，但只用于 client 侧 `qatlas extractor` 实验性子命令；qatlasd server 从未读过它们。详见上方 [LLM](#llm-纯-client-字段) 段。
