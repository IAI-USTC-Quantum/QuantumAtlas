# 贡献工作流

这份文档描述如何把内容贡献到 QuantumAtlas，包括两条并列的协作路径：

- **Raw 资产贡献**：把论文 PDF、解析 Markdown、元数据落到 `RAW_DIR`。
- **Wiki 协作**：在独立 Git 仓库里编辑知识页面，按需触发服务器拉取。

如果你想理解项目分层和设计动机，先看 [architecture.md](architecture.md)。如果你只想跑起来一个服务，看 [deployment.md](deployment.md)。本文档定位是「内容贡献的 how-to」。

---

## 0. 配置加载约定

服务端与所有 `qatlas` 客户端命令都通过 [pydantic-settings](https://docs.pydantic.dev/latest/concepts/pydantic_settings/) 从仓库根目录的 `.env` 自动读取配置（见 `atlas/server/config.py`）。

- 字段映射到环境变量名（如 `MINERU_API_TOKEN`、`WIKI_DIR`、`USER_HEADER`）。
- 已有的 OS 环境变量优先级高于 `.env`。
- 临时关闭 `.env` 加载：`QUANTUMATLAS_SKIP_DOTENV=1`。

**客户端 `.env` 只需要写自己用到的几项**，不需要写服务端字段：

```env
# 用于 qatlas 命令默认指向哪台服务器（也可以每次用 --base-url 显式传）
PUBLIC_BASE_URL=https://quantum-atlas.ai

# 仅在使用 qatlas mineru 本地解析时需要
MINERU_API_TOKEN=mn_xxxxx
# 其余 MINERU_* 字段都有合理默认值，按需覆盖
```

不需要在客户端写 `SERVER_HOST` / `SERVER_PORT` / `NEO4J_*` / `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` 这些纯服务端字段——它们在客户端 .env 里出现也无害，但毫无意义。

服务端的 `.env` 见 [deployment.md](deployment.md) 的「推荐的单机生产目录」段。

---

## 1. Raw 资产贡献的三条路径

`RAW_DIR` 是论文资产的 canonical store。文件名遵循 arXiv 规范并按 YYMM 分片：

| 风格 | 例子 | 存储路径 |
|---|---|---|
| 老式（pre-Apr 2007） | `quant-ph/9508027v1` | `raw/pdf/9508/9508027v1.pdf` |
| 新式（post-Apr 2007） | `2501.00010v1` | `raw/pdf/2501/2501.00010v1.pdf` |

`markdown/` / `json/` / `images/` 子目录采用同样的分片布局。版本号 `vN` 是**强制**的——所有 upload 端点都拒绝不带版本的 ID。

下面三条路径都会落到这一套布局。选哪条取决于谁出算力、谁出资产。

### Path A：服务器侧按 arXiv ID 抓取

最轻量。贡献者只需要给一个 arXiv ID，服务端负责抓 PDF、解析、（可选）跑 LLM 抽取。

```bash
qatlas ingest quant-ph/9508027 --no-extract --no-sync-neo4j
qatlas ingest 2501.00010 --parser mineru   # 用服务器 .env 里的 MINERU_API_TOKEN
```

`qatlas ingest` 会同步走默认的 ingest 流水线并轮询任务状态。完整选项：

```bash
qatlas ingest --help
```

关键选项：

| 选项 | 行为 |
|---|---|
| `--parser pymupdf` / `--parser mineru` | 选择解析器；`mineru` 需要服务端配置 `MINERU_API_TOKEN` |
| `--stop-after fetch/parse/extract/wiki/neo4j` | 在指定阶段后停止 |
| `--stages a,b,c` | 只跑明确列出的阶段，跳过的阶段如果有本地资产会被复用 |
| `--force-fetch` / `--force-parse` | 即使本地已有 PDF/Markdown 也强制重做 |
| `--mineru-no-cache` | 让 MinerU 绕过它自己的缓存 |
| `--no-poll` | 提交后立即返回，不等任务完成 |

任务状态可以单独查询：

```bash
qatlas ingest status TASK_ID
```

需要带上人工 review 过的抽取结果再继续，可以走：

```bash
qatlas ingest continue TASK_ID --reviewed-json reviewed.json
qatlas ingest reviewed ARXIV_ID --reviewed-json reviewed.json
```

底层端点：

- `POST /api/ingest/paper`
- `POST /api/ingest/{task_id}/continue`
- `POST /api/ingest/paper/reviewed-extraction`
- `GET /api/ingest/{task_id}` / `GET /api/ingest/stages` / `GET /api/ingests`

### Path B：鉴权用户直接上传 PDF（可附带元数据 JSON）

适用场景：贡献者本地已有 PDF（或 arXiv 不可达），希望直接把资产推到服务器。

```bash
qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf --metadata meta.json
qatlas upload pdf 2501.00010v1     --pdf paper.pdf --overwrite
```

对应端点 `POST /api/papers/{arxiv_id}/upload-pdf` 行为：

- arXiv ID 必须携带版本号 `vN`，否则 400。
- 对 PDF 做最基本的 `%PDF-` magic 校验，不通过 400 并清理临时文件。
- `metadata` 可选；若提供，必须是 UTF-8 JSON。
- 默认拒绝覆盖（409）；显式 `--overwrite` 才允许替换。
- 单文件大小上限：PDF 100 MiB，metadata 2 MiB。

上传 parsed markdown 用：

```bash
qatlas upload markdown 2501.00010v1 --markdown out.md --source mineru
```

对应端点 `POST /api/papers/{arxiv_id}/upload-markdown`：

- markdown 必须是 UTF-8。
- 单文件上限 25 MiB。
- `source` 是可选审计标签（如 `mineru`、`pymupdf`、`hand-edited`），不影响存储位置。

### Path C：本地跑 MinerU 后把解析结果推回云端

适用场景：贡献者愿意用**自己的** MinerU 配额（`MINERU_API_TOKEN` 写在自己电脑的 `.env`），服务器配置可以完全不带这个 token。

**前置条件**：要处理的 PDF 必须已经在服务器的 `RAW_DIR` 里（通过 Path A 或 Path B 进入）。`qatlas mineru` 不接受用户自带 URL——这是为了保证生成的 markdown 永远对应 raw 中已知的 PDF，不会产生孤儿数据。

```bash
# 队列模式：从服务器的“需要 MinerU”队列里取至多 --max 个还没人处理的论文，
# 逐个领取（claim）→ 跑 MinerU → 上传 markdown → 自动释放 claim。
qatlas mineru
qatlas mineru --max 20 --continue-on-error

# 单篇模式：处理指定 arxiv 论文（同样要求服务器已有 PDF）。
qatlas mineru quant-ph/9508027v1

# 只跑 MinerU 不上传（结果留在本地临时目录，claim 立刻释放）
qatlas mineru 2501.00010v1 --no-push
```

**并发模型（claim/lease）**：

- 想处理一篇 → `POST /api/papers/{arxiv_id}/mineru-claim` 申请一个短期租约（默认 30 分钟，最长 2 小时，可用 `--ttl-seconds` 调整）。
- 服务端原子地写 `DATA_DIR/mineru-claims/{key}.json`；如果已有未过期 claim → 409 + 现有租约元数据。
- 上传 markdown 成功 → 服务端自动删除 claim。
- 客户端异常中断 → 用 `DELETE /api/papers/{arxiv_id}/mineru-claim/{claim_id}` 主动释放（`qatlas mineru` 在 except 分支会自动调用）。
- 没释放也没事：lease 到期后会被下一个 claim 请求覆盖。

`qatlas mineru` 流程：

1. 从本地 `.env` 读 `MINERU_API_TOKEN` 等字段。
2. 没传 arxiv_id → `GET /api/papers/needs-mineru?limit=<max>` 取队列；传了就只处理那一篇。
3. 对每篇 → POST claim；服务端返回 `pdf_url`（指向服务器 share URL）+ `claim_id`。
4. 提交 MinerU → 按 `MINERU_POLL_INTERVAL` / `MINERU_TIMEOUT` 轮询。
5. 完成后下载 `full.md` → POST `/api/papers/{arxiv_id}/upload-markdown?source=mineru`。上传成功 = claim 自动释放。
6. 异常路径（MinerU 失败 / 上传失败 / Ctrl+C）→ DELETE claim 主动释放。

也可以纯手工：用任意解析器在本地生成 Markdown，再走 Path B 的 `qatlas upload markdown`（不涉及 claim 机制，但仍要求 PDF 先在 raw 里）。

---

## 2. 鉴权与审计

服务器自身不内置浏览器登录流程。生产部署通常由 Caddy 等反向代理在前面完成 OAuth / cookie / bearer 鉴权后，再把用户身份注入 HTTP 请求头。

涉及到的服务端配置：

- `USER_HEADER`：服务器读取的用户名 header 名（例如 `X-Forwarded-User`）。upload 端点会把这个值记到响应 `uploaded_by` 字段和日志里。
- 服务器自身不验证 token——它信任反代已经鉴权过。

客户端 CLI 调用时，把反代签发的 bearer token 加到 `Authorization` 头：

```bash
TOKEN=$(curl -s -b "AUTHP_ACCESS_TOKEN=$AUTHP_TOKEN" https://atlas.example/api/session/token \
        | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')

curl -X POST -H "Authorization: Bearer $TOKEN" \
  -F pdf=@paper.pdf -F metadata=@meta.json \
  "https://atlas.example/api/papers/quant-ph/9508027v1/upload-pdf?overwrite=true"
```

Web UI 的 Token 页面（`/token`）直接提供「Copy curl」按钮，复制出来的命令已经带好 `Authorization` 头。

具体反代配置（Caddy + caddy-security 的样例）在 [deployment.md](deployment.md)。

---

## 3. Wiki 协作：以 Git 仓库为主边界

Wiki 内容**不必经过服务器**就能更新。推荐做法是把 Wiki 仓库作为应用仓库的兄弟目录 checkout，所有人都直接对它 clone / branch / push / PR：

```text
~/work/
├── QuantumAtlas/          # 应用代码仓库
└── QuantumAtlas-Wiki/     # Wiki 内容仓库（任意人都可以 clone 编辑）
```

应用侧只需要在 `.env` 里指向这个 checkout：

```env
WIKI_DIR=../QuantumAtlas-Wiki
```

服务器上的 Wiki checkout 应当保持干净——它**只读**，且只接受 fast-forward 更新。当 Wiki 仓库有新 commit 被合入主分支后，任何能访问服务器 API 的客户端都可以触发服务器拉取，无需登录服务器 shell：

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  https://atlas.example/api/wiki/sync/pull
```

对应端点 `POST /api/wiki/sync/pull` 的动作是 `git fetch --prune` + `git pull --ff-only`：

- 本地有未提交改动 → 409。
- 不是 Git 仓库 → 409。
- 不能 fast-forward（远端有 force-push 或本地领先）→ 409。
- 远端不可达 / git 命令失败 → 502。

成功响应里会带 `old_commit` / `new_commit` / `changed` 字段。

当前状态可以通过 `GET /api/wiki/sync/status` 查看（只读本地 Git 信息，不访问远端）。如果服务器 checkout 不在 `main` / `master` 分支，状态会带 warning。

Wiki 页面本身的格式规范（页面类型、frontmatter schema、命名前缀、lint 错误码）见 [wiki-conventions.md](wiki-conventions.md)。

---

## 4. 推荐协作节奏

1. **摄入论文或资料**：按 Path A / B / C 之一把 raw 资产入库，保留证据链。
2. **整理 Wiki 页面**：在 `QuantumAtlas-Wiki` 仓库里 commit / PR / review，让分类、摘要、引用和状态可审阅。
3. **触发服务器同步**：调用 `POST /api/wiki/sync/pull`，再让稳定的 Wiki 页面同步到 Neo4j，用关系图做依赖发现和路径查询。Graph 始终是派生视图，不是另一份手工维护的 truth。
4. **下游使用**：从算法或原语继续生成实现，经过验证和资源估计后再进入下游。
