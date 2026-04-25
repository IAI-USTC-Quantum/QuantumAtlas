"""
Small HMAC-signed tokens for CLI access.

These tokens bridge an existing reverse-proxy OAuth login to programmatic API
access without requiring QuantumAtlas to become a full OAuth provider.
"""

import base64
import hashlib
import hmac
import json
import time
from datetime import datetime, timezone
from typing import Any

from fastapi import HTTPException

from atlas.server.config import ServerConfig

TOKEN_PREFIX = "qat1"


def _b64encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def _b64decode(data: str) -> bytes:
    padding = "=" * (-len(data) % 4)
    return base64.urlsafe_b64decode(data + padding)


def _sign(secret: str, payload: str) -> str:
    digest = hmac.new(secret.encode("utf-8"), payload.encode("ascii"), hashlib.sha256).digest()
    return _b64encode(digest)


def _require_secret(config: ServerConfig) -> str:
    if not config.cli_token_secret:
        raise HTTPException(
            status_code=503,
            detail="CLI token issuing is not configured on this server.",
        )
    return config.cli_token_secret


def issue_cli_token(config: ServerConfig, subject: str) -> dict[str, Any]:
    """Create a signed bearer token for a reverse-proxy-authenticated subject."""
    secret = _require_secret(config)
    now = int(time.time())
    expires_in = max(60, int(config.cli_token_expires_in))
    payload = {
        "typ": "cli",
        "sub": subject,
        "iat": now,
        "exp": now + expires_in,
    }
    payload_json = json.dumps(payload, separators=(",", ":"), sort_keys=True).encode("utf-8")
    payload_b64 = _b64encode(payload_json)
    token = f"{TOKEN_PREFIX}.{payload_b64}.{_sign(secret, payload_b64)}"
    expires_at = datetime.fromtimestamp(payload["exp"], tz=timezone.utc).isoformat()
    return {
        "access_token": token,
        "token_type": "bearer",
        "expires_in": expires_in,
        "expires_at": expires_at,
        "subject": subject,
    }


def verify_cli_token(config: ServerConfig, token: str) -> str:
    """Return the token subject or raise HTTP 401."""
    secret = _require_secret(config)
    try:
        prefix, payload_b64, signature = token.split(".", 2)
    except ValueError as exc:
        raise HTTPException(status_code=401, detail="Invalid bearer token.") from exc

    if prefix != TOKEN_PREFIX:
        raise HTTPException(status_code=401, detail="Invalid bearer token.")

    expected = _sign(secret, payload_b64)
    if not hmac.compare_digest(signature, expected):
        raise HTTPException(status_code=401, detail="Invalid bearer token.")

    try:
        payload = json.loads(_b64decode(payload_b64))
    except (ValueError, json.JSONDecodeError) as exc:
        raise HTTPException(status_code=401, detail="Invalid bearer token.") from exc

    if payload.get("typ") != "cli" or not isinstance(payload.get("sub"), str):
        raise HTTPException(status_code=401, detail="Invalid bearer token.")
    if int(payload.get("exp", 0)) < int(time.time()):
        raise HTTPException(status_code=401, detail="Bearer token expired.")
    return payload["sub"]
