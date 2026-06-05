# RAG 向量检索接入

RAG (Retrieval-Augmented Generation) 让 `qatlasd` 把 8 万+ arXiv 论文的 chunk 级语义检索暴露成 `/api/rag/*`。整套架构分**三个角色**，分别放在不同主机：

| Role | 进程 | 默认绑定 | 跑在哪 | 装什么 extras |
|---|---|---|---|---|
| **edge sidecar** | FastAPI `qatlas_rag.sidecar.app:app` | `127.0.0.1:8802` | 每个边缘节点（跟 `qatlasd` 同机或 mesh 邻居） | `uv sync --extra sidecar` |
| **embed worker** | FastAPI `qatlas_rag.embed.worker:app` | `0.0.0.0:8801` | 一台 GPU 主机（典型：RTX 5080 / sm_120） | `uv sync --extra embed` |
| **Qdrant** | docker `qdrant/qdrant:v1.12.4` | `:6333`/`:6334` | 任何能跑 docker 的机器（典型：WSL 内） | (docker, 不用装 Python deps) |

`qatlasd` Go server 只持有 **反向代理**（[`internal/routes/rag.go`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/internal/routes/rag.go)）—— 真业务逻辑在 sidecar 里。

## 两个开关都必须 ON 才会注册 `/api/rag/*`

```bash
# .env (server 端)
QATLAS_PAPER_ACCESS_ENABLED=true              # 部署方对外承担派生 paper bytes 重分发合规义务
QATLAS_RAG_SIDECAR_URL=http://127.0.0.1:8802  # 指向已起好的 sidecar
```

任一为 false / 空，`/api/rag/*` **不注册**（404，跟"无此 handler"不可区分）。公共 `quantum-atlas.ai`（RackNerd）保持 OFF；Alibaba 内部部署可以打开。

## 三种典型拓扑

| 形态 | sidecar / embed / Qdrant 放哪 | 适用 |
|---|---|---|
| **All-in-one 单机** | 同一台机器，sidecar `127.0.0.1:8802`、embed `127.0.0.1:8801`、Qdrant docker | 个人 dev、单边缘 |
| **GPU 隔离** | sidecar 跟 qatlasd 同机；embed 在独立 GPU 主机（mesh IP）；Qdrant 任放 | 生产单边缘 + 共享 GPU |
| **Multi-edge** | 每边缘各跑 sidecar；共用一台 GPU embed + 一个 Qdrant | 多区域生产 |

## 起 Qdrant（最简：docker compose）

模板：[`deploy/qdrant-compose.example.yaml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/deploy/qdrant-compose.example.yaml)。复制到目标机的目录里，准备 `.env`：

```bash
QDRANT_API_KEY=<32B-random>      # ingester 用
QDRANT_RO_KEY=<32B-random>       # sidecar 用（read-only）
```

```bash
docker compose -f qdrant-compose.example.yaml up -d
curl -fsSL -H "api-key: $QDRANT_API_KEY" http://localhost:6333/readyz
```

> ⚠️ WSL2 部署 Qdrant 的话需要 Windows host 上加 portproxy 把 `<mesh-ip>:{6333,6334}` 转给 WSL2 内的 docker，否则 mesh 邻居拿不到。模板见 [`rag/deploy/portproxy-qdrant-1810.ps1`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/rag/deploy/portproxy-qdrant-1810.ps1)。WSL2 mirrored 模式确认后这一跳可以省。

## 起 embed worker（GPU）

要求 GPU compute capability **≥ sm_120** + CUDA 12.8（RTX 5080 / Blackwell）。其他 GPU 改 `torch` index 路径即可（pyproject 的 `[tool.uv.sources]`）。

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas
cd QuantumAtlas
uv sync --extra embed                                       # 拉 torch+cu128 ~2 GB
uv run python -m rag.scripts.spike.phase1_gpu_smoke         # 必须先过这个 smoke
```

通过后用 systemd user unit（模板 [`rag/deploy/qatlas-rag-embed.service`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/rag/deploy/qatlas-rag-embed.service)）让 worker 后台跑：

```bash
cp rag/deploy/qatlas-rag-embed.service ~/.config/systemd/user/
$EDITOR ~/.config/systemd/user/qatlas-rag-embed.service       # 改 WorkingDirectory / ExecStart 绝对路径
systemctl --user daemon-reload
systemctl --user enable --now qatlas-rag-embed
curl -s http://localhost:8801/healthz
```

## 起 sidecar（每个边缘）

```bash
cd QuantumAtlas
uv sync --extra sidecar                                     # 无 torch，30s 装完
cp rag/.env.example rag/.env                                # 改 Qdrant / embed worker 地址
```

`rag/.env` 关键字段：

```bash
QATLAS_RAG_QDRANT_URL=http://<qdrant-host>:6333
QATLAS_RAG_QDRANT_API_KEY=<RO key, sidecar 只读>
QATLAS_RAG_EMBED_URL=http://<gpu-host>:8801
QATLAS_RAG_COLLECTION=qatlas_papers_v1
```

```bash
cp rag/deploy/qatlas-rag-sidecar.service ~/.config/systemd/user/
$EDITOR ~/.config/systemd/user/qatlas-rag-sidecar.service     # 改路径
systemctl --user daemon-reload
systemctl --user enable --now qatlas-rag-sidecar
curl -s http://127.0.0.1:8802/healthz
```

## 起 ingester（建索引）

只在做**初次 build** 或**增量 sync** 时跑，平时不常驻。

```bash
uv sync --extra ingest
$EDITOR rag/.env                                            # 补 S3 / RustFS 配置
uv run python -m rag.scripts.spike.full_build \
    --collection qatlas_papers_v1 --limit 100               # 先跑 100 篇 smoke
uv run python -m rag.scripts.spike.full_build \
    --collection qatlas_papers_v1                           # 全量
```

进度 / 失败重试 / SQLite 增量 manifest 见 [`rag/qatlas_rag/ingest/runner.py`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/rag/qatlas_rag/ingest/runner.py)。

## 配 qatlasd 启用反向代理

每边缘 `qatlasd` 的 `.env` 加：

```bash
QATLAS_PAPER_ACCESS_ENABLED=true
QATLAS_RAG_SIDECAR_URL=http://127.0.0.1:8802     # 本机 sidecar
# 或 mesh 邻居 sidecar:
# QATLAS_RAG_SIDECAR_URL=http://10.144.18.88:8802
```

```bash
systemctl restart qatlasd
journalctl -u qatlasd --since '1 min ago' | grep 'rag:'
# 期望: rag: enabled sidecar=http://127.0.0.1:8802
```

## 验证全链路

```bash
# 1. 匿名 healthz（SPA 拿这个决定要不要显示 toggle）
curl -s https://<your-edge>/api/rag/healthz
# {"status":"ok"}

# 2. 鉴权 search（需要 papers:read scope 的 PAT）
curl -s -X POST -H 'content-type: application/json' \
     -H "authorization: Bearer $PAT" \
     https://<your-edge>/api/rag/search \
     -d '{"query":"surface code","top_k":3,"rerank":true}' | jq .
# took_s ~ 0.7-1.0; results[].canonical / .snippet / .score
```

SPA 端进 `https://<your-edge>/zh/papers/search`，左侧 sidebar 应该出现「文章搜索」入口。

## 网络配置

- `papers:read` PAT scope 用同一套，没新增 `rag:read`
- sidecar 默认绑 `127.0.0.1` —— 暴露给 mesh 邻居要改 bind 地址（systemd unit ExecStart 加 `--host 10.x.x.x`）
- embed worker 绑 `0.0.0.0`，建议挂防火墙限制只接受 mesh 段；它跟 GPU 同机，任何能调 `/embed` 的都能跑你的卡
- Qdrant API key 分 rw（ingester）/ ro（sidecar）两套，sidecar **不要**给 rw key

## 关闭 / 卸载

```bash
# 关 endpoint（保持 sidecar 运行，UI 立刻看不到 toggle）
sed -i 's/QATLAS_RAG_SIDECAR_URL=.*//' .env
systemctl restart qatlasd

# 停 sidecar / embed
systemctl --user stop qatlas-rag-{sidecar,embed}
systemctl --user disable qatlas-rag-{sidecar,embed}

# 停 Qdrant（数据保留在 ./qdrant_storage/）
docker compose -f deploy/qdrant-compose.example.yaml down
```
