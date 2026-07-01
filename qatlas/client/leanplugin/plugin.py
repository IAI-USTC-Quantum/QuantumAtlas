"""``qatlas lean <subcommand>`` — passthrough to a local qatlas-lean checkout."""

from __future__ import annotations

import os
import sys

from qatlas.client.plugins.base import CommandSpec, QatlasPlugin


class LeanPlugin(QatlasPlugin):
    name = "lean"

    def available(self) -> bool:
        return lean_dir() is not None

    def top_level_commands(self) -> dict[str, CommandSpec]:
        return {
            "lean": CommandSpec(
                _run,
                "Drive a local qatlas-lean checkout's CLI "
                "(start / daemon / dashboard / contrib claim / status …)",
            )
        }


def lean_dir() -> str | None:
    """Resolve the configured qatlas-lean checkout, or None.

    Precedence: env QATLAS_LEAN_DIR, then config.yaml ``lean_dir``. The path
    must exist and contain the ``qatlas-lean`` launcher.
    """
    candidate = os.getenv("QATLAS_LEAN_DIR")
    if not candidate:
        try:
            from qatlas.config import ServerConfig

            candidate = ServerConfig.from_env().lean_dir
        except Exception:  # noqa: BLE001
            candidate = None
    if not candidate:
        return None
    path = os.path.abspath(os.path.expanduser(candidate))
    launcher = os.path.join(path, "qatlas-lean")
    return path if os.path.isfile(launcher) else None


def _run(argv: list[str]) -> int:
    from qatlas.client.leanplugin.runner import run_lean

    path = lean_dir()
    if path is None:
        print(
            "ERROR: no qatlas-lean checkout configured.\n"
            "Set it with:  qatlas config set lean_dir <path-to-checkout>\n"
            "or:           export QATLAS_LEAN_DIR=<path-to-checkout>\n"
            "The path must contain the `qatlas-lean` launcher.",
            file=sys.stderr,
        )
        return 2
    if not argv or argv[0] in ("-h", "--help"):
        print(
            "qatlas lean — passthrough to the qatlas-lean formalization daemon CLI\n\n"
            f"  checkout: {path}\n\n"
            "Usage:  qatlas lean <subcommand> [args...]\n"
            "Run `qatlas lean help` for the full qatlas-lean command surface "
            "(start / daemon / dashboard / contrib claim / status / logs / …).",
        )
        if not argv:
            return 0
    return run_lean(path, argv)
