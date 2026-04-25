"""
Authentication helper endpoints.

The CLI token endpoint is intended to sit behind Caddy's existing OAuth gate.
QuantumAtlas trusts the user identity header injected by that gate for this
single issuance step, then signs its own bearer token for programmatic API use.
"""

from fastapi import APIRouter, HTTPException, Request

from atlas.server.auth import issue_cli_token
from atlas.server.config import ServerConfig

router = APIRouter()

_FALLBACK_USER_HEADERS = (
    "X-Token-User-Name",
    "X-Token-Subject",
    "X-Token-User-Email",
    "X-Forwarded-User",
)


def _header_value(request: Request, name: str) -> str | None:
    value = request.headers.get(name)
    if value:
        return value.strip()
    return None


def _oauth_subject(request: Request, config: ServerConfig) -> str:
    header_names = [config.user_header, *_FALLBACK_USER_HEADERS]
    for header_name in dict.fromkeys(name for name in header_names if name):
        value = _header_value(request, header_name)
        if value:
            return value
    raise HTTPException(status_code=401, detail="OAuth user header was not provided.")


@router.post("/cli-token")
async def create_cli_token(request: Request):
    """Issue a bearer token for the currently OAuth-authenticated user."""
    config: ServerConfig = request.app.state.config
    return issue_cli_token(config, _oauth_subject(request, config))
