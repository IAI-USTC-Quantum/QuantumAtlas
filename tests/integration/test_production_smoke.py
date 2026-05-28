"""Production smoke tests for the QuantumAtlas Go server (P12).

Targets are configured through ``QATLAS_SERVER_TARGETS`` — a comma- or
newline-separated list of ``URL`` / ``URL|insecure`` entries (see the
docstring of ``_parse_targets`` for the grammar). Without a configured
target, every test in this file is skipped.

Typical local invocation::

    QATLAS_SERVER_TARGETS=$'https://quantum-atlas.ai\\nhttps://47.102.36.175|insecure' \\
    QATLAS_TOKEN=qat_<your_long_lived_PAT> \\
        uv run pytest -m e2e tests/integration/test_production_smoke.py

``QATLAS_TOKEN`` accepts either:

  * a **PAT plaintext** (``qat_*``) — minted at https://<host>/pat or via
    ``qatlas-server pat mint`` on the server box; lives up to 365 days,
    so it is the recommended shape for unattended callers (CI secrets,
    cron jobs).
  * a **PocketBase user JWT** (anything else, typically a long ``eyJ...``
    string) — copy from the SPA's /token page; lives 14 days by default,
    so suitable for interactive use only.

The nightly CI workflow injects both targets plus a PAT-shaped
``QATLAS_TOKEN`` (no 14-day rotation chore). Without ``QATLAS_TOKEN`` the
single token-required test self-skips and the unauthenticated checks
still run.

What this exercises against each target:

  * GET /health and GET /api/server/info — alive checks (must report the
    Go engine and not the legacy Python server).
  * Public read endpoints — /api/stats, /api/pages, /api/search,
    /api/lint, /api/graph/stats — must respond 200 without auth.
  * Static SPA — GET / returns HTML that points at /assets/*.js (not the
    old /static/web/... path that broke after the vite.config.ts fix).
  * authGuard enforcement on write endpoints — POST /api/shares/ must
    return 401 with no Authorization, 401 with a wrong bearer, and (only
    when ``QATLAS_TOKEN`` is supplied with a real PAT or JWT) move on to
    validate the JSON body (400 "paths required").

The PAT-management contracts (mandatory expiry, scope enforcement,
sessionGuard-rejects-PAT, full lifecycle) are NOT exercised here — they
require a session JWT to bootstrap (POST /api/pat is gated by
sessionGuard, which by design refuses PAT auth so a leaked PAT can't
self-replicate). Putting a JWT in CI secrets means rotating every 14
days, which we explicitly reject as a long-running operational chore.
Those contracts are covered offline by ``internal/routes/pat_test.go``
(PocketBase test-app harness, runs on every push).

The /api/ingest/* endpoints intentionally do **not** appear here. The Go
server does not implement that surface (see HANDOFF.md §"Things
explicitly out of scope"). Tests for the legacy Python-only
``atlas.server.routers.api`` live in test_live_server_paper_flow.py etc.,
marked ``legacy`` so they no longer run by default.
"""

from __future__ import annotations

import os
import re
from typing import NamedTuple

import pytest
import requests
import urllib3

pytestmark = [
    pytest.mark.e2e,
    pytest.mark.network,
    pytest.mark.slow,
]


class Target(NamedTuple):
    url: str
    insecure: bool

    @property
    def verify(self) -> bool:
        return not self.insecure


def _parse_targets() -> list[Target]:
    """Parse QATLAS_SERVER_TARGETS into a list of Targets.

    Each entry is ``URL`` or ``URL|insecure`` (commas or newlines as
    separators). The ``|insecure`` suffix disables TLS verification, which
    is the common case for IP-based vhosts using Caddy's ``tls internal``
    self-signed certs (e.g. https://47.102.36.175 routed through Alibaba).
    Legacy fallback: ``QATLAS_SERVER_URL`` + optional ``QATLAS_INSECURE=1``.
    """
    raw = os.environ.get("QATLAS_SERVER_TARGETS", "").strip()
    if raw:
        targets: list[Target] = []
        for chunk in raw.replace("\r\n", "\n").replace("\n", ",").split(","):
            entry = chunk.strip()
            if not entry:
                continue
            if "|" in entry:
                url, *flags = entry.split("|")
                insecure = any(f.strip().lower() == "insecure" for f in flags)
            else:
                url, insecure = entry, False
            url = url.strip().rstrip("/")
            if not url.startswith(("http://", "https://")):
                raise ValueError(
                    f"QATLAS_SERVER_TARGETS entry missing http(s):// scheme: {url!r}"
                )
            targets.append(Target(url, insecure))
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
    # Hush the TLS warnings emitted once per insecure call so test output
    # stays readable when the Alibaba edge runs alongside RackNerd.
    if request.param.insecure:
        urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
    return request.param


def _get(target: Target, path: str, **kw) -> requests.Response:
    kw.setdefault("timeout", 15)
    kw.setdefault("verify", target.verify)
    return requests.get(f"{target.url}{path}", **kw)


def _post(target: Target, path: str, **kw) -> requests.Response:
    kw.setdefault("timeout", 15)
    kw.setdefault("verify", target.verify)
    return requests.post(f"{target.url}{path}", **kw)


# ---------------------------------------------------------------------------
# Liveness
# ---------------------------------------------------------------------------


def test_health_endpoint(target: Target):
    response = _get(target, "/health")
    assert response.status_code == 200, response.text
    body = response.json()
    assert body.get("status") == "healthy", body
    # Version string includes the '-go' suffix for the Go binary; if a
    # legacy Python server ever resurfaces under this URL we want to
    # notice immediately.
    assert str(body.get("version", "")).endswith("-go"), body


def test_server_info_reports_go_engine(target: Target):
    response = _get(target, "/api/server/info")
    assert response.status_code == 200, response.text
    body = response.json()
    assert body.get("engine") == "go+pocketbase", body
    assert body.get("mode") == "server", body


# ---------------------------------------------------------------------------
# Public read endpoints — no Authorization, must succeed
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "path",
    [
        "/api/stats",
        "/api/pages",
        "/api/lint",
        "/api/search?q=quantum",
        "/api/graph/stats",
    ],
)
def test_public_read_endpoints_open(target: Target, path: str):
    response = _get(target, path)
    assert response.status_code == 200, f"{path} -> {response.status_code}: {response.text[:200]}"


# ---------------------------------------------------------------------------
# Static SPA bundle
# ---------------------------------------------------------------------------


def test_spa_index_points_at_root_relative_assets(target: Target):
    response = _get(target, "/")
    assert response.status_code == 200, response.text
    body = response.text
    assert "<title>QuantumAtlas</title>" in body
    # vite.config.ts sets base='/', so asset URLs must NOT be prefixed with
    # the old caddy-security era '/static/web/'.
    assert 'src="/assets/' in body, body[:400]
    assert '/static/web/' not in body, body[:400]


def test_spa_asset_loads(target: Target):
    """Pluck the first /assets/*.js hash out of index.html, fetch it, and
    confirm Caddy / PocketBase actually serve the bundle (not redirect to
    login)."""
    index = _get(target, "/").text
    match = re.search(r'src="(/assets/index-[^"]+\.js)"', index)
    assert match, "could not find /assets/index-*.js in SPA index"
    asset = _get(target, match.group(1))
    assert asset.status_code == 200, asset.text[:200]
    ctype = asset.headers.get("Content-Type", "")
    assert "javascript" in ctype, ctype


# ---------------------------------------------------------------------------
# Auth gate — write endpoints
# ---------------------------------------------------------------------------


def test_write_endpoint_rejects_anonymous(target: Target):
    response = _post(
        target,
        "/api/shares/",
        json={"paths": ["x"]},
        headers={"Content-Type": "application/json"},
    )
    assert response.status_code == 401, response.text
    body = response.json()
    assert "authentication required" in body.get("detail", "").lower(), body


def test_write_endpoint_rejects_wrong_bearer(target: Target):
    response = _post(
        target,
        "/api/shares/",
        json={"paths": ["x"]},
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer not-a-real-token-zzz",
        },
    )
    assert response.status_code == 401, response.text


def test_write_endpoint_accepts_user_token(target: Target):
    """If ``QATLAS_TOKEN`` is set, prove the auth gate lets us through
    (400 from the body parser, not 401 from authGuard, not 403 from
    scopeGuard). Self-skips without a token.

    Accepts either a PAT (``qat_...``, recommended for nightly secrets
    because of the 365-day lifetime) or a PocketBase user JWT (anything
    else, typically rotated every 14 days from the SPA /token page).

    If the token is a PAT, it MUST have been minted with the
    ``shares:write`` scope — otherwise scopeGuard returns 403 and this
    test fails with a hint pointing the operator at the fix. Mint a
    properly-scoped PAT via https://<host>/pat or on the server box
    with ``qatlas-server pat mint --scopes shares:write``.
    """
    token = os.environ.get("QATLAS_TOKEN", "").strip()
    if not token:
        pytest.skip("QATLAS_TOKEN not set; cannot validate accepted path")

    response = _post(
        target,
        "/api/shares/",
        json={},  # missing paths key triggers the handler's own 400
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {token}",
        },
    )
    # 401 = the supplied token wasn't accepted (expired? wrong shape?).
    #       Re-fetch a JWT from /token, or mint a fresh PAT at /pat.
    # 403 = a PAT was accepted but lacks shares:write scope. Re-mint
    #       the PAT with --scopes shares:write.
    # 400 = passed both authGuard and scopeGuard, reached the handler's
    #       body validator. This is the happy path under test.
    if response.status_code == 403:
        pytest.fail(
            "PAT accepted by authGuard but rejected by scopeGuard (403). "
            "Your QATLAS_TOKEN PAT lacks the 'shares:write' scope. "
            f"Re-mint with --scopes shares:write. Body: {response.text}"
        )
    assert response.status_code == 400, (
        f"expected 400 (handler validation), got {response.status_code}: {response.text}"
    )
    body = response.json()
    assert body.get("detail") == "paths required", body


# ---------------------------------------------------------------------------
# PAT lifecycle / sessionGuard / scope enforcement / mandatory expiry
#
# These contracts USED to live here as live-server scenarios that
# bootstrapped a temporary PAT via a session JWT, exercised it against
# the production server, and revoked it. They moved to
# ``internal/routes/pat_test.go`` (PocketBase test-app harness) for
# two reasons:
#
#   1. The bootstrap step (POST /api/pat) is gated by sessionGuard,
#      which by design refuses PAT auth (a leaked PAT must not be
#      able to self-replicate — mirrors GitHub fine-grained PAT).
#      That makes the e2e tests require a session JWT, which means
#      rotating the CI secret every 14 days. We explicitly reject
#      that operational chore.
#
#   2. The contracts under test are HTTP-layer business rules
#      (validation, status codes, error detail shape) — exactly what
#      PocketBase's tests.NewTestApp() harness was built for. Running
#      them offline in CI on every push is strictly better than once
#      a night against a live server.
#
# The nightly's PAT-shaped QATLAS_TOKEN secret never expires for 365
# days, so unattended bootstrap is solved without touching this file.
# ---------------------------------------------------------------------------

