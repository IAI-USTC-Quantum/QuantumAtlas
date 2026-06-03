"""YAML config IO helpers for the ``qatlas`` user-level CLI.

v0.17.0 (this rewrite): client config is YAML-only. ``ServerConfig``
in ``qatlas/config.py`` reads ``~/.config/qatlas/config.yaml`` through
``pydantic_settings.YamlConfigSettingsSource``; this module only houses
the file-IO helpers used by ``qatlas config show / set / get / unset``.

The earlier ``.env`` → YAML migration logic is gone: v0.17.0 dropped
all env / dotenv support, so users upgrading from v0.16 or earlier
just need to copy their env values into the auto-created
``config.yaml``.
"""

from __future__ import annotations

import logging
import os
import stat
import tempfile
from pathlib import Path
from typing import Any, Dict

logger = logging.getLogger(__name__)


HEADER_COMMENT = """\
# QuantumAtlas client config (v0.17.0+)
#
# Managed by `qatlas config set/unset` — those commands REWRITE this
# file and discard hand-written comments (matches the gh / kubectl
# `config set` behaviour). If you want persistent notes, keep them in
# a wrapper script alongside this file.
#
# Resolution: this file is the ONLY config source. CLI flags / OS env
# vars / `QATLAS_DOTENV` / `QATLAS_CONFIG` are NOT consulted in v0.17.0+.
# Auto-created on first `qatlas` invocation; edit freely.
#
# Field reference: see the ServerConfig class in qatlas/config.py.
# Unknown keys are silently ignored.
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


def coerce_for_field(field_name: str, raw_value: str) -> Any:
    """Coerce a string value (e.g. from ``qatlas config set <key> <value>``)
    into the type the ``ServerConfig`` field expects.

    Handles ``bool`` / ``int`` / ``float``; everything else passes
    through as a string. Lazy-imports config to avoid circular imports.
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
