# qatlas_rag — embed worker (Python, GPU-only)

> v0.20.0 起，**只剩 embed worker 一个 Python 角色**。Qdrant 查询 + 写入两路都已经收编进 Go server (`qatlasd`)，前端不变。这个目录现在只装 bge-m3 + bge-reranker-v2-m3 在 GPU 上跑。

## 架构

```
浏览器 → /api/rag/search → qatlasd ─ HTTP ─→ embed worker (这里)
                              │                 │ bge-m3
                              │                 │ bge-reranker-v2-m3
                              │                 ▼
                              │               GPU
                              │
                              └─ gRPC ─→ Qdrant (docker)
```

qatlasd 自己当 Qdrant client + 自己调 embed worker，所有检索策略（hybrid 权重、rerank pool、score normalize、snippet 截取）用 Go 在 `internal/routes/rag.go` 实现。本目录跟检索策略**完全无关**。

## 跑起来

```bash
cd QuantumAtlas
uv sync --extra embed                    # 拉 torch+cu128 + FlagEmbedding (~2 GB)
uv run python -m rag.scripts.spike.phase1_gpu_smoke    # GPU smoke; 必须先过
uv run uvicorn qatlas_rag.embed.worker:app --host 0.0.0.0 --port 8801
```

生产用 systemd user unit 模板：[`deploy/qatlas-rag-embed.service`](./deploy/qatlas-rag-embed.service)。把 `WorkingDirectory` / `ExecStart` 改成你 clone 的绝对路径就能跑。

## API

| Method | Path | Body | 备注 |
|---|---|---|---|
| `GET` | `/healthz` | — | 无 token；coarse status 防 leak topology |
| `POST` | `/embed?lane=query\|build` | `{"texts": [...], "return_sparse": bool}` | bearer 认证 |
| `POST` | `/rerank?lane=query\|build` | `{"query": "...", "passages": [...]}` | bearer 认证 |

两个 lane（query / build）共享同一张 GPU 但 query 优先级高，避免 ingester 跑批时 query 被阻塞。详见 `qatlas_rag/embed/worker.py` 顶部注释。

## 配置

环境变量统一前缀 `QATLAS_RAG_`，见 [`.env.example`](./.env.example)：

| Var | 默认 | 说明 |
|---|---|---|
| `QATLAS_RAG_EMBED_TOKEN` | (空) | 调用 `/embed` / `/rerank` 必带的 bearer |
| `QATLAS_RAG_EMBED_MODEL` | `BAAI/bge-m3` | dense embedding model |
| `QATLAS_RAG_RERANKER_MODEL` | `BAAI/bge-reranker-v2-m3` | cross-encoder reranker |

## 部署端到端

整套（Qdrant + embed worker + qatlasd 启用 RAG）部署见 [`docs/deployment/rag.md`](../docs/deployment/rag.md)。
