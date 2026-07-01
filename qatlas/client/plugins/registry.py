"""Discover + merge qatlas client plugins.

Which first-party plugins are **enabled is config-driven**, not hardcoded: they
ship with the package, but a builtin contributes commands only when its name is in
the config-driven enabled list (config.yaml ``plugins:`` — a list of names; absent →
the default set, ``lean`` only). Third-party plugins register via ``qatlas.plugins``
entry points and are always considered (gated by their own ``available()``). Only
plugins whose ``available()`` env-check passes contribute commands; a failing plugin
import is skipped (never breaks the CLI for unrelated commands).
"""

from __future__ import annotations

from .base import CommandSpec, QatlasPlugin

# The first-party (builtin) plugins that ship with the package: name -> (module, class).
# This catalog is inherent (they are package modules); *enablement* is config-driven
# via the `plugins` config list — retiring/adding a builtin is a config edit, not a
# code edit (mirrors qatlasd's config-driven plugin enablement; see ADR 0008).
_BUILTIN_CATALOG: dict[str, tuple[str, str]] = {
    "lean": ("qatlas.client.leanplugin.plugin", "LeanPlugin"),
    "claim": ("qatlas.client.claim.plugin", "ClaimPlugin"),
}

# Enabled when config.yaml has no ``plugins`` key. ``lean`` self-gates on a checkout
# (its available()); ``claim`` (the localhost claim-drafting WebUI) is opt-in — ADR
# 0008 moved the maintained flow to qatlas-lean — so it is NOT enabled by default.
_DEFAULT_ENABLED: tuple[str, ...] = ("lean",)


def _enabled_builtin_names() -> list[str]:
    """The config-driven set of enabled builtin plugin names (config.yaml ``plugins:``).

    ``None`` (key absent) → the default set; an explicit list (incl. ``[]``) is honored
    verbatim. Unknown names are dropped so a typo can never crash the CLI.
    """
    try:
        from qatlas.config import ServerConfig

        configured = ServerConfig.from_env().plugins
    except Exception:  # noqa: BLE001 - a config problem must never break the CLI
        configured = None
    names = list(_DEFAULT_ENABLED) if configured is None else list(configured)
    return [n for n in names if n in _BUILTIN_CATALOG]


def _builtin_plugins() -> list[QatlasPlugin]:
    import importlib

    plugins: list[QatlasPlugin] = []
    # Instantiate only the config-enabled builtins; lazily + defensively so a
    # broken/optional plugin never blocks the rest of the CLI.
    for name in _enabled_builtin_names():
        module_path, cls_name = _BUILTIN_CATALOG[name]
        try:
            mod = importlib.import_module(module_path)
            plugins.append(getattr(mod, cls_name)())
        except Exception:  # noqa: BLE001
            continue
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
