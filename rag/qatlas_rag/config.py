"""Shared pydantic-settings config for the embed worker.

Only the embed worker is now Python — the query path (sidecar / Qdrant
client) was reabsorbed into qatlasd (Go) in v0.20.0, so the previously
shipped `sidecar` and `ingest` Python roles are gone. See ./README.md.
"""

from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="QATLAS_RAG_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # --- Embed worker (Ag-Workstation 5080) ---
    embed_token: str | None = None
    embed_model: str = "BAAI/bge-m3"
    reranker_model: str = "BAAI/bge-reranker-v2-m3"


def get_settings() -> Settings:
    return Settings()
