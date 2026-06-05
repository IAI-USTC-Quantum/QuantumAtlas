"""qatlas-rag: RAG sidecar for QuantumAtlas-Internal.

Layout:
- qatlas_rag.embed   — bge-m3 worker (FastAPI), GPU-resident, runs on Ag-Workstation 5080.
- qatlas_rag.ingest  — RustFS list/diff manifest + chunker + Qdrant upsert.
- qatlas_rag.sidecar — query-path FastAPI service deployed on each edge.
- qatlas_rag.config  — pydantic-settings config (env-driven).
- qatlas_rag.cli     — `qatlas-rag` console entry point.
"""

__version__ = "0.1.0"
