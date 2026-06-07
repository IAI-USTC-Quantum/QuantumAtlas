"""qatlas-rag: GPU embed worker for QuantumAtlas RAG.

Layout:
- qatlas_rag.embed   — bge-m3 worker (FastAPI), GPU-resident embedding + rerank.
- qatlas_rag.config  — pydantic-settings config (env-driven).
- qatlas_rag.cli     — `qatlas-rag` console entry point.

As of v0.20.0 the query path (Qdrant calls, hybrid query, rerank, snippet
assembly) lives in the Go `qatlasd` server; this package is just the embed
worker it calls over HTTP.
"""

__version__ = "0.1.0"
