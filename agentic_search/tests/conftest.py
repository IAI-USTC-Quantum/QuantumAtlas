"""pytest config for agentic_search/tests.

The base test run (no extras installed) must pass. ``agent`` tests need the
``agentic-search`` extra (pydantic-ai), so they are skipped when it's absent.
Live-network backend calls are marked ``network`` and excluded by CI's
``-m "not network and not e2e"``.
"""

from __future__ import annotations

import importlib

collect_ignore: list[str] = []

try:
    importlib.import_module("pydantic_ai")
except ImportError:
    collect_ignore.append("test_agent.py")
