"""Default filesystem locations for the ``qatlas`` user-level CLI.

Resolution order for the user config file:

1. ``QATLAS_DOTENV`` env var — explicit override (mostly for systemd
   units on the server side that load a project-specific .env).
2. ``$XDG_CONFIG_HOME/qatlas/.env`` (default
   ``~/.config/qatlas/.env``) — preferred location for
   ``uv tool install`` users.
3. ``./.env`` in the current working directory — **legacy fallback**
   only, with a deprecation warning. Kept so users who developed
   against an editable install in ``~/TiMidlY-projects/QuantumAtlas/``
   keep working until they migrate.

Mirrors the portal-mcp-server pattern (which itself cites ssh / git /
gh / docker / kubectl / rclone / borg / mpv / age as the precedent for
"user-level CLI = XDG, ignore cwd"). The cwd fallback is **only** kept
for migration; new docs should always point at the XDG path.

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


def user_dotenv_path() -> Path:
    """The canonical ``.env`` file path for ``qatlas`` users."""
    return user_config_dir() / ".env"


def resolve_dotenv_path() -> tuple[Path | None, str | None]:
    """Locate the .env file to load, in priority order.

    Returns ``(path, source)`` where ``source`` describes which rule
    matched:

      * ``"env_override"`` — ``QATLAS_DOTENV`` env var
      * ``"xdg"``          — ``~/.config/qatlas/.env``
      * ``"cwd_legacy"``   — ``./.env`` in current working directory
                             (the caller SHOULD emit a deprecation
                             warning recommending the XDG location)

    Returns ``(None, None)`` when nothing is found — the caller should
    fall back to OS env vars only (which is fine because every
    settings field has an ``QATLAS_*`` env alias).

    A path is "found" if and only if the file exists. We intentionally
    don't probe inside zip / wheel files: this CLI assumes a real
    filesystem.
    """
    override = os.environ.get("QATLAS_DOTENV", "").strip()
    if override:
        candidate = Path(override).expanduser().resolve()
        # Honour the override even if the file is missing — caller
        # presumably wants the explicit failure mode rather than silent
        # XDG fallback.
        return candidate, "env_override"

    xdg = user_dotenv_path()
    if xdg.is_file():
        return xdg, "xdg"

    cwd = Path.cwd() / ".env"
    if cwd.is_file():
        return cwd, "cwd_legacy"

    return None, None
