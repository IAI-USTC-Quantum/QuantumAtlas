"""Discover + merge qatlas client plugins.

Resolution order: built-in plugins, then ``qatlas.plugins`` entry points. Only
plugins whose ``available()`` is true contribute commands. A failing plugin
import is skipped (never breaks the CLI for unrelated commands).
"""

from __future__ import annotations

from .base import CommandSpec, QatlasPlugin


def _builtin_plugins() -> list[QatlasPlugin]:
    plugins: list[QatlasPlugin] = []
    # Imported lazily + defensively so a broken/optional plugin never blocks
    # the rest of the CLI.
    try:
        from qatlas.client.leanplugin.plugin import LeanPlugin

        plugins.append(LeanPlugin())
    except Exception:  # noqa: BLE001
        pass
    return plugins


def _entrypoint_plugins() -> list[QatlasPlugin]:
    out: list[QatlasPlugin] = []
    try:
        from importlib.metadata import entry_points

        eps = entry_points()
        group = eps.select(group="qatlas.plugins") if hasattr(eps, "select") else eps.get("qatlas.plugins", [])  # type: ignore[union-attr]
        for ep in group:
            try:
                factory = ep.load()
                plugin = factory() if callable(factory) else factory
                if isinstance(plugin, QatlasPlugin):
                    out.append(plugin)
            except Exception:  # noqa: BLE001
                continue
    except Exception:  # noqa: BLE001
        pass
    return out


def active_plugins() -> list[QatlasPlugin]:
    """Built-in + entry-point plugins that report themselves available."""
    plugins = _builtin_plugins() + _entrypoint_plugins()
    out: list[QatlasPlugin] = []
    for p in plugins:
        try:
            if p.available():
                out.append(p)
        except Exception:  # noqa: BLE001
            continue
    return out


def top_level_commands() -> dict[str, CommandSpec]:
    """Merged ``qatlas <name>`` commands from all active plugins."""
    out: dict[str, CommandSpec] = {}
    for p in active_plugins():
        try:
            out.update(p.top_level_commands())
        except Exception:  # noqa: BLE001
            continue
    return out


def contrib_subcommands() -> dict[str, CommandSpec]:
    """Merged ``qatlas contrib <name>`` subcommands from all active plugins."""
    out: dict[str, CommandSpec] = {}
    for p in active_plugins():
        try:
            out.update(p.contrib_subcommands())
        except Exception:  # noqa: BLE001
            continue
    return out
