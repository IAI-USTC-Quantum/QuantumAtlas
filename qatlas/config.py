"""
QuantumAtlas Configuration

Environment-driven settings shared by the ``qatlas`` client CLI and the
local workspace tooling. The HTTP service itself is the Go ``qatlasd``
binary; this module only resolves how the Python side reaches it and where
local assets live.
"""

import logging
import os
from pathlib import Path
from typing import Any, Optional

from dotenv import dotenv_values
from pydantic import AliasChoices, Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

from qatlas.paths import resolve_dotenv_path

logger = logging.getLogger(__name__)


def bootstrap_env(dotenv_path: Optional[Path] = None) -> None:
    """Mirror dotenv file values into ``os.environ`` with ``override=False``.

    Direct ``os.getenv`` readers elsewhere in the codebase
    (``qatlas.client._common.resolve_token`` reading ``QATLAS_TOKEN``,
    ``qatlas.wiki.engine`` reading ``QATLAS_WIKI_DIR``, etc.) historically
    only saw real environment variables, NOT values pydantic-settings
    loaded from a dotenv file — because pydantic-settings populates
    fields on the model instance, never touches ``os.environ``.

    This shim closes the gap: after resolving the user's dotenv file
    we copy each key into ``os.environ`` only when the variable isn't
    already set, so a real env var always wins. Matches the precedence
    chain documented in :meth:`ServerConfig.from_env`.

    Pass ``dotenv_path=None`` to use the standard resolution chain.
    """
    if dotenv_path is None:
        dotenv_path, _ = resolve_dotenv_path()
    if dotenv_path is None or not dotenv_path.is_file():
        return
    for key, value in dotenv_values(dotenv_path).items():
        if value is None:
            continue
        # override=False semantic: real env var wins.
        os.environ.setdefault(key, value)


def get_project_root() -> Path:
    """Resolve repository root (directory containing the qatlas package)."""
    current = Path(__file__).resolve()
    for parent in current.parents:
        if (parent / "qatlas").is_dir():
            return parent
    return Path.cwd()


def _skip_dotenv() -> bool:
    """Return whether repository .env loading is disabled for the current process.

    Honors ``QATLAS_SKIP_DOTENV`` first; falls back to the legacy
    ``QUANTUMATLAS_SKIP_DOTENV`` for back-compat.
    """
    for key in ("QATLAS_SKIP_DOTENV", "QUANTUMATLAS_SKIP_DOTENV"):
        if os.getenv(key, "").lower() in {"1", "true", "yes"}:
            return True
    return False


class ServerConfig(BaseSettings):
    """Server configuration settings."""

    model_config = SettingsConfigDict(
        env_file=None,
        env_file_encoding="utf-8",
        extra="ignore",
        populate_by_name=True,
    )

    # ─── Our own settings: prefer QATLAS_* names; legacy bare names kept as aliases ───
    # Server settings (server only)
    host: str = Field(
        "127.0.0.1",
        validation_alias=AliasChoices("QATLAS_SERVER_HOST", "SERVER_HOST"),
    )
    port: int = Field(
        4200,
        validation_alias=AliasChoices("QATLAS_SERVER_PORT", "SERVER_PORT"),
    )
    debug: bool = Field(
        False,
        validation_alias=AliasChoices("QATLAS_SERVER_DEBUG", "SERVER_DEBUG"),
    )

    # Wiki / raw / data dirs
    wiki_dir: str = Field(
        "wiki",
        validation_alias=AliasChoices("QATLAS_WIKI_DIR", "WIKI_DIR"),
    )
    raw_dir: str = Field(
        "raw",
        validation_alias=AliasChoices("QATLAS_RAW_DIR", "RAW_DIR"),
    )
    data_dir: str = Field(
        "data",
        validation_alias=AliasChoices("QATLAS_DATA_DIR", "DATA_DIR"),
    )

    # Collaboration / outward-facing URLs
    # Renamed: PUBLIC_BASE_URL → QATLAS_SERVER_URL (clearer in client context).
    server_url: Optional[str] = Field(
        None,
        validation_alias=AliasChoices("QATLAS_SERVER_URL", "PUBLIC_BASE_URL"),
    )
    # Client-only: skip TLS certificate verification (for self-signed servers).
    insecure: bool = Field(
        False,
        validation_alias="QATLAS_INSECURE",
    )
    user_header: Optional[str] = Field(
        None,
        validation_alias=AliasChoices("QATLAS_USER_HEADER", "USER_HEADER"),
    )
    # PocketBase (IdP / PAT management)
    pocketbase_url: str = Field(
        "http://127.0.0.1:8090",
        validation_alias=AliasChoices("QATLAS_POCKETBASE_URL", "POCKETBASE_URL"),
    )
    pocketbase_data_dir: Optional[str] = Field(
        None,
        validation_alias="QATLAS_POCKETBASE_DATA_DIR",
    )
    pocketbase_port: Optional[str] = Field(
        None,
        validation_alias="QATLAS_POCKETBASE_PORT",
    )
    session_secret: Optional[str] = Field(
        None,
        validation_alias="QATLAS_SESSION_SECRET",
    )
    admin_github_logins: Optional[str] = Field(
        None,
        validation_alias="QATLAS_ADMIN_GITHUB_LOGINS",
    )

    # MinerU PDF parser (third-party vendor name; no QATLAS_ prefix)
    mineru_api_token: Optional[str] = Field(None, validation_alias="MINERU_API_TOKEN")
    mineru_api_base_url: str = Field("https://mineru.net", validation_alias="MINERU_API_BASE_URL")
    mineru_model_version: str = Field("vlm", validation_alias="MINERU_MODEL_VERSION")
    mineru_language: str = Field("ch", validation_alias="MINERU_LANGUAGE")
    mineru_is_ocr: bool = Field(False, validation_alias="MINERU_IS_OCR")
    mineru_enable_formula: bool = Field(True, validation_alias="MINERU_ENABLE_FORMULA")
    mineru_enable_table: bool = Field(True, validation_alias="MINERU_ENABLE_TABLE")
    mineru_poll_interval: float = Field(3.0, validation_alias="MINERU_POLL_INTERVAL")
    mineru_timeout: int = Field(1800, validation_alias="MINERU_TIMEOUT")

    @property
    def public_base_url(self) -> Optional[str]:
        """Back-compat shim: server_url used to be called public_base_url."""
        return self.server_url

    @field_validator(
        "debug",
        "mineru_is_ocr",
        "mineru_enable_formula",
        "mineru_enable_table",
        mode="before",
    )
    @classmethod
    def _parse_true_only_bool(cls, value: Any) -> Any:
        """Keep legacy .env semantics: only the literal string true enables a flag."""
        if isinstance(value, str):
            return value.strip().lower() == "true"
        return value

    @field_validator(
        "server_url",
        mode="before",
    )
    @classmethod
    def _empty_string_to_none(cls, value: Any) -> Any:
        if value == "":
            return None
        return value

    @classmethod
    def from_env(cls) -> "ServerConfig":
        """Load configuration with the canonical precedence:

        1. Real OS environment variables (always win; ``--server-url`` /
           ``--token`` style CLI flags layer on top via argparse).
        2. ``QATLAS_DOTENV=<path>`` explicit override (for systemd
           units that ship a deployment-specific .env, or for
           containers that mount a pre-baked config file).
        3. ``~/.config/qatlas/.env`` (XDG, the canonical location for
           ``uv tool install`` users — populated via ``qatlas config
           init`` / ``qatlas config set``).
        4. Built-in defaults defined on each field.

        ``QATLAS_SKIP_DOTENV=1`` disables all dotenv loading and
        forces env-vars-only.

        The cwd ``./.env`` fallback was removed in v0.15.0a5 to
        match the gh / docker / kubectl / aws pattern (user-level
        CLIs MUST NOT silently pick up cwd config). Users who
        relied on it should run ``qatlas config init`` once, or set
        ``QATLAS_DOTENV=$PWD/.env`` for a one-off invocation.

        Also calls :func:`bootstrap_env` so the same precedence chain
        is visible to direct ``os.getenv`` readers elsewhere in the
        codebase (e.g. ``qatlas.client._common.resolve_token`` which
        reads ``QATLAS_TOKEN`` via os.getenv, not via this model).
        """
        if _skip_dotenv():
            return cls(_env_file=None)

        dotenv_path, _source = resolve_dotenv_path()
        # Mirror the values into os.environ so that direct os.getenv
        # readers (resolve_token, get_wiki_root, etc.) see the same
        # values pydantic-settings just loaded into the model. Done
        # with override=False so a real env var always wins, matching
        # the documented precedence.
        if dotenv_path is not None:
            bootstrap_env(dotenv_path)
        return cls(_env_file=dotenv_path)

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
        if self.server_url:
            return self.server_url.rstrip("/")
        return None

    def get_server_url(self) -> Optional[str]:
        """Alias of get_public_base_url(); preferred name in new code."""
        return self.get_public_base_url()

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
