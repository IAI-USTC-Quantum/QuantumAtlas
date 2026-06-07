# RAG 向量检索接入

RAG (Retrieval-Augmented Generation) 让 `qatlasd` 把 8 万+ arXiv 论文的 chunk 级语义检索暴露成 `/api/rag/*`。架构两个角色：

| Role | 进程 | 默认绑定 | 跑在哪 |
|---|---|---|---|
| **qatlasd** (Go) | `internal/routes/rag.go` | `127.0.0.1:4200` | 每个边缘节点（既是 web server、也是 Qdrant gRPC client + embed worker HTTP caller） |
| **embed worker** (Python) | `qatlas_rag.embed.worker:app` | `0.0.0.0:8801` | 一台 GPU 主机（典型：RTX 5080 / sm_120），整个仓库**唯一保留的 Python 进程** |
| **Qdrant** (Rust) | docker `qdrant/qdrant:v1.12.4` | `:6333`/`:6334` | 任何能跑 docker 的机器（典型：WSL 内） |

> v0.20.0 起去掉了 Python sidecar 这一层 —— 之前 sidecar 做的所有事（hybrid query 构造、Qdrant 调用、rerank 编排、snippet 拼装）都搬进 `qatlasd` 的 Go handler 里了。前端没变化。

## 两个开关都必须 ON 才会注册 `/api/rag/*`

```bash
# .env (qatlasd 这边)
QATLAS_PAPER_ACCESS_ENABLED=true                # 部署方对外承担派生 paper bytes 重分发合规义务
QATLAS_RAG_QDRANT_URL=qdrant.internal:6334       # gRPC host:port，也可以是 http(s):// scheme
QATLAS_RAG_QDRANT_API_KEY=<read-only key>        # 可选；公网 Qdrant 必须
QATLAS_RAG_QDRANT_COLLECTION=qatlas_papers_v1    # 默认值，跟 ingester 对齐
QATLAS_RAG_EMBED_URL=http://embed.internal:8801
QATLAS_RAG_EMBED_TOKEN=<embed worker bearer>     # 跟 embed worker 的 QATLAS_RAG_EMBED_TOKEN 一致
```

任一字段为空，`/api/rag/*` **不注册**（404，跟"无此 handler"不可区分）。面向公网的实例通常保持 OFF，只在受控的内部部署打开。

## 起 Qdrant（最简：docker compose）

模板：[`deploy/qdrant-compose.example.yaml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/deploy/qdrant-compose.example.yaml)。复制到目标机的目录里，准备它自己的 `.env`：

```bash
QDRANT_API_KEY=<32B-random>      # ingester 用（rw）
QDRANT_RO_KEY=<32B-random>       # qatlasd 查询路径用（read-only）
```

```bash
docker compose -f qdrant-compose.example.yaml up -d
curl -fsSL -H "api-key: $QDRANT_API_KEY" http://localhost:6333/readyz
```

> ⚠️ WSL2 部署 Qdrant 的话需要 Windows host 上加 portproxy 把 `<mesh-ip>:{6333,6334}` 转给 WSL2 内的 docker，否则 mesh 邻居拿不到。模板见 [`rag/deploy/portproxy-qdrant-wsl.ps1`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/rag/deploy/portproxy-qdrant-wsl.ps1)。WSL2 mirrored 模式确认后这一跳可以省。

## 起 embed worker（GPU）

要求 GPU compute capability **≥ sm_120** + CUDA 12.8（RTX 5080 / Blackwell）。其他 GPU 改 `torch` index 路径即可（主 `pyproject.toml` 的 `[tool.uv.sources]`）。

```bash
git clone https://github.com/IAI-USTC-Quantum/QuantumAtlas
cd QuantumAtlas
uv sync --extra embed                                       # 拉 torch+cu128 ~2 GB
uv run python -m rag.scripts.spike.phase1_gpu_smoke         # 必须先过 GPU smoke
```

通过后用 systemd user unit（模板 [`rag/deploy/qatlas-rag-embed.service`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/rag/deploy/qatlas-rag-embed.service)）让 worker 后台跑：

```bash
cp rag/deploy/qatlas-rag-embed.service ~/.config/systemd/user/
$EDITOR ~/.config/systemd/user/qatlas-rag-embed.service       # 改 WorkingDirectory / ExecStart 绝对路径
echo 'QATLAS_RAG_EMBED_TOKEN=<some-secret>' >> ~/QuantumAtlas/rag/.env
systemctl --user daemon-reload
systemctl --user enable --now qatlas-rag-embed
curl -s http://localhost:8801/healthz
```

`QATLAS_RAG_EMBED_TOKEN` 这个值要跟 qatlasd 那边的 `QATLAS_RAG_EMBED_TOKEN` **完全一致**。

## 配 qatlasd

在每个想启用 RAG 的 qatlasd 主 `.env` 里加上文那一组 5 个变量，然后：

```bash
sudo systemctl restart qatlasd
journalctl -u qatlasd --since '30 sec ago' | grep 'rag:'
# 期望: rag: enabled qdrant=qdrant.internal:6334 collection=qatlas_papers_v1 embed=http://embed.internal:8801
```

## 验证全链路

```bash
# 1. 匿名 healthz（SPA 拿这个决定要不要显示文章搜索入口）
curl -s https://<your-edge>/api/rag/healthz
# {"status":"ok"}

# 2. 鉴权 search（需要 papers:read scope 的 PAT）
curl -s -X POST -H 'content-type: application/json' \
     -H "authorization: Bearer $PAT" \
     https://<your-edge>/api/rag/search \
     -d '{"query":"surface code","top_k":3,"rerank":true,"use_sparse":true}' | jq .
# took_s ~ 0.7-1.0; results[].canonical / .snippet / .score
```

SPA 端进 `https://<your-edge>/zh/papers/search`，左侧 sidebar 应该出现「文章搜索」入口。

## 网络配置

- `papers:read` PAT scope 用同一套，没新增 `rag:read`
- embed worker 绑 `0.0.0.0`，建议挂防火墙限制只接受 mesh 段；它跟 GPU 同机，任何能调 `/embed` 的都能跑你的卡
- Qdrant API key 分 rw（ingester）/ ro（qatlasd 查询）两套，qatlasd 用 ro key

## 关闭 / 卸载

```bash
# 关 endpoint（保持 embed worker / Qdrant 运行，UI 立刻看不到入口）
sed -i '/^QATLAS_RAG_/d' .env       # 或者只删 QDRANT_URL / EMBED_URL 中任一条
systemctl restart qatlasd

# 停 embed worker
systemctl --user disable --now qatlas-rag-embed

# 停 Qdrant（数据保留在 ./qdrant_storage/）
docker compose -f deploy/qdrant-compose.example.yaml down
```

## 增量重建索引（ingester）

> ⏳ ingester 部分（从 RustFS 拉 markdown → chunking → embed → Qdrant upsert）的 Go 实现还在 spike 阶段。当前 89k 索引由历史 Python ingester 跑出来的，存量数据不动；新论文增量同步流程定型后补这一段。
