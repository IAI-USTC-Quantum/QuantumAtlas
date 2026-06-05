"""pytest config for rag/tests/

Currently only the embed worker is Python, so we just gate the (none-
existent yet) embed test subdir on torch importability. The smoke test
itself doesn't need any optional deps.
"""
from __future__ import annotations

import importlib

collect_ignore: list[str] = []

try:
    importlib.import_module("torch")
except ImportError:
    collect_ignore.append("embed")
