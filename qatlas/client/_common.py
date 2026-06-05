"""Shared helpers for QuantumAtlas client-side CLIs.

Configuration source (v0.17.0+): every value comes from
``~/.config/qatlas/config.yaml`` via ``qatlas.config.ServerConfig``.
No CLI flag, no OS env, no ``QATLAS_DOTENV``. ``qatlas auth login``
still maintains a separate per-host token file
(``~/.config/qatlas/hosts.yml``) used as the last-resort token
fallback when ``config.yaml`` has no ``token:``.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from typing import Any

import requests

from qatlas import __version__ as _CLIENT_VERSION
from qatlas.config import ServerConfig


def _client_config() -> ServerConfig:
    """Build a fresh ServerConfig view of the current YAML.

    Cheap (~ms), and re-reading per call means a `qatlas config set`
    in between two `qatlas papers ...` invocations is picked up
    immediately without process state.
    """
    return ServerConfig.from_env()


def default_base_url() -> str:
    """Resolve the server base URL from ``config.yaml`` ``server_url``.

    Falls back to ``http://127.0.0.1:8090`` (PocketBase default) when
    unset so ``qatlas`` doesn't fatal on first run without a configured
    server — useful for local dev.
    """
    cfg = _client_config()
    server_url = cfg.get_server_url()
    if server_url:
        return server_url
    return "http://127.0.0.1:8090"


def base_url_from_args(args: argparse.Namespace) -> str:
    """Return the config-file server_url.

    ``args`` is accepted for back-compat with the v0.16 signature; no
    field is read from it anymore. Subcommands that need a different
    server should set up a separate ``XDG_CONFIG_HOME``-isolated
    config.
    """
    return default_base_url()


def request_verify(args: argparse.Namespace) -> bool:
    """Honor ``insecure: true`` in config.yaml to disable TLS verification.

    Same one-shot warning behaviour as before. ``args`` kept for
    signature back-compat.
    """
    cfg = _client_config()
    if not cfg.insecure:
        return True
    if not getattr(args, "_insecure_warning_shown", False):
        requests.packages.urllib3.disable_warnings(  # type: ignore[attr-defined]
            category=requests.packages.urllib3.exceptions.InsecureRequestWarning
        )
        print("Warning: TLS certificate verification is disabled.", file=sys.stderr)
        args._insecure_warning_shown = True
    return False


def resolve_token(args: argparse.Namespace) -> str:
    """Resolve the bearer credential from ``~/.config/qatlas/hosts.yml``.

    The store is populated by ``qatlas auth login`` (browser OAuth /
    ``--with-token`` from stdin). ``server_url:`` in config.yaml /
    ``--server-url`` CLI flag picks WHICH host's token to use.

    ``args`` kept for signature back-compat (used to honour
    ``--token`` CLI flag, removed in v0.17.0; config.yaml ``token:``
    field removed in v0.19.0 — it silently shadowed all per-host
    tokens in hosts.yml).

    An empty return value means no Authorization header will be set;
    the server then either serves open reads or replies 401 for write
    endpoints. The 401 body always points the user at ``/pat``
    (top-level redirect to ``/<lang>/pat``, defined in
    ``web/src/routes/pat.tsx``) regardless of language.
    """
    try:
        from qatlas.client.auth import get_stored_token  # local import to avoid cycle

        return get_stored_token(default_base_url())
    except Exception:
        # Defensive: never let a config-file glitch break unrelated commands.
        return ""


def auth_headers(args: argparse.Namespace) -> dict[str, str]:
    """Build the Authorization header for a CLI request.

    Returns an empty dict when no token is configured so callers can
    safely splat ``{**auth_headers(args), ...other...}``.
    """
    token = resolve_token(args)
    if not token:
        return {}
    return {"Authorization": f"Bearer {token}"}


def print_json(payload: dict[str, Any]) -> None:
    print(json.dumps(payload, ensure_ascii=False, indent=2))


def add_common_http_args(parser: argparse.ArgumentParser) -> None:
    """Register the shared ``--request-timeout`` flag.

    v0.17.0 removed ``--base-url`` / ``--token`` / ``--insecure``
    flags — those fields now live exclusively in
    ``~/.config/qatlas/config.yaml``. ``--request-timeout`` is kept
    because it's an in-call ergonomic knob (raise the timeout for a
    slow MinerU poll), not a persistent config concern.
    """
    parser.add_argument(
        "--request-timeout",
        type=float,
        default=120.0,
        help="HTTP request timeout in seconds (per-call override).",
    )


def run_with_request_errors(func, *args, **kwargs) -> int:
    """Convert ValueError / RequestException into standard CLI exit codes."""
    try:
        return func(*args, **kwargs)
    except ValueError as exc:
        print(f"Invalid input: {exc}", file=sys.stderr)
        return 2
    except requests.RequestException as exc:
        print(f"Request failed: {exc}", file=sys.stderr)
        return 1


# ---------------------------------------------------------------------------
# Client/server version negotiation (since v0.8.0)
# ---------------------------------------------------------------------------
#
# Contract: the client version MUST be >= the server version (major+minor
# semver). Rationale: when the server adds a new endpoint (e.g. the v0.8.0
# `upload-mineru` replacing `upload-markdown`), an old client doesn't know
# the new wire shape and will fail in confusing ways. Forcing client >=
# server prevents the silent broken state.
#
# Mechanism:
#   1. Every request adds  `X-Qatlas-Client-Version: <version>`  (Headers
#      injected by client_version_headers()). The server logs / future
#      rate-limit policies can use it.
#   2. Every response (when from a v0.8.0+ server) includes
#      `X-Qatlas-Server-Version: <version>`. The client compares major+
#      minor against its own __version__.
#   3. If server > client AND the call is a write op → raise SystemExit
#      (hard fail). Read ops just emit a one-shot stderr warning.
#   4. If the header is absent (older server) the client treats the
#      server as "unknown version" and silently skips negotiation —
#      forward-compatible with pre-v0.8.0 deployments.
#
# Patch-level differences are ignored on purpose: a patch bump is supposed
# to be backwards-compatible bug-fix, so cross-patch usage is fine.

_VERSION_TUPLE_RE = re.compile(r"^(\d+)\.(\d+)(?:\.(\d+))?(?:[.+-].*)?$")


def _parse_semver(v: str) -> tuple[int, int] | None:
    """Return (major, minor) for a semver string, or None if unparseable."""
    if not v:
        return None
    m = _VERSION_TUPLE_RE.match(v.strip())
    if not m:
        return None
    return (int(m.group(1)), int(m.group(2)))


def client_version_headers() -> dict[str, str]:
    """Headers every outgoing request should include for version negotiation."""
    return {"X-Qatlas-Client-Version": _CLIENT_VERSION}


_WARNED_OLDER_CLIENT: set[str] = set()


def check_response_version(response: requests.Response, *, write: bool) -> None:
    """Compare X-Qatlas-Server-Version against this client's version.

    * `write=True` callers (POST/PUT/PATCH/DELETE) hard-fail (SystemExit 4)
      when the server is newer than the client at major+minor level — the
      server's wire contract may have moved and silent breakage is the
      worst failure mode.
    * `write=False` callers (GET, status polls) only emit a one-shot
      warning per unique server version, so read-mostly workflows still
      function while signalling the upgrade is needed.

    Older servers (pre-v0.8.0) don't send the header — we treat the
    absence as "unknown" and do nothing, preserving the new client's
    ability to talk to legacy deployments.
    """
    server_version = response.headers.get("X-Qatlas-Server-Version", "").strip()
    if not server_version:
        return  # pre-v0.8.0 server, skip negotiation
    server = _parse_semver(server_version)
    client = _parse_semver(_CLIENT_VERSION)
    if server is None or client is None:
        return  # unparseable on either side — fail open, don't block calls
    if server <= client:
        return  # client >= server, contract satisfied
    msg = (
        f"server version {server_version} is newer than client {_CLIENT_VERSION}.\n"
        f"This client may not understand new endpoints/fields. Upgrade with:\n"
        f"  pip install --upgrade quantum-atlas"
    )
    if write:
        print(f"ERROR: {msg}", file=sys.stderr)
        raise SystemExit(4)
    if server_version not in _WARNED_OLDER_CLIENT:
        print(f"WARNING: {msg}", file=sys.stderr)
        _WARNED_OLDER_CLIENT.add(server_version)
