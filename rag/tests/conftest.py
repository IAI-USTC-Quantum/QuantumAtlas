"""pytest config for rag/tests/

Each test file may depend on one of the optional extras (`sidecar` /
`embed` / `ingest`). Without that extra installed, importing the test
module triggers a `ModuleNotFoundError` at collection time, which fails
the whole `pytest` run.

This conftest checks importability of each role's hot dep up-front and
adds the corresponding subdirectory to `collect_ignore` when it's
missing — so `pytest` on a sidecar-only install still runs the smoke
test and any other tests with no heavy deps, instead of erroring out.
"""
from __future__ import annotations

import importlib

collect_ignore: list[str] = []

# (subdir, sentinel module that gates the entire subdir)
_GATES = (
    ("ingest", "boto3"),  # ingest tests need boto3 / sqlalchemy / mistune
    ("embed", "torch"),  # embed tests need torch + FlagEmbedding
    ("sidecar", "fastapi"),  # sidecar tests need fastapi + qdrant-client
)

for subdir, sentinel in _GATES:
    try:
        importlib.import_module(sentinel)
    except ImportError:
        collect_ignore.append(subdir)
