"""
QuantumAtlas client configuration (v0.17.0+).

Single source of truth: a ``config.yaml`` file at the OS-native
user-config location (resolved via :mod:`platformdirs`):

* Linux:   ``~/.config/qatlas/config.yaml``  (honors ``XDG_CONFIG_HOME``)
* macOS:   ``~/Library/Application Support/qatlas/config.yaml``
* Windows: ``%APPDATA%\\qatlas\\config.yaml``

Auto-created on first invocation of any ``qatlas`` subcommand. No CLI
flag, no OS env var, no ``$QATLAS_DOTENV`` / ``$QATLAS_CONFIG``
override. ``qatlas config path`` prints the resolved path at runtime
when unsure.

Rationale:

* Server (``qatlasd``) is a long-lived daemon — it lives behind systemd
  / docker / k8s and needs CLI flag + env + .env so each deploy form
  has a natural injection point.
* Client (``qatlas``) is a short-lived per-call CLI — users configure
  it once and reuse it forever. A single YAML file is the simplest
  mental model and matches how ``gh`` / ``kubectl`` / ``aws`` /
  ``rclone`` work.

The class is still named ``ServerConfig`` for back-compat with existing
``from qatlas.config import ServerConfig`` imports; rename is left for
a future major version to avoid churn.
"""
from __future__ import annotations

import logging
from pathlib import Path
from typing import Any, Optional, Tuple, Type

from pydantic import Field, field_validator
from pydantic_settings import (
    BaseSettings,
    PydanticBaseSettingsSource,
    SettingsConfigDict,
    YamlConfigSettingsSource,
)

from qatlas.paths import user_config_yaml_path

logger = logging.getLogger(__name__)


def get_project_root() -> Path:
    """Resolve repository root (directory containing the qatlas package).

    Used by ``get_raw_root`` / ``get_data_root`` to anchor relative
    paths configured in YAML. Falls back to CWD for installed-only
    usage where the qatlas source tree isn't on disk.
    """
    current = Path(__file__).resolve()
    for parent in current.parents:
        if (parent / "qatlas").is_dir():
            return parent
    return Path.cwd()


_DEFAULT_CONFIG_YAML = """\
# QuantumAtlas client config — managed by `qatlas config` subcommand or
# edit this file directly. See https://github.com/IAI-USTC-Quantum/QuantumAtlas
#
# All values are OPTIONAL. Defaults shown commented; uncomment and edit
# the fields you need.

# ── Server endpoint + auth ─────────────────────────────────────────
# server_url: https://quantum-atlas.ai
# token:                # PAT from https://<server>/pat; required for write ops
# insecure: false       # skip TLS verification (dev / self-signed only)

# ── Local workspace (dev tooling: qatlas wiki / ingest) ────────────
# wiki_dir: ./wiki      # local checkout of QuantumAtlas-Wiki
# raw_dir: ./raw        # asset cache root

# ── MinerU API (qatlas mineru) ─────────────────────────────────────
# mineru_api_tokens:        # List of JWTs from https://mineru.net
#   - jwt-1                 # client rotates across them when one hits
#   - jwt-2                 # the daily quota
# mineru_api_base_url: https://mineru.net
# mineru_model_version: vlm
# mineru_language: ch
# mineru_is_ocr: false
# mineru_enable_formula: true
# mineru_enable_table: true
# mineru_poll_interval: 3.0
# mineru_timeout: 1800

# ── LLM extractor (qatlas extractor; experimental) ────────────────
# openai_api_key:
# anthropic_api_key:
"""


def ensure_default_config_exists() -> Path:
    """Create the default ``config.yaml`` template at the canonical
    path if it does not already exist. Idempotent — safe to call on
    every CLI invocation.

    Returns the resolved path either way.
    """
    path = user_config_yaml_path()
    if path.exists():
        return path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(_DEFAULT_CONFIG_YAML, encoding="utf-8")
    try:
        path.chmod(0o600)
    except OSError:
        # E.g. SMB / FAT mounts without POSIX perms — survive but warn.
        logger.warning("Could not chmod 0600 on %s; secrets may be world-readable.", path)
    logger.info("Created default config at %s", path)
    return path


class ServerConfig(BaseSettings):
    """User-level client config (v0.17.0+: YAML only).

    All fields read from the qatlas config.yaml (see module docstring
    for OS-specific paths). Init args (passed when constructing
    programmatically) still win for tests and embedded callers. No
    env-var fallback.
    """

    model_config = SettingsConfigDict(
        # Disable pydantic-settings env loading entirely — yaml is the
        # only source. Tests / embedded callers pass overrides as init
        # args to ServerConfig(field=value).
        env_file=None,
        extra="ignore",
        populate_by_name=True,
    )

    # ── Server endpoint + auth ────────────────────────────────────
    server_url: Optional[str] = Field(default=None)
    token: Optional[str] = Field(default=None)
    insecure: bool = Field(default=False)
    user_header: Optional[str] = Field(default=None)

    # ── Local workspace (dev tooling reads these) ────────────────
    wiki_dir: str = Field(default="wiki")
    raw_dir: str = Field(default="raw")
    data_dir: str = Field(default="data")

    # ── MinerU (third-party SDK; client-only) ────────────────────
    # Pool of MinerU API tokens. Configure ≥1 to enable MinerU calls;
    # client (today) uses the first valid one and surfaces a deprecation
    # log when extras are present — full client-side rotation tracks
    # the server-side feature and is on the roadmap. Set via YAML:
    #   mineru_api_tokens: [jwt-1, jwt-2]
    # Or env (server processes only): MINERU_API_TOKENS=jwt-1,jwt-2
    mineru_api_tokens: list[str] = Field(default_factory=list)
    mineru_api_base_url: str = Field(default="https://mineru.net")
    mineru_model_version: str = Field(default="vlm")
    mineru_language: str = Field(default="ch")
    mineru_is_ocr: bool = Field(default=False)
    mineru_enable_formula: bool = Field(default=True)
    mineru_enable_table: bool = Field(default=True)
    mineru_poll_interval: float = Field(default=3.0)
    mineru_timeout: int = Field(default=1800)

    @property
    def mineru_api_token(self) -> Optional[str]:
        """Convenience accessor: the first configured MinerU API token,
        or ``None`` when the pool is empty. Existing client code that
        needs a single token to pass into the per-paper MinerU client
        keeps working through this shim while full client-side
        rotation is built out separately.
        """
        for tok in self.mineru_api_tokens:
            if tok:
                return tok
        return None

    # ── LLM extractor (third-party SDK; client-only) ─────────────
    openai_api_key: Optional[str] = Field(default=None)
    anthropic_api_key: Optional[str] = Field(default=None)

    @property
    def public_base_url(self) -> Optional[str]:
        """Back-compat shim: server_url used to be called public_base_url."""
        return self.server_url

    @field_validator(
        "insecure",
        "mineru_is_ocr",
        "mineru_enable_formula",
        "mineru_enable_table",
        mode="before",
    )
    @classmethod
    def _coerce_string_bool(cls, value: Any) -> Any:
        """Tolerate legacy string booleans in user-edited YAML."""
        if isinstance(value, str):
            return value.strip().lower() in {"1", "true", "yes", "on", "y", "t"}
        return value

    @field_validator("server_url", "token", "user_header",
                     "openai_api_key", "anthropic_api_key", mode="before")
    @classmethod
    def _empty_string_to_none(cls, value: Any) -> Any:
        """Treat an empty YAML string as "unset" so unconfigured fields
        are uniformly ``None`` rather than ``""`` (the latter breaks
        ``if cfg.token:`` checks).
        """
        if value == "":
            return None
        return value

    @field_validator("mineru_api_tokens", mode="before")
    @classmethod
    def _coerce_mineru_tokens(cls, value: Any) -> Any:
        """Accept either a YAML list or a single string (for legacy
        single-token configs and CSV env values). Empty / whitespace
        entries are dropped so ``[]`` always means "no tokens".
        """
        if value is None or value == "":
            return []
        if isinstance(value, str):
            value = [v.strip() for v in value.split(",")]
        if isinstance(value, list):
            return [str(v).strip() for v in value if str(v).strip()]
        return value

    # ─── pydantic-settings source chain ───────────────────────────
    #
    # Only init + YAML. No env source, no dotenv source.
    # Precedence: init args > YAML file > Field default.
    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls: Type[BaseSettings],
        init_settings: PydanticBaseSettingsSource,
        env_settings: PydanticBaseSettingsSource,
        dotenv_settings: PydanticBaseSettingsSource,
        file_secret_settings: PydanticBaseSettingsSource,
    ) -> Tuple[PydanticBaseSettingsSource, ...]:
        sources: list[PydanticBaseSettingsSource] = [init_settings]
        yaml_path = user_config_yaml_path()
        if yaml_path.is_file():
            sources.append(YamlConfigSettingsSource(settings_cls, yaml_file=yaml_path))
        return tuple(sources)

    @classmethod
    def from_env(cls) -> "ServerConfig":
        """Load configuration from the YAML file (auto-creating it if
        absent). Name kept for back-compat with v0.16.x callers; the
        "env" in the name is now a misnomer (we no longer read env).
        """
        ensure_default_config_exists()
        return cls()

    def get_raw_root(self) -> Path:
        """Resolve RAW_DIR (relative paths anchored at project root)."""
        raw_path = Path(self.raw_dir)
        if not raw_path.is_absolute():
            raw_path = get_project_root() / raw_path
        return raw_path.resolve()

    def get_data_root(self) -> Path:
        """Resolve DATA_DIR (relative paths anchored at project root)."""
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
