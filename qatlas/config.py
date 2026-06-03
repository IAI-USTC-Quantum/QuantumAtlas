"""
QuantumAtlas Configuration

Environment-driven settings shared by the ``qatlas`` client CLI and the
local workspace tooling. The HTTP service itself is the Go ``qatlasd``
binary; this module only resolves how the Python side reaches it and where
local assets live.

Storage / precedence (v0.17.0+):

  1. CLI flag (each command's argparse)
  2. OS env var (QATLAS_*, MINERU_*, OPENAI_*, ANTHROPIC_*)
  3. $QATLAS_CONFIG (explicit YAML override path)
  4. $QATLAS_DOTENV (legacy .env override; deprecated)
  5. ~/.config/qatlas/config.yaml (XDG, canonical)
  6. ~/.config/qatlas/.env (legacy XDG, auto-migrated on next config init)
  7. Built-in Field default

The YAML schema is derived 1:1 from ServerConfig field names (snake_case)
— no hand-maintained mapping table. Adding a new field automatically
makes it readable from YAML; we use validation_alias to keep the env-var
names (which often don't match the field name, e.g. SDK-standard
``MINERU_API_TOKEN``) working.

This is a v0.17.0 rewrite of the v0.16.0 "flatten YAML into os.environ
then let pydantic-settings read env" hack. Now we go straight through
``pydantic_settings.YamlConfigSettingsSource``, with a thin
``sync_secrets_to_env`` shim that keeps the handful of legacy direct
``os.getenv`` call sites (qatlas.client._common.resolve_token,
qatlas.wiki.engine, etc.) working without touching them.
"""

import logging
import os
from pathlib import Path
from typing import Any, Optional, Tuple, Type

from dotenv import dotenv_values
from pydantic import AliasChoices, Field, field_validator
from pydantic_settings import (
    BaseSettings,
    PydanticBaseSettingsSource,
    SettingsConfigDict,
    YamlConfigSettingsSource,
)

from qatlas.paths import resolve_config_path, user_config_yaml_path

logger = logging.getLogger(__name__)


# Fields whose values must be visible to direct os.getenv readers
# elsewhere in the codebase (qatlas.client._common.resolve_token reads
# QATLAS_TOKEN; qatlas.client.mineru reads MINERU_*). We can't audit
# every call site for every future field, so the simplest contract is
# "every public env var the YAML can set gets pushed back into os.environ
# at config-load time, with setdefault semantics so real env wins".
#
# Tuple of (model field name, env var name) — kept in sync with
# ServerConfig field declarations below. The test
# tests/test_config_yaml.py::test_sync_targets_cover_all_validation_aliases
# guards this list against drift.
_ENV_SYNC_TARGETS: list[Tuple[str, str]] = [
    ("server_url", "QATLAS_SERVER_URL"),
    ("token", "QATLAS_TOKEN"),
    ("insecure", "QATLAS_INSECURE"),
    ("wiki_dir", "QATLAS_WIKI_DIR"),
    ("mineru_api_token", "MINERU_API_TOKEN"),
    ("mineru_api_base_url", "MINERU_API_BASE_URL"),
    ("mineru_model_version", "MINERU_MODEL_VERSION"),
    ("mineru_language", "MINERU_LANGUAGE"),
    ("mineru_is_ocr", "MINERU_IS_OCR"),
    ("mineru_enable_formula", "MINERU_ENABLE_FORMULA"),
    ("mineru_enable_table", "MINERU_ENABLE_TABLE"),
    ("mineru_poll_interval", "MINERU_POLL_INTERVAL"),
    ("mineru_timeout", "MINERU_TIMEOUT"),
    ("openai_api_key", "OPENAI_API_KEY"),
    ("anthropic_api_key", "ANTHROPIC_API_KEY"),
]


def _resolve_yaml_path() -> Optional[Path]:
    """Pick the YAML file pydantic-settings should read.

    Walks the same resolution chain documented in the module docstring,
    but returns only YAML hits (legacy .env files are handled by a
    separate dotenv shim in bootstrap_env_from_config).
    """
    path, source = resolve_config_path()
    if path is None or source is None:
        return None
    if source.endswith("_yaml"):
        return path
    return None


def sync_secrets_to_env(cfg: "ServerConfig") -> None:
    """Mirror configured field values into ``os.environ`` for the
    benefit of direct ``os.getenv`` readers elsewhere in the codebase.

    Uses ``setdefault`` semantics: a real env var already in the
    process environment wins over the YAML / .env value (matching
    the precedence chain in the module docstring).

    Only fields enumerated in :data:`_ENV_SYNC_TARGETS` are synced; the
    list is purposely narrow (only fields with known external readers)
    so we don't pollute the env with every server-only field a future
    contributor might add.
    """
    for field_name, env_name in _ENV_SYNC_TARGETS:
        value = getattr(cfg, field_name, None)
        if value is None or value == "":
            continue
        if isinstance(value, bool):
            os.environ.setdefault(env_name, "true" if value else "false")
        else:
            os.environ.setdefault(env_name, str(value))


def bootstrap_env_from_config(config_path: Optional[Path] = None) -> None:
    """Load the user config (YAML or legacy .env) and mirror its values
    into ``os.environ`` so direct ``os.getenv`` readers see them.

    This shim exists for back-compat with v0.16.0 callers; new code
    should just call :meth:`ServerConfig.from_env`. Dispatches on file
    extension; legacy .env files still go through python-dotenv with
    override-false semantic, identical to v0.16.0 behaviour.
    """
    if config_path is None:
        config_path, _ = resolve_config_path()
    if config_path is None or not config_path.is_file():
        return

    if config_path.suffix.lower() in (".yaml", ".yml"):
        # ServerConfig.from_env() reads the yaml + env in one go and
        # syncs the result back to os.environ.
        cfg = ServerConfig.from_env()
        sync_secrets_to_env(cfg)
        return

    # Legacy .env path — keep the dotenv loader so v0.16.x users
    # mid-migration aren't broken by a single config init.
    for key, value in dotenv_values(config_path).items():
        if value is None:
            continue
        os.environ.setdefault(key, value)


def bootstrap_env(dotenv_path: Optional[Path] = None) -> None:
    """Back-compat alias for :func:`bootstrap_env_from_config`."""
    bootstrap_env_from_config(dotenv_path)


def get_project_root() -> Path:
    """Resolve repository root (directory containing the qatlas package)."""
    current = Path(__file__).resolve()
    for parent in current.parents:
        if (parent / "qatlas").is_dir():
            return parent
    return Path.cwd()


def _skip_dotenv() -> bool:
    """Return whether file-based config loading is disabled.

    Honors ``QATLAS_SKIP_DOTENV`` first; falls back to the legacy
    ``QUANTUMATLAS_SKIP_DOTENV`` for back-compat.

    Despite the name (history: the original mechanism only knew about
    .env files), this flag now also disables YAML loading — the contract
    is "force env-vars-only mode", which is what tests and CI runners
    care about.
    """
    for key in ("QATLAS_SKIP_DOTENV", "QUANTUMATLAS_SKIP_DOTENV"):
        if os.getenv(key, "").lower() in {"1", "true", "yes"}:
            return True
    return False


class ServerConfig(BaseSettings):
    """User-level client config + a handful of fields the local workspace
    tooling reads.

    Fields are declared once; pydantic-settings' built-in
    ``YamlConfigSettingsSource`` + env source handle the [CLI > env >
    YAML > default] precedence. No hand-maintained YAML_TO_ENV table.

    Adding a new operator-tunable field: just declare it here with a
    ``validation_alias`` (the env var name) and — if direct os.getenv
    readers consume it elsewhere — add it to ``_ENV_SYNC_TARGETS`` at
    the top of the module.
    """

    model_config = SettingsConfigDict(
        env_file=None,
        env_file_encoding="utf-8",
        extra="ignore",
        populate_by_name=True,
    )

    # ── Server endpoint + auth ────────────────────────────────────
    server_url: Optional[str] = Field(
        None,
        validation_alias=AliasChoices("QATLAS_SERVER_URL", "PUBLIC_BASE_URL"),
    )
    token: Optional[str] = Field(
        None,
        validation_alias=AliasChoices("QATLAS_TOKEN", "TOKEN"),
    )
    insecure: bool = Field(False, validation_alias="QATLAS_INSECURE")
    user_header: Optional[str] = Field(
        None,
        validation_alias=AliasChoices("QATLAS_USER_HEADER", "USER_HEADER"),
    )

    # ── Server runtime (server-only; client reads only on local boot)
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

    # ── Filesystem ────────────────────────────────────────────────
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

    # ── Legacy / server-side PocketBase (untouched, kept for back-compat)
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

    # ── MinerU (third-party SDK env names — no QATLAS_ prefix)
    mineru_api_token: Optional[str] = Field(None, validation_alias="MINERU_API_TOKEN")
    mineru_api_base_url: str = Field("https://mineru.net", validation_alias="MINERU_API_BASE_URL")
    mineru_model_version: str = Field("vlm", validation_alias="MINERU_MODEL_VERSION")
    mineru_language: str = Field("ch", validation_alias="MINERU_LANGUAGE")
    mineru_is_ocr: bool = Field(False, validation_alias="MINERU_IS_OCR")
    mineru_enable_formula: bool = Field(True, validation_alias="MINERU_ENABLE_FORMULA")
    mineru_enable_table: bool = Field(True, validation_alias="MINERU_ENABLE_TABLE")
    mineru_poll_interval: float = Field(3.0, validation_alias="MINERU_POLL_INTERVAL")
    mineru_timeout: int = Field(1800, validation_alias="MINERU_TIMEOUT")

    # ── LLM extractor (third-party SDK env names — no QATLAS_ prefix)
    openai_api_key: Optional[str] = Field(None, validation_alias="OPENAI_API_KEY")
    anthropic_api_key: Optional[str] = Field(None, validation_alias="ANTHROPIC_API_KEY")

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
        """Keep legacy .env semantics: only the literal string ``true``
        enables a flag (matches the Go server's behaviour in
        internal/config). YAML native booleans pass through unchanged.
        """
        if isinstance(value, str):
            return value.strip().lower() == "true"
        return value

    @field_validator("server_url", "token", mode="before")
    @classmethod
    def _empty_string_to_none(cls, value: Any) -> Any:
        """An empty string in YAML / env should look "unset" to the
        consumer; otherwise a freshly-init'd yaml with ``token:`` would
        be treated as "no token" but the resolve_token() call site
        would see ``QATLAS_TOKEN=""`` and fail differently.
        """
        if value == "":
            return None
        return value

    # ─── pydantic-settings source chain ───────────────────────────
    #
    # Order matters — the first source to return a value wins, so the
    # canonical precedence ends up being:
    #
    #   init args (CLI overrides passed by tests / programmatic callers)
    #   > OS environment variables
    #   > YAML config file (~/.config/qatlas/config.yaml or $QATLAS_CONFIG)
    #   > built-in Field defaults
    #
    # We don't add ``dotenv_settings`` here: legacy .env files are
    # handled by bootstrap_env_from_config() before pydantic-settings
    # sees the env (it injects via os.environ.setdefault), so the env
    # source picks them up naturally.
    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls: Type[BaseSettings],
        init_settings: PydanticBaseSettingsSource,
        env_settings: PydanticBaseSettingsSource,
        dotenv_settings: PydanticBaseSettingsSource,
        file_secret_settings: PydanticBaseSettingsSource,
    ) -> Tuple[PydanticBaseSettingsSource, ...]:
        sources: list[PydanticBaseSettingsSource] = [init_settings, env_settings]
        yaml_path = _resolve_yaml_path()
        if yaml_path is not None and yaml_path.is_file():
            sources.append(YamlConfigSettingsSource(settings_cls, yaml_file=yaml_path))
        return tuple(sources)

    @classmethod
    def from_env(cls) -> "ServerConfig":
        """Load configuration with the canonical precedence (see module
        docstring).

        ``QATLAS_SKIP_DOTENV=1`` disables all file-based loading and
        forces env-vars-only.
        """
        if _skip_dotenv():
            # Skip means "no YAML, no .env, env vars only". Build with
            # the default sources only (init + env), bypassing
            # settings_customise_sources's YAML hook.
            saved = cls.settings_customise_sources
            try:
                cls.settings_customise_sources = classmethod(  # type: ignore[method-assign]
                    lambda _cls, _settings_cls, init_settings, env_settings, *_args, **_kwargs: (
                        init_settings,
                        env_settings,
                    )
                )
                return cls()
            finally:
                cls.settings_customise_sources = saved  # type: ignore[method-assign]

        # Pre-load any legacy .env into os.environ so the env source
        # below can see those values. (YAML hits go straight through
        # YamlConfigSettingsSource attached by settings_customise_sources.)
        path, source = resolve_config_path()
        if path is not None and source and source.endswith("_dotenv_legacy"):
            for key, value in dotenv_values(path).items():
                if value is not None:
                    os.environ.setdefault(key, value)

        return cls()

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


def env_alias_to_field(env_name: str) -> Optional[str]:
    """Walk ServerConfig.model_fields to find the field whose
    ``validation_alias`` (single string OR AliasChoices) contains
    ``env_name``. Returns the snake_case field name (which is also the
    YAML key) or ``None`` if no field claims this env name.

    Used by ``qatlas config set/get/unset`` so operators can keep using
    the env-var name they're already familiar with
    (``QATLAS_SERVER_URL``, ``MINERU_API_TOKEN``) without learning the
    YAML key separately.
    """
    target = env_name.strip()
    for field_name, field_info in ServerConfig.model_fields.items():
        alias = field_info.validation_alias
        if alias is None:
            continue
        # AliasChoices wraps multiple aliases; plain string is the simple case.
        if hasattr(alias, "choices"):
            for choice in alias.choices:
                if hasattr(choice, "alias"):
                    if choice.alias == target:
                        return field_name
                elif choice == target:
                    return field_name
        elif alias == target:
            return field_name
    return None


def field_to_env_alias(field_name: str) -> Optional[str]:
    """Inverse of env_alias_to_field: return the canonical env var name
    for a given snake_case field name. Returns the FIRST alias when
    the field has multiple (canonical = primary).
    """
    info = ServerConfig.model_fields.get(field_name)
    if info is None or info.validation_alias is None:
        return None
    alias = info.validation_alias
    if hasattr(alias, "choices"):
        first = next(iter(alias.choices))
        if hasattr(first, "alias"):
            return first.alias
        return str(first)
    return str(alias)
