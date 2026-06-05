"""Shared pydantic-settings config.

Each role only reads the subset of fields it needs:
- embed worker  → QATLAS_RAG_EMBED_*
- ingester      → QATLAS_RAG_S3_*, QATLAS_RAG_QDRANT_*, QATLAS_RAG_EMBED_*
- sidecar       → QATLAS_RAG_QDRANT_*, QATLAS_RAG_EMBED_*
"""

from __future__ import annotations

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="QATLAS_RAG_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # --- Qdrant (1810 docker) ---
    qdrant_url: str = "http://10.144.18.10:6334"  # gRPC; sidecar prefers gRPC
    qdrant_http_url: str = "http://10.144.18.10:6333"
    qdrant_api_key: str | None = None
    qdrant_collection: str = "qatlas_papers_v1"

    # --- Embed worker (Ag-Workstation 5080) ---
    embed_url: str = "http://10.144.18.88:8801"
    embed_token: str | None = None
    embed_model: str = "BAAI/bge-m3"
    reranker_model: str = "BAAI/bge-reranker-v2-m3"

    # --- RustFS (1810) ---
    s3_endpoint: str = "http://10.144.18.10:9000"
    s3_region: str = "us-east-1"
    s3_access_key: str | None = None
    s3_secret_key: str | None = None
    s3_md_bucket: str = "qatlas-md"
    s3_images_bucket: str = "qatlas-images"

    # --- Local state ---
    manifest_path: str = "./manifest.db"

    # --- Sidecar tuning ---
    default_top_k: int = Field(default=8, ge=1, le=50)
    default_rerank_pool: int = Field(default=50, ge=10, le=200)


def get_settings() -> Settings:
    return Settings()
