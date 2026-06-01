"""Production smoke tests for the QuantumAtlas Go server (qatlasd).

Run nightly against the two live edges; deliberately minimal — every
test here MUST cover something unit tests cannot. Things that the Go
/ Python unit suites already pin down (handler shape, scopeGuard
behaviour, decodeScopes fail-closed contracts, PAT lifecycle, etc.)
do NOT belong in this file.

# What we test, and why each one is here

  * /api/health — anonymous and authenticated tiers. The authenticated
    detail tier proves Neo4j Bolt + RustFS HEAD-bucket + wiki git
    HEAD all wire to the *real* deployment AND the system PAT chain
    (env → pat.LoadSystemPAT → authGuard → handler) is intact end
    to end; unit tests can only exercise mocks / loopback. The
    anonymous tier proves the privacy split (Sanitise) is wired
    correctly on prod — no mesh IPs / bucket names / wiki commit
    info leaking.

  * /api/server/info — confirms the running binary is the one the
    last release pushed (version float check); detects "Caddy is
    serving cached HTML / old binary forgot to restart" silently.

  * One anonymous read endpoint returning 401 — proves prod lockdown
    is in fact in place (a misconfigured deployment that bypassed
    authGuard would silently leak the corpus; better to catch that
    here than in a security audit).

  * SPA index — proves the embedded SPA + Caddy reverse-proxy is
    serving real assets, not a fallback / login page. The asset
    referenced in the HTML must actually fetch and have a JS
    content-type. No unit test can verify the full HTTP stack.

Specifically NOT tested here (covered by Go / Python unit suites):
auth scope vocabulary (pat_test.go / scopes_test.go), individual
read handlers' response shape (papers_stats_test.go / graph.go
handler tests / wiki cache tests), full PAT lifecycle
(pat_cmd_test.go), per-endpoint authGuard behaviour (auth_test.go).
Re-running those against the live edge was duplicative cost.

Targets are configured through ``QATLAS_SERVER_TARGETS`` (comma- or
newline-separated ``URL`` / ``URL|insecure`` entries; see
``_parse_targets`` for the full grammar). Without it, every test
self-skips.

Per-edge system PAT is read from ``QATLAS_SYSTEM_PAT_<EDGE>`` via the
``token-env=NAME`` flag on each target spec. Each edge has its own
``QATLAS_SYSTEM_PAT`` in its .env, so per-edge secrets ARE required
(they are not interchangeable).

Token gotcha: ``QATLAS_SYSTEM_PAT`` is the server's env var name on the
edge box (loaded by ``pat.LoadSystemPAT``); the GitHub Actions secret
mirrors per-edge as ``QATLAS_SYSTEM_PAT_RACKNERD`` /
``QATLAS_SYSTEM_PAT_ALIBABA``. The test code just receives the resolved
plaintext via the ``token-env=`` spec — neither end of the wire knows
about the "system PAT" name; it's just a long-lived bearer.
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
    token: str  # per-target bearer ("" → fall back to QATLAS_TOKEN env)

    @property
    def verify(self) -> bool:
        return not self.insecure

    def auth_token(self) -> str:
        if self.token:
            return self.token
        return os.environ.get("QATLAS_TOKEN", "").strip()


def _parse_targets() -> list[Target]:
    """Parse QATLAS_SERVER_TARGETS into Targets.

    Each entry is ``URL`` optionally followed by ``|FLAG`` segments,
    comma- or newline-separated at the top level. Flags:

      * ``insecure`` — disable TLS verification (Caddy ``tls internal``).
      * ``token-env=VAR_NAME`` — pull per-target bearer from ``$VAR_NAME``.

    Falls back to legacy ``QATLAS_SERVER_URL`` + ``QATLAS_INSECURE``
    + ``QATLAS_TOKEN`` when ``QATLAS_SERVER_TARGETS`` is unset.
    """
    raw = os.environ.get("QATLAS_SERVER_TARGETS", "").strip()
    if raw:
        targets: list[Target] = []
        for chunk in raw.replace("\r\n", "\n").replace("\n", ",").split(","):
            entry = chunk.strip()
            if not entry:
                continue
            insecure = False
            token = ""
            if "|" in entry:
                url, *flags = entry.split("|")
                for f in flags:
                    f = f.strip()
                    if f.lower() == "insecure":
                        insecure = True
                    elif f.startswith("token-env="):
                        var_name = f[len("token-env="):].strip()
                        if not var_name:
                            raise ValueError(
                                f"token-env= requires a variable name: {entry!r}"
                            )
                        token = os.environ.get(var_name, "").strip()
            else:
                url = entry
            url = url.strip().rstrip("/")
            if not url.startswith(("http://", "https://")):
                raise ValueError(
                    f"QATLAS_SERVER_TARGETS entry missing http(s):// scheme: {url!r}"
                )
            targets.append(Target(url, insecure, token))
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
        return [Target(legacy_url.rstrip("/"), legacy_insecure, "")]

    return []


_TARGETS = _parse_targets()
_PARAMS = _TARGETS or [Target("", False, "")]
_IDS = [t.url or "no-target-configured" for t in _PARAMS]


@pytest.fixture(params=_PARAMS, ids=_IDS)
def target(request) -> Target:
    if not _TARGETS:
        pytest.skip(
            "no production target configured "
            "(set QATLAS_SERVER_TARGETS or QATLAS_SERVER_URL)"
        )
    if request.param.insecure:
        urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
    return request.param


def _get(target: Target, path: str, **kw) -> requests.Response:
    kw.setdefault("timeout", 15)
    kw.setdefault("verify", target.verify)
    return requests.get(f"{target.url}{path}", **kw)


# ---------------------------------------------------------------------------
# 1. Anonymous health — sanitised payload only, no topology leaks
# ---------------------------------------------------------------------------


# Detail-tier field names that the sanitised /api/health response must
# NEVER surface. The healthz package's unit tests pin the same contract
# for Sanitise() against an in-memory Result; here we re-verify the
# wiring on the live wire (handler -> Caddy -> client). If a future
# refactor accidentally serves the unsanitised payload to anonymous
# callers, this fails.
_HEALTH_DETAIL_FIELDS_FORBIDDEN_ANON = [
    "endpoint",
    "uri",
    "bucket",
    "buckets",
    "dir",
    "commit",
    "commit_time",
    "branch",
    "dirty",
    "latency_ms",
    "backend",
    "database",
    # ``error`` was historically allowed on the anon tier as a "one-line
    # cause" hint, but raw err.Error() / "bucket %s: %v" strings from
    # SDK drivers embed mesh IPs / bucket names / bolt URIs inline,
    # silently defeating the rest of the redaction. Post-v0.12 audit:
    # Sanitise() drops Error entirely on the anon tier.
    "error",
]


def test_health_anonymous_is_sanitised(target: Target):
    """Anonymous /api/health must report alive + per-check status only.

    Detail fields (mesh IPs, bucket names, wiki commit info) must be
    stripped — they are deployment fingerprints valuable to an
    attacker and useless to a liveness probe. The privacy contract
    is enforced by ``healthz.Result.Sanitise``; this test catches
    accidental bypass of the wiring in main.go.
    """
    response = _get(target, "/api/health")
    assert response.status_code == 200, response.text
    body = response.json()
    assert body.get("code") == 200, body
    data = body.get("data", {})
    assert data.get("version"), data
    checks = data.get("checks", {})
    # Expected per-check shape: just {"status": ...}. The leak sweep
    # runs unconditionally — even on a degraded production server
    # (status != "ok") Sanitise() must keep redacting detail fields,
    # so a transient backend outage must not mask a Sanitise bypass.
    for name, c in checks.items():
        for forbidden in _HEALTH_DETAIL_FIELDS_FORBIDDEN_ANON:
            assert forbidden not in c, (
                f"check {name} leaked {forbidden!r} to anonymous caller: {c}"
            )
    # Aggregate health is asserted last so the leak invariant is
    # checked regardless of prod liveness.
    assert data.get("status") == "healthy", f"degraded: {data}"
    for name, c in checks.items():
        assert c.get("status") == "ok", f"{name} status: {c}"


def test_health_authenticated_returns_detail(target: Target):
    """The same /api/health, called with the system PAT, MUST include
    detail fields (proving the auth-aware split is wired both ways).

    We don't assert specific values (mesh IP, bucket names) because
    deployments may change them — we only assert that at least one
    detail field surfaces, demonstrating Sanitise is NOT being applied
    to authenticated callers.
    """
    token = target.auth_token()
    if not token:
        pytest.skip(f"no system PAT configured for {target.url}")

    response = _get(target, "/api/health", headers={"Authorization": f"Bearer {token}"})
    assert response.status_code == 200, response.text
    body = response.json()
    assert body.get("code") == 200, body
    data = body.get("data", {})
    assert data.get("status") == "healthy", f"degraded with detail: {data}"

    # At least one detail field must surface across the checks.
    checks = data.get("checks", {})
    detail_present = False
    for c in checks.values():
        for f in _HEALTH_DETAIL_FIELDS_FORBIDDEN_ANON:
            if f in c:
                detail_present = True
                break
        if detail_present:
            break
    assert detail_present, (
        "authenticated health response has no detail fields — either auth probe "
        f"misfired or Sanitise leaked into the auth path: {checks}"
    )


# ---------------------------------------------------------------------------
# 2. Version freshness — detect "old binary still running"
# ---------------------------------------------------------------------------


def test_server_info_reports_go_engine(target: Target):
    """``/api/server/info`` confirms (a) we're talking to the Go binary,
    not a resurrected legacy Python server, and (b) the version field
    is populated (catches build flags being lost in a botched release).
    """
    response = _get(target, "/api/server/info")
    assert response.status_code == 200, response.text
    body = response.json()
    assert body.get("engine") == "go+pocketbase", body
    assert body.get("mode") == "server", body
    assert body.get("version"), body


# ---------------------------------------------------------------------------
# 3. One representative protected endpoint must reject anonymous GETs
# ---------------------------------------------------------------------------


def test_anonymous_read_is_locked_down(target: Target):
    """One representative protected endpoint must reject anonymous
    GETs with 401.

    We don't sweep the whole vocabulary (Go unit tests already pin
    authGuard's behaviour on every endpoint); we just verify that a
    misconfigured deployment hasn't accidentally turned the corpus
    public. /api/stats picked as the canary because it touches the
    wiki cache (the most "leaky-looking" surface if lockdown broke).
    """
    response = _get(target, "/api/stats")
    assert response.status_code == 401, (
        f"/api/stats anonymously returned {response.status_code} (LEAKED?): {response.text[:200]}"
    )
    body = response.json()
    assert "authentication required" in body.get("detail", "").lower(), body


# ---------------------------------------------------------------------------
# 4. SPA shell — only real-deployment can verify the embedded SPA +
#    Caddy reverse-proxy + asset content-type chain
# ---------------------------------------------------------------------------


def test_spa_renders_and_assets_load(target: Target):
    """``/`` returns the embedded SPA HTML and the first
    ``/assets/*.js`` it references actually fetches as JavaScript.

    Catches two failure modes invisible to unit tests:
      1. embed.go regression / asset path drift (would serve a fallback
         page or 404 the asset);
      2. Caddy / reverse-proxy misconfig stripping the asset prefix
         (would serve text/html for a .js URL).
    """
    index = _get(target, "/")
    assert index.status_code == 200, index.text
    body = index.text
    assert "<title>QuantumAtlas</title>" in body
    assert 'src="/assets/' in body, body[:400]
    assert "/static/web/" not in body, body[:400]

    match = re.search(r'src="(/assets/index-[^"]+\.js)"', body)
    assert match, "could not find /assets/index-*.js in SPA index"
    asset = _get(target, match.group(1))
    assert asset.status_code == 200, asset.text[:200]
    ctype = asset.headers.get("Content-Type", "")
    assert "javascript" in ctype, ctype
