"""Subprocess passthrough to a qatlas-lean checkout's ``./qatlas-lean`` CLI."""

from __future__ import annotations

import os
import subprocess
import sys


def run_lean(lean_dir: str, argv: list[str]) -> int:
    """Run ``<lean_dir>/qatlas-lean <argv>`` in the checkout root.

    The launcher is a shebang'd Python script; we exec it directly when it is
    executable (so its own shebang selects the interpreter — identical to a
    plain ``./qatlas-lean`` invocation in that checkout), otherwise fall back to
    the current interpreter. stdio is inherited so the passthrough is fully
    interactive. cwd is the checkout root because qatlas-lean reads its config
    and var/ state relative to its own root.
    """
    launcher = os.path.join(lean_dir, "qatlas-lean")
    if not os.path.isfile(launcher):
        print(f"ERROR: lean launcher not found: {launcher}", file=sys.stderr)
        return 127
    if os.access(launcher, os.X_OK):
        cmd = [launcher, *argv]
    else:
        cmd = [sys.executable, launcher, *argv]
    try:
        return subprocess.run(cmd, cwd=lean_dir).returncode
    except FileNotFoundError:
        print(f"ERROR: lean launcher not found: {launcher}", file=sys.stderr)
        return 127
    except KeyboardInterrupt:
        return 130
