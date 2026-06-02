"""Default filesystem locations for the ``qatlas`` user-level CLI.

Resolution order for the user config file (v0.16.0+):

1. ``QATLAS_CONFIG`` env var — explicit YAML config override
   (preferred for systemd / docker / k8s, where the file is
   pre-baked at deploy time).
2. ``QATLAS_DOTENV`` env var — legacy .env override; emits a
   deprecation warning. Kept until v0.17.0 so existing systemd
   units don't break overnight.
3. ``$XDG_CONFIG_HOME/qatlas/config.yaml`` (default
   ``~/.config/qatlas/config.yaml``) — the canonical location,
   populated by ``qatlas config init`` and edited by
   ``qatlas config set``.
4. ``$XDG_CONFIG_HOME/qatlas/.env`` (legacy XDG dotenv) — also
   triggers a deprecation warning. The first ``qatlas config init``
   invocation against this file auto-migrates it to ``config.yaml``
   and renames the original to ``.env.migrated-from-v0.16.0`` so
   nothing is silently lost.

Mirrors the gh / docker / kubectl / aws / rclone pattern: a
user-level CLI MUST NOT silently pick up a ``./.env`` in the current
working directory. The cwd_legacy fallback was removed in v0.15.0a5.

Intentionally NOT a singleton — every call re-reads the env vars so
tests can monkey-patch ``HOME`` / ``XDG_CONFIG_HOME`` without import
order weirdness.
"""
from __future__ import annotations

import logging
import os
from pathlib import Path

_APP = "qatlas"

logger = logging.getLogger(__name__)


def _xdg_dir(env_var: str, fallback_rel_to_home: str) -> Path:
    """Resolve an XDG base dir per the freedesktop spec.

    Per the spec, relative values in ``XDG_*_HOME`` MUST be ignored
    (treated as unset). Mirrors portal-mcp-server/paths.py behaviour.
    """
    raw = os.environ.get(env_var)
    if raw:
        candidate = Path(raw).expanduser()
        if candidate.is_absolute():
            return candidate
        logger.warning(
            "Ignoring non-absolute %s=%r per XDG Base Directory spec; "
            "falling back to ~/%s.",
            env_var, raw, fallback_rel_to_home,
        )
    return Path.home() / fallback_rel_to_home


def xdg_config_home() -> Path:
    """Return ``$XDG_CONFIG_HOME`` (default ``~/.config``)."""
    return _xdg_dir("XDG_CONFIG_HOME", ".config")


def xdg_state_home() -> Path:
    """Return ``$XDG_STATE_HOME`` (default ``~/.local/state``)."""
    return _xdg_dir("XDG_STATE_HOME", ".local/state")


def xdg_cache_home() -> Path:
    """Return ``$XDG_CACHE_HOME`` (default ``~/.cache``)."""
    return _xdg_dir("XDG_CACHE_HOME", ".cache")


def user_config_dir() -> Path:
    """The directory for our user-level config files.

    Defaults to ``~/.config/qatlas/`` but follows
    ``$XDG_CONFIG_HOME`` when set.
    """
    return xdg_config_home() / _APP


def user_state_dir() -> Path:
    """The directory for our user-level state / cache files.

    Used e.g. for ``mineru-batches/`` resume hints when we add them.
    """
    return xdg_state_home() / _APP


def user_config_yaml_path() -> Path:
    """The canonical ``config.yaml`` path for ``qatlas`` users (v0.16.0+).

    Returned unconditionally — caller decides whether the file actually
    exists. ``qatlas config init`` writes here; ``qatlas config show /
    set / get / unset`` read here.
    """
    return user_config_dir() / "config.yaml"


def user_dotenv_path() -> Path:
    """The legacy ``.env`` path for ``qatlas`` users (pre-v0.16.0).

    Kept as a discoverable function because the .env → config.yaml
    auto-migration in ``qatlas config init`` still needs to find the
    old file. New code should use :func:`user_config_yaml_path`.
    """
    return user_config_dir() / ".env"


def resolve_config_path() -> tuple[Path | None, str | None]:
    """Locate the config file to load, in priority order (v0.16.0+).

    Returns ``(path, source)`` where ``source`` describes which rule
    matched:

      * ``"env_override_yaml"``       — ``QATLAS_CONFIG`` env var
      * ``"env_override_dotenv_legacy"`` — ``QATLAS_DOTENV`` env var
        (deprecated; emits a warning log; will be dropped in v0.17.0)
      * ``"xdg_yaml"``                — ``~/.config/qatlas/config.yaml``
      * ``"xdg_dotenv_legacy"``       — ``~/.config/qatlas/.env``
        (deprecated; emits a warning log; auto-migrated on the next
        ``qatlas config init`` invocation)

    Returns ``(None, None)`` when nothing is found — the caller should
    fall back to OS env vars only (every settings field has a
    ``QATLAS_*`` env alias).

    The source string is loadable-format-aware: callers can branch on
    ``source.endswith("_yaml")`` vs ``"_dotenv_legacy"`` instead of
    re-checking the file extension.
    """
    # Tier 1: explicit YAML override (preferred for systemd / docker).
    yaml_override = os.environ.get("QATLAS_CONFIG", "").strip()
    if yaml_override:
        return Path(yaml_override).expanduser().resolve(), "env_override_yaml"

    # Tier 2: legacy .env override. Emit deprecation warning since this
    # path is going away in v0.17.0; users should switch to
    # QATLAS_CONFIG=<file>.yaml.
    dotenv_override = os.environ.get("QATLAS_DOTENV", "").strip()
    if dotenv_override:
        logger.warning(
            "QATLAS_DOTENV is deprecated and will be removed in v0.17.0; "
            "migrate to YAML and use QATLAS_CONFIG=%s.yaml instead.",
            dotenv_override.rsplit(".", 1)[0] if "." in dotenv_override else dotenv_override,
        )
        return Path(dotenv_override).expanduser().resolve(), "env_override_dotenv_legacy"

    # Tier 3: XDG YAML — the new canonical location.
    yaml_xdg = user_config_yaml_path()
    if yaml_xdg.is_file():
        return yaml_xdg, "xdg_yaml"

    # Tier 4: XDG legacy .env — accepted with a one-time warn so the
    # next `qatlas config init` migrates it.
    dotenv_xdg = user_dotenv_path()
    if dotenv_xdg.is_file():
        logger.warning(
            "Found legacy %s; run `qatlas config init` to migrate to "
            "%s (the .env file will be renamed, not deleted). The .env "
            "fallback is removed in v0.17.0.",
            dotenv_xdg, user_config_yaml_path(),
        )
        return dotenv_xdg, "xdg_dotenv_legacy"

    return None, None


def resolve_dotenv_path() -> tuple[Path | None, str | None]:
    """DEPRECATED: thin back-compat wrapper around :func:`resolve_config_path`.

    Some callers still expect "where's the .env file" semantics. We keep
    this function alive through v0.16.x so they don't break, but it now
    only matches files ending in ``.env`` (i.e. the legacy paths).
    New code should call :func:`resolve_config_path` directly.

    Returns ``(None, None)`` when the resolved file is YAML — that's a
    deliberate signal to the caller "you're using legacy lookup, but
    the user has migrated already".
    """
    path, source = resolve_config_path()
    if path is None:
        return None, None
    if source in {"env_override_dotenv_legacy", "xdg_dotenv_legacy"}:
        # Translate the new source string back to the legacy values
        # callers used to expect (env_override / xdg) so we don't break
        # anything that pattern-matches on `source`.
        legacy_source = "env_override" if source == "env_override_dotenv_legacy" else "xdg"
        return path, legacy_source
    return None, None
