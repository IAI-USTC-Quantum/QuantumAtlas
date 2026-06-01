"""MkDocs hook: bridge the swaggo-generated OpenAPI spec into the docs site.

The single source of truth for the API spec is ``internal/apidocs/swagger.json``,
generated from the swaggo annotations by ``pixi run swagger`` and compiled into
the qatlasd binary (served live at ``/swagger``). To avoid committing a
second copy that could silently drift, we do NOT keep a copy under ``docs/``.
Instead this hook copies the committed spec into ``docs/reference/openapi.json``
at build time so the ``mkdocs-swagger-ui-tag`` plugin can render it, then removes
it afterwards. ``docs/reference/openapi.json`` is gitignored.

Read the Docs runs ``mkdocs build`` after a plain checkout (no Go toolchain), so
this relies only on the committed JSON — never on regenerating it here.
"""

from __future__ import annotations

import shutil
from pathlib import Path

# Resolved at runtime from the mkdocs config file path so the hook works
# regardless of the process cwd (RTD, pixi, local serve).
_SPEC_REL = Path("internal/apidocs/swagger.json")
_DEST_REL = Path("docs/reference/openapi.json")


def _paths(config) -> tuple[Path, Path]:
    root = Path(config["config_file_path"]).resolve().parent
    return root / _SPEC_REL, root / _DEST_REL


def on_pre_build(config, **kwargs) -> None:
    spec, dest = _paths(config)
    if not spec.is_file():
        raise FileNotFoundError(
            f"OpenAPI spec not found at {spec}. Run `pixi run swagger` to "
            "regenerate it before building the docs."
        )
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(spec, dest)


def _cleanup(config) -> None:
    _, dest = _paths(config)
    dest.unlink(missing_ok=True)


def on_post_build(config, **kwargs) -> None:
    _cleanup(config)


def on_build_error(error, **kwargs) -> None:  # pragma: no cover - best effort
    # error hook receives no config; remove the well-known relative path.
    Path(_DEST_REL).unlink(missing_ok=True)
