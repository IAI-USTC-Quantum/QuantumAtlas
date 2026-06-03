"""Default filesystem locations for the ``qatlas`` user-level CLI.

Self-contained, single-source-of-truth path resolution (v0.17.0+):

* User config:  ``~/.config/qatlas/config.yaml``
* User state:   ``~/.local/state/qatlas/``
* User cache:   ``~/.cache/qatlas/``

All honor the XDG Base Directory spec via ``XDG_CONFIG_HOME`` /
``XDG_STATE_HOME`` / ``XDG_CACHE_HOME`` env vars; non-absolute values
are ignored per the spec.

No ``QATLAS_CONFIG`` / ``QATLAS_DOTENV`` overrides — the file location
is fixed. Users move it via ``XDG_CONFIG_HOME=...`` (standard
freedesktop mechanism) if needed.

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
    (treated as unset).
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
    """The directory for our user-level state files."""
    return xdg_state_home() / _APP


def user_config_yaml_path() -> Path:
    """The canonical ``config.yaml`` path for ``qatlas`` users.

    Returned unconditionally — caller decides whether the file actually
    exists. On first read the loader auto-creates a default template
    here; ``qatlas config show / set / get / unset`` read/write here.
    """
    return user_config_dir() / "config.yaml"
