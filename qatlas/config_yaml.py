"""YAML config IO helpers for the ``qatlas`` user-level CLI.

v0.17.0 returned to a minimal surface: ``ServerConfig`` (in
``qatlas/config.py``) talks to YAML directly through
``pydantic_settings.YamlConfigSettingsSource``, and this module only
houses the helpers that operate on the YAML *as a file* — read, write,
atomic-replace, migrate from legacy ``.env``.

The earlier v0.16.0 ``YAML_TO_ENV`` map + ``flatten_yaml_to_env`` /
``bootstrap_env_from_yaml`` / hand-rolled set/get/unset are gone; that
logic is now derived automatically from ``ServerConfig.model_fields``
(via ``env_alias_to_field`` / ``field_to_env_alias`` in config.py).

Migration semantics (``.env`` → ``config.yaml``) live here because
they're a one-shot, file-system-level concern, not a configuration
schema concern.
"""

from __future__ import annotations

import logging
import os
import stat
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List

logger = logging.getLogger(__name__)


HEADER_COMMENT = """\
# QuantumAtlas client config
#
# Managed by `qatlas config init/set/unset` — those commands REWRITE
# this file and discard hand-written comments (matches the gh / kubectl
# `config set` behaviour). If you want persistent notes, keep them in a
# wrapper script that exports QATLAS_* / MINERU_* env vars instead.
#
# Resolution order (see `qatlas config path`):
#   1. CLI flag (--server-url / --token / --insecure / ...)
#   2. OS env var (QATLAS_*, MINERU_*, OPENAI_*, ANTHROPIC_*)
#   3. $QATLAS_CONFIG (explicit YAML override)
#   4. $QATLAS_DOTENV (legacy .env override; ⚠️ deprecated, removed in v0.18.0)
#   5. ~/.config/qatlas/config.yaml          ← this file
#   6. ~/.config/qatlas/.env                 ← legacy, auto-migrated by init
#   7. Built-in defaults defined in qatlas/config.py
#
# Use snake_case keys at the top level (server_url, token,
# mineru_api_token, openai_api_key, ...). The full field list is the
# ServerConfig class in qatlas/config.py — keys not declared there are
# silently ignored.
"""


def load_yaml_file(path: Path) -> Dict[str, Any]:
    """Read a YAML file and return its top-level dict (empty when
    empty file or missing). Raises ``ValueError`` on parse error so
    callers can surface a clear message instead of opaque YAML
    tracebacks.
    """
    if not path.is_file():
        return {}
    import yaml
    try:
        loaded = yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        raise ValueError(f"failed to parse {path}: {exc}") from exc
    if loaded is None:
        return {}
    if not isinstance(loaded, dict):
        raise ValueError(
            f"{path}: expected a top-level mapping (key: value structure), "
            f"got {type(loaded).__name__}"
        )
    return loaded


def dump_yaml(data: Dict[str, Any]) -> str:
    """Serialise a YAML dict back to text with the header comment.

    ``sort_keys=False`` so caller-supplied ordering survives the
    round-trip (PyYAML otherwise alphabetises, which buries the
    user-relevant ``server_url`` / ``token`` beneath ``anthropic_api_key``).
    """
    import yaml
    if data:
        body = yaml.safe_dump(data, sort_keys=False, allow_unicode=True, default_flow_style=False)
    else:
        body = ""
    return HEADER_COMMENT + "\n" + body


def write_yaml_atomic(path: Path, data: Dict[str, Any]) -> None:
    """Write the YAML dict to ``path`` via tempfile + atomic rename.

    Mode 0600 because the file may carry secrets (PAT / MinerU JWT /
    OpenAI API key). Tempfile lives in the same directory as the
    target so ``os.replace`` is atomic (POSIX: cross-filesystem rename
    falls back to copy, breaking atomicity).
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    content = dump_yaml(data).encode("utf-8")
    fd, tmp_name = tempfile.mkstemp(
        prefix=path.name + ".", suffix=".tmp", dir=str(path.parent)
    )
    try:
        os.write(fd, content)
        os.close(fd)
        os.chmod(tmp_name, stat.S_IRUSR | stat.S_IWUSR)
        os.replace(tmp_name, path)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def parse_dotenv(path: Path) -> Dict[str, str]:
    """Tiny dotenv parser used only by the .env → config.yaml
    migration path. We deliberately don't pull in python-dotenv here
    (already imported elsewhere) to keep the migration logic
    self-contained — anybody chasing a migration bug can read this
    file end-to-end without learning a third-party library.
    """
    out: Dict[str, str] = {}
    if not path.is_file():
        return out
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        out[key] = value
    return out


def migrate_dotenv_to_yaml(dotenv_path: Path, yaml_path: Path) -> List[str]:
    """One-shot migration: read legacy .env, write equivalent YAML,
    rename the .env to ``.env.migrated-from-vX.Y.Z.<timestamp>`` so
    nothing is silently lost.

    Returns the list of env-var names that had no corresponding
    ``ServerConfig`` field and were therefore dropped (caller should
    warn the user). The renamed backup file is kept indefinitely —
    rollback path is "rename backup back to .env, delete config.yaml,
    downgrade qatlas".

    Performs the env→field mapping via :func:`qatlas.config.env_alias_to_field`,
    so the migration automatically tracks any future schema additions
    without touching this module.
    """
    from qatlas.config import env_alias_to_field

    env_pairs = parse_dotenv(dotenv_path)
    if not env_pairs:
        return []

    yaml_dict: Dict[str, Any] = {}
    dropped: List[str] = []
    for env_name, raw_value in env_pairs.items():
        if raw_value == "":
            continue
        field = env_alias_to_field(env_name)
        if field is None:
            dropped.append(env_name)
            continue
        yaml_dict[field] = _coerce_for_field(field, raw_value)

    yaml_path.parent.mkdir(parents=True, exist_ok=True)
    write_yaml_atomic(yaml_path, yaml_dict)

    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup = dotenv_path.with_name(f".env.migrated-from-v0.17.0.{timestamp}")
    dotenv_path.rename(backup)
    print(
        f"Migrated {dotenv_path.name} -> {yaml_path.name}; "
        f"original kept as {backup.name} for safety.",
        file=sys.stderr,
    )
    return dropped


def _coerce_for_field(field_name: str, raw_value: str) -> Any:
    """Coerce a string value from a .env file into the type the
    ``ServerConfig`` field expects. Lazy-imports config to avoid a
    circular import on module load.

    Only handles the field types we actually carry today (bool, int,
    float). Anything else passes through as a string.
    """
    from qatlas.config import ServerConfig

    info = ServerConfig.model_fields.get(field_name)
    if info is None:
        return raw_value
    annotation = info.annotation
    import typing
    if typing.get_origin(annotation) is typing.Union:
        args = [a for a in typing.get_args(annotation) if a is not type(None)]
        annotation = args[0] if args else annotation
    if annotation is bool:
        return raw_value.strip().lower() in {"1", "true", "yes", "on"}
    if annotation is int:
        try:
            return int(raw_value)
        except (TypeError, ValueError):
            return raw_value
    if annotation is float:
        try:
            return float(raw_value)
        except (TypeError, ValueError):
            return raw_value
    return raw_value
