# qatlas_rag — RAG pipeline (sidecar + embed worker + ingester)

> Python 子项目，与 Go 服务端 (`qatlasd`)、PocketBase、SPA (`web/`) 共享同一个仓库与 `pyproject.toml`。**不是独立 uv 项目**——所有 deps 通过主 `pyproject.toml` 的可选 extras 装。

Go 端 (`internal/routes/rag.go`) 把 `/api/rag/*` reverse-proxy 给本目录跑出来的 sidecar。Sidecar 自己再查 Qdrant + 调 embed worker。整套架构示意：

```
浏览器 → qatlasd /api/rag/*  (PaperAccessEnabled + RAGSidecarURL 双闸)
              │
              ▼
   sidecar  ./qatlas_rag/sidecar/app.py   (FastAPI, edge-local 127.0.0.1:8802)
              │ Qdrant gRPC          │ embed/rerank HTTP
              ▼                       ▼
   Qdrant (1810 WSL docker)    embed worker (GPU, ./qatlas_rag/embed/worker.py)
   见 deploy/qdrant-compose.example.yaml      bge-m3 + bge-reranker-v2-m3 fp16
```

## 三个角色 = 三个 extras

| Role | 跑在哪 | 装什么 | 是什么 |
|---|---|---|---|
| **sidecar** | 每个边缘节点（RackNerd / Alibaba / 本机 dev） | `uv sync --extra sidecar` | `qatlas_rag.sidecar.app:app` — FastAPI 反向代理 in front of Qdrant，无 torch |
| **embed** | GPU 主机（典型：RTX 5080 / sm_120 Blackwell） | `uv sync --extra embed` | `qatlas_rag.embed.worker:app` — `/embed` 和 `/rerank` 端点；bge-m3 + reranker fp16 |
| **ingest** | 任意能访问 RustFS + Qdrant + embed worker 的机器 | `uv sync --extra ingest` | `qatlas-rag ingest …` 一次性命令；listdiff RustFS → chunk → embed → upsert Qdrant |

PyTorch wheel 走 `download.pytorch.org/whl/cu128`（sm_120 / Blackwell 必须 torch>=2.7）；主 `pyproject.toml` 的 `[tool.uv.sources]` 已经把 `torch` scope 到这条 index，只有装 `embed` extra 时才拉。

## 目录

```
rag/
├── README.md                ← 你正在看
├── .env.example             ← 三个 role 共用的环境变量模板（QATLAS_RAG_*）
├── .gitignore               ← 只忽略 rag/ 私货（manifest.db, qdrant_storage/, .env）
├── qatlas_rag/              ← Python 包；主 pyproject 通过 hatch sources 映射到顶层
│   ├── cli.py               ← `qatlas-rag` 入口
│   ├── config.py            ← pydantic-settings（QATLAS_RAG_*）
│   ├── embed/worker.py      ← FastAPI on 5080
│   ├── ingest/              ← chunker / parser / s3 / qdrant_store / manifest / runner / embed_client
│   └── sidecar/app.py       ← FastAPI on edge
├── tests/                   ← pytest，主仓库 testpaths 已经加 "rag/tests"
├── scripts/spike/           ← 一次性验证脚本（phase1 GPU smoke / phase2.5 e2e / full_build）
├── docs/spike-report.md     ← Phase 2.5 dense vs hybrid 结果
└── deploy/                  ← rag-自有的部署文件
    ├── qatlas-rag-embed.service       ← systemd user unit，5080 worker
    ├── qatlas-rag-sidecar.service     ← systemd user unit，边缘 sidecar
    └── portproxy-qdrant-1810.{ps1,v2.ps1}   ← Windows 端 portproxy 给 WSL2 Qdrant
```

Qdrant 的 docker compose 放在 **`deploy/qdrant-compose.example.yaml`**（顶层 deploy/，跟 neo4j / rustfs 的 compose 平级），不在本子目录。

## 验证：GPU smoke（任何 phase 之前必跑）

```bash
uv sync --extra embed
uv run python -m rag.scripts.spike.phase1_gpu_smoke
```

通过 = bge-m3 + reranker fp16 都能 resident，VRAM peak ~2.7 GB，embed 32×~800tok ~0.9s，rerank 50 pairs ~1s。

## 部署文档

- 端到端架构 + Alibaba/RackNerd 上线流程 → **[`docs/deployment/rag.md`](../docs/deployment/rag.md)**
- Qdrant docker 部署 → `deploy/qdrant-compose.example.yaml` 顶部注释
- 边缘 server 加挂 sidecar → `qatlas_rag.sidecar.app:app` + `QATLAS_RAG_SIDECAR_URL` env（Go 端读）
- 服务端反代实现 → `internal/routes/rag.go`（Go 测试 `internal/routes/rag_test.go`）

## 协作

Python 测试 `uv run pytest rag/tests/`；主 `pytest` 也会自动 discover（`testpaths = ["tests", "rag/tests"]`）。

新增 dep 改主 `pyproject.toml` 对应 extras 段（`sidecar` / `embed` / `ingest`），跑 `uv lock --extra sidecar --extra embed --extra ingest` 更新 lockfile，commit `pyproject.toml` + `uv.lock`。
