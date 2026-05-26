"""Production smoke tests.

Hit one or more real, externally hosted QuantumAtlas instances to verify they
are alive and serving requests end-to-end. Designed for the nightly CI.

Targets are configured through environment variables:

  - ``QATLAS_SERVER_TARGETS`` (preferred): a comma- or newline-separated list
    of entries. Each entry is ``URL`` or ``URL|insecure``. The ``|insecure``
    suffix disables TLS verification for self-signed hosts (e.g. an IP).
  - ``QATLAS_SERVER_URL`` (legacy single host) + ``QATLAS_INSECURE=1`` still
    work as a fallback.

If nothing is configured, every test is skipped.

Run locally::

    QATLAS_SERVER_TARGETS=$'https://atlas.example.com\\nhttps://1.2.3.4|insecure' \\
        uv run pytest -m e2e tests/integration/test_production_smoke.py

Gated on ``e2e`` + ``network`` + ``slow`` markers so the default PR suite
never reaches it.
"""

from __future__ import annotations

import os
import time
from typing import NamedTuple

import pytest
import requests

pytestmark = [
    pytest.mark.e2e,
    pytest.mark.network,
    pytest.mark.slow,
]


SMOKE_ARXIV_ID = "quant-ph/9508027v1"
SMOKE_PARSER = "mineru"  # exercise the real MinerU integration in production


class Target(NamedTuple):
    url: str
    insecure: bool

    @property
    def verify(self) -> bool:
        return not self.insecure


def _parse_targets() -> list[Target]:
    raw = os.environ.get("QATLAS_SERVER_TARGETS", "").strip()
    if raw:
        targets: list[Target] = []
        for chunk in raw.replace("\n", ",").split(","):
            entry = chunk.strip()
            if not entry:
                continue
            if "|" in entry:
                url, *flags = entry.split("|")
                insecure = any(f.strip().lower() == "insecure" for f in flags)
            else:
                url, insecure = entry, False
            targets.append(Target(url.rstrip("/"), insecure))
        return targets

    legacy_url = os.environ.get("QATLAS_SERVER_URL") or os.environ.get(
        "PUBLIC_BASE_URL"
    )
    if legacy_url:
        legacy_insecure = os.environ.get("QATLAS_INSECURE", "").lower() in {
            "1",
            "true",
            "yes",
        }
        return [Target(legacy_url.rstrip("/"), legacy_insecure)]

    return []


_TARGETS = _parse_targets()
_PARAMS = _TARGETS or [Target("", False)]
_IDS = [t.url or "no-target-configured" for t in _PARAMS]


@pytest.fixture(params=_PARAMS, ids=_IDS)
def target(request) -> Target:
    if not _TARGETS:
        pytest.skip(
            "no production target configured "
            "(set QATLAS_SERVER_TARGETS or QATLAS_SERVER_URL)"
        )
    return request.param


def _poll_task(target: Target, task_id: str, *, timeout: float = 300.0) -> dict:
    deadline = time.time() + timeout
    last_payload: dict = {"status": "queued"}
    while time.time() < deadline:
        response = requests.get(
            f"{target.url}/api/ingest/{task_id}",
            timeout=15,
            verify=target.verify,
        )
        response.raise_for_status()
        last_payload = response.json()
        if last_payload.get("status") not in {"queued", "running", "pending"}:
            return last_payload
        time.sleep(2.0)
    raise TimeoutError(f"production server task did not finish: {last_payload}")


def test_production_health_endpoint_responds(target: Target):
    response = requests.get(
        f"{target.url}/health", timeout=15, verify=target.verify
    )
    assert response.status_code == 200, response.text


def test_production_exposes_canonical_ingest_stages(target: Target):
    response = requests.get(
        f"{target.url}/api/ingest/stages",
        timeout=15,
        verify=target.verify,
    )
    assert response.status_code == 200
    body = response.json()
    assert body["stages"] == ["fetch", "parse"], body


def test_production_can_fetch_a_known_paper(target: Target):
    """End-to-end: POST ingest, poll until terminal, assert fetch+parse succeeded."""
    response = requests.post(
        f"{target.url}/api/ingest/paper",
        json={
            "arxiv_id": SMOKE_ARXIV_ID,
            "parser": SMOKE_PARSER,
        },
        timeout=20,
        verify=target.verify,
    )
    response.raise_for_status()
    task_id = response.json()["task_id"]

    task = _poll_task(target, task_id, timeout=600)
    assert task["status"] == "succeeded", task
    assert task["steps"]["fetch"]["status"] == "succeeded", task
    assert task["steps"]["parse"]["status"] == "succeeded", task
    # ff-only invariant: production server must never expose downstream stages
    assert "extract" not in task["steps"]
    assert "wiki" not in task["steps"]
    assert "neo4j" not in task["steps"]
