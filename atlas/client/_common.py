"""Shared helpers for QuantumAtlas client-side CLIs."""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any

import requests

from atlas.server.config import ServerConfig


def default_base_url() -> str:
    """Resolve the server base URL from QATLAS_SERVER_URL (legacy PUBLIC_BASE_URL)
    or fall back to .env host/port (for local dev where the client targets a
    locally-running server)."""
    config = ServerConfig.from_env()
    server_url = config.get_server_url()
    if server_url:
        return server_url
    host = "127.0.0.1" if config.host in {"0.0.0.0", "::"} else config.host
    return f"http://{host}:{config.port}"


def base_url_from_args(args: argparse.Namespace) -> str:
    """Return the explicit --base-url if supplied, else the .env default."""
    return args.base_url.rstrip("/") if args.base_url else default_base_url()


def _env_insecure_default() -> bool:
    """QATLAS_INSECURE=1 makes the client default to skipping TLS verification."""
    return os.getenv("QATLAS_INSECURE", "").strip().lower() in {"1", "true", "yes"}


def request_verify(args: argparse.Namespace) -> bool:
    """Honor --insecure (or QATLAS_INSECURE=1) to disable TLS verification.

    Precedence: explicit ``--insecure`` flag > ``QATLAS_INSECURE`` env > default (verify).
    """
    insecure = bool(getattr(args, "insecure", False)) or _env_insecure_default()
    if not insecure:
        return True
    if not getattr(args, "_insecure_warning_shown", False):
        requests.packages.urllib3.disable_warnings(  # type: ignore[attr-defined]
            category=requests.packages.urllib3.exceptions.InsecureRequestWarning
        )
        print("Warning: TLS certificate verification is disabled.", file=sys.stderr)
        args._insecure_warning_shown = True
    return False


def print_json(payload: dict[str, Any]) -> None:
    print(json.dumps(payload, ensure_ascii=False, indent=2))


def add_common_http_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--base-url",
        help="Server base URL; defaults to QATLAS_SERVER_URL (legacy PUBLIC_BASE_URL), then .env host/port",
    )
    parser.add_argument("--request-timeout", type=float, default=120.0)
    parser.add_argument(
        "--insecure",
        action="store_true",
        help="Skip TLS certificate verification (also enabled by QATLAS_INSECURE=1)",
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
