"""
Server Configuration

Defines configuration for the FastAPI web server.
"""

import os
from pathlib import Path
from typing import Any, Optional

from pydantic import AliasChoices, Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


def get_project_root() -> Path:
    """Resolve repository root (directory containing the atlas package)."""
    current = Path(__file__).resolve()
    for parent in current.parents:
        if (parent / "atlas").is_dir():
            return parent
    return Path.cwd()


def _skip_dotenv() -> bool:
    """Return whether repository .env loading is disabled for the current process."""
    return os.getenv("QUANTUMATLAS_SKIP_DOTENV", "").lower() in {"1", "true", "yes"}


class ServerConfig(BaseSettings):
    """Server configuration settings."""

    model_config = SettingsConfigDict(
        env_file=None,
        env_file_encoding="utf-8",
        extra="ignore",
        populate_by_name=True,
    )

    # Server settings
    host: str = Field("127.0.0.1", validation_alias="SERVER_HOST")
    port: int = Field(4200, validation_alias="SERVER_PORT")
    debug: bool = Field(False, validation_alias="SERVER_DEBUG")

    # Neo4j settings
    neo4j_uri: str = Field("bolt://localhost:7687", validation_alias="NEO4J_URI")
    neo4j_user: str = Field("neo4j", validation_alias="NEO4J_USER")
    neo4j_password: str = Field("", validation_alias="NEO4J_PASSWORD")

    # Wiki settings
    wiki_dir: str = Field("wiki", validation_alias="WIKI_DIR")
    raw_dir: str = Field("raw", validation_alias="RAW_DIR")

    # Collaboration / raw exposure
    data_dir: str = Field("data", validation_alias="DATA_DIR")
    public_base_url: Optional[str] = Field(None, validation_alias="PUBLIC_BASE_URL")
    share_access_token: Optional[str] = Field(
        None,
        validation_alias=AliasChoices("SHARE_ACCESS_TOKEN", "PUBLIC_SHARE_TOKEN"),
    )
    default_share_expires_in: Optional[int] = Field(
        600, validation_alias="DEFAULT_SHARE_EXPIRES_IN"
    )
    user_header: Optional[str] = Field(None, validation_alias="USER_HEADER")
    require_release_tag: bool = Field(
        False,
        validation_alias=AliasChoices("QUANTUMATLAS_REQUIRE_RELEASE_TAG", "REQUIRE_RELEASE_TAG"),
    )
    # LLM settings
    openai_api_key: Optional[str] = Field(None, validation_alias="OPENAI_API_KEY")
    anthropic_api_key: Optional[str] = Field(None, validation_alias="ANTHROPIC_API_KEY")
    mineru_api_token: Optional[str] = Field(None, validation_alias="MINERU_API_TOKEN")
    mineru_api_base_url: str = Field("https://mineru.net", validation_alias="MINERU_API_BASE_URL")
    mineru_model_version: str = Field("vlm", validation_alias="MINERU_MODEL_VERSION")
    mineru_language: str = Field("ch", validation_alias="MINERU_LANGUAGE")
    mineru_is_ocr: bool = Field(False, validation_alias="MINERU_IS_OCR")
    mineru_enable_formula: bool = Field(True, validation_alias="MINERU_ENABLE_FORMULA")
    mineru_enable_table: bool = Field(True, validation_alias="MINERU_ENABLE_TABLE")
    mineru_poll_interval: float = Field(3.0, validation_alias="MINERU_POLL_INTERVAL")
    mineru_timeout: int = Field(1800, validation_alias="MINERU_TIMEOUT")

    @field_validator(
        "debug",
        "mineru_is_ocr",
        "mineru_enable_formula",
        "mineru_enable_table",
        "require_release_tag",
        mode="before",
    )
    @classmethod
    def _parse_true_only_bool(cls, value: Any) -> Any:
        """Keep legacy .env semantics: only the literal string true enables a flag."""
        if isinstance(value, str):
            return value.strip().lower() == "true"
        return value

    @field_validator(
        "public_base_url",
        "share_access_token",
        mode="before",
    )
    @classmethod
    def _empty_string_to_none(cls, value: Any) -> Any:
        if value == "":
            return None
        return value

    @classmethod
    def from_env(cls) -> "ServerConfig":
        """Load configuration from environment variables."""
        env_file = None if _skip_dotenv() else get_project_root() / ".env"
        return cls(_env_file=env_file)

    def get_neo4j_config(self) -> dict:
        """Get Neo4j connection configuration."""
        return {
            "uri": self.neo4j_uri,
            "user": self.neo4j_user,
            "password": self.neo4j_password,
        }

    def get_raw_root(self) -> Path:
        """Resolve RAW_DIR."""
        raw_path = Path(self.raw_dir)
        if not raw_path.is_absolute():
            raw_path = get_project_root() / raw_path
        return raw_path.resolve()

    def get_data_root(self) -> Path:
        """Resolve DATA_DIR."""
        data_path = Path(self.data_dir)
        if not data_path.is_absolute():
            data_path = get_project_root() / data_path
        return data_path.resolve()

    def get_public_base_url(self) -> Optional[str]:
        """Return the external service base URL when configured."""
        if self.public_base_url:
            return self.public_base_url.rstrip("/")
        return None

    def get_paper_asset_dir(self, kind: str) -> Path:
        """Resolve one canonical paper asset subdirectory under RAW_DIR."""
        if kind not in {"pdf", "markdown", "json", "images"}:
            raise ValueError(f"unknown paper asset kind: {kind}")
        return (self.get_raw_root() / kind).resolve()


# Global configuration instance
config: Optional[ServerConfig] = None


def get_config() -> ServerConfig:
    """Get or create global configuration."""
    global config
    if config is None:
        config = ServerConfig.from_env()
    return config
