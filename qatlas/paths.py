"""Default filesystem locations for the ``qatlas`` user-level CLI.

Cross-platform paths via the standard ``platformdirs`` library, so
users find config / state / cache in the spot their OS conventionally
expects:

| Platform | user_config_dir                  | user_state_dir                                       |
| -------- | -------------------------------- | ---------------------------------------------------- |
| Linux    | ``$XDG_CONFIG_HOME/qatlas/``     | ``$XDG_STATE_HOME/qatlas/``                          |
|          | (default ``~/.config/qatlas/``)  | (default ``~/.local/state/qatlas/``)                 |
| macOS    | ``~/Library/Application Support/qatlas/`` | ``~/Library/Application Support/qatlas/``   |
| Windows  | ``%APPDATA%\\qatlas\\``            | ``%LOCALAPPDATA%\\qatlas\\``                           |

(``platformdirs`` honors ``XDG_CONFIG_HOME`` / ``XDG_STATE_HOME`` /
``XDG_CACHE_HOME`` on Linux per the freedesktop spec — Linux behavior
is unchanged from the v0.17.0a0 hand-rolled implementation.)

We pass ``appauthor=False`` so the Windows path is the clean
``%APPDATA%\\qatlas\\`` rather than ``%APPDATA%\\qatlas\\qatlas\\``
(default ``platformdirs`` doubles the app name there for vendor
namespacing, irrelevant for a single-author project).

No ``QATLAS_CONFIG`` / ``QATLAS_DOTENV`` overrides — the file location
is fixed per ``platformdirs``. Users move it via the platform-native
environment mechanism if needed (``XDG_CONFIG_HOME`` on Linux,
``APPDATA`` on Windows; macOS users typically just symlink).

Intentionally NOT cached — every call re-asks platformdirs so tests
can monkey-patch ``HOME`` / ``XDG_CONFIG_HOME`` / ``APPDATA`` without
import order weirdness.
"""
from __future__ import annotations

import logging
from pathlib import Path

import platformdirs

_APP = "qatlas"

logger = logging.getLogger(__name__)


def user_config_dir() -> Path:
    """The directory for our user-level config files.

    Uses :func:`platformdirs.user_config_dir` so the right thing
    happens on every supported OS (see module docstring table).
    """
    return Path(platformdirs.user_config_dir(_APP, appauthor=False))


def user_state_dir() -> Path:
    """The directory for our user-level state files (PAT cache, etc.)."""
    return Path(platformdirs.user_state_dir(_APP, appauthor=False))


def user_cache_dir() -> Path:
    """The directory for our user-level cache files."""
    return Path(platformdirs.user_cache_dir(_APP, appauthor=False))


def user_config_yaml_path() -> Path:
    """The canonical ``config.yaml`` path for ``qatlas`` users.

    Returned unconditionally — caller decides whether the file actually
    exists. On first read the loader auto-creates a default template
    here; ``qatlas config show / set / get / unset`` read/write here.
    """
    return user_config_dir() / "config.yaml"
