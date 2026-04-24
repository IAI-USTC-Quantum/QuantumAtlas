"""
Share link API and public /share/{token} routes.
"""

from __future__ import annotations

import html
import logging
import secrets
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import List, Optional

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import FileResponse, HTMLResponse, RedirectResponse
from pydantic import BaseModel

from atlas.server.config import ServerConfig
from atlas.server.tasks import ShareRecord, ShareStore

logger = logging.getLogger(__name__)

api_router = APIRouter()
public_router = APIRouter()

SHARE_URL_PREFIX = "/share"
PERMANENT_SHARE_PATHS = [
    "papers/pdf",
    "papers/markdown",
    "papers/json",
    "papers/images",
]


class ShareRequest(BaseModel):
    """Create a share."""

    paths: List[str]
    label: Optional[str] = None
    expires_in: Optional[int] = None


def _resolve_raw_dir(config: ServerConfig) -> Path:
    return config.get_raw_root()


def _safe_within_root(root: Path, rel_path: str, detail: str) -> Path:
    root_resolved = root.resolve()
    candidate = (root / rel_path).resolve()
    try:
        candidate.relative_to(root_resolved)
    except ValueError as e:
        raise HTTPException(status_code=403, detail=detail) from e
    return candidate


def _resolve_share_target(config: ServerConfig, rel_path: str) -> Path:
    """Resolve a share-relative path to an on-disk target."""
    rel = rel_path.strip("/")
    for kind in ("pdf", "markdown", "json", "images"):
        prefix = f"papers/{kind}"
        if rel == prefix or rel.startswith(prefix + "/"):
            suffix = rel[len(prefix):].lstrip("/")
            return _safe_within_root(
                config.get_paper_asset_dir(kind),
                suffix,
                f"path not under paper {kind} directory",
            )

    return _safe_within_raw(_resolve_raw_dir(config), rel)


def _validate_share_path_fragment(path: str) -> str:
    p = path.strip()
    if not p:
        raise ValueError("path must be non-empty")
    if p.startswith("/") or "\\" in p or ".." in p:
        raise ValueError("path must be relative without '..' or backslashes")
    return p


def _is_under_share(path: str, allowed_roots: List[str]) -> bool:
    norm = path.strip("/")
    for root in allowed_roots:
        r = root.strip("/")
        if norm == r or norm.startswith(r + "/"):
            return True
    return False


def _safe_within_raw(raw_dir: Path, rel_path: str) -> Path:
    return _safe_within_root(raw_dir, rel_path, "path not under RAW_DIR")


def create_share_record(
    *,
    share_store: ShareStore,
    config: ServerConfig,
    paths: List[str],
    label: Optional[str] = None,
    expires_in: Optional[int] = None,
    created_by: Optional[str] = None,
) -> dict:
    """Create a share record programmatically."""
    cleaned: List[str] = []
    for p in paths:
        cleaned.append(_validate_share_path_fragment(p))

    for p in cleaned:
        fs_path = _resolve_share_target(config, p)
        if not fs_path.exists():
            raise HTTPException(status_code=400, detail=f"path does not exist: {p}")

    token = secrets.token_hex(16)
    now = datetime.now(timezone.utc)
    created_at = now.isoformat()
    ttl = config.default_share_expires_in if expires_in is None else expires_in
    expires_at: Optional[str] = None
    if ttl is not None:
        if ttl <= 0:
            raise HTTPException(status_code=400, detail="expires_in must be positive")
        expires_at = (now + timedelta(seconds=ttl)).isoformat()

    record = ShareRecord(
        token=token,
        paths=cleaned,
        created_by=created_by,
        created_at=created_at,
        expires_at=expires_at,
        label=label,
    )
    share_store.save(record)

    return {
        "token": token,
        "url_prefix": build_share_url(token),
        "paths": cleaned,
        "created_at": created_at,
        "expires_at": expires_at,
        "label": label,
    }


def build_share_url(
    token: str,
    rel_path: Optional[str] = None,
    *,
    base_url: Optional[str] = None,
) -> str:
    """Build a public share URL."""
    prefix = SHARE_URL_PREFIX
    if base_url:
        prefix = f"{base_url.rstrip('/')}{SHARE_URL_PREFIX}"
    if rel_path:
        return f"{prefix}/{token}/{rel_path.strip('/')}"
    return f"{prefix}/{token}"


def build_external_share_url(
    config: ServerConfig,
    token: str,
    rel_path: Optional[str] = None,
) -> str:
    """Build an absolute share URL from PUBLIC_BASE_URL."""
    base_url = config.get_public_base_url()
    if not base_url:
        raise ValueError("PUBLIC_BASE_URL must be an absolute service URL")
    return build_share_url(token, rel_path, base_url=base_url)


def permanent_share_record(config: ServerConfig) -> Optional[ShareRecord]:
    """Return the built-in non-expiring share record, if configured."""
    if not config.share_access_token:
        return None
    return ShareRecord(
        token=config.share_access_token,
        paths=PERMANENT_SHARE_PATHS,
        created_by="config",
        created_at="config",
        expires_at=None,
        label="configured permanent paper asset share",
    )


@api_router.post("/")
async def create_share(request: Request, body: ShareRequest):
    """Create a new share token for configured shareable paths."""
    config: ServerConfig = request.app.state.config
    share_store: ShareStore = request.app.state.share_store
    return create_share_record(
        share_store=share_store,
        config=config,
        paths=body.paths,
        label=body.label,
        expires_in=body.expires_in,
        created_by=request.headers.get(config.user_header),
    )


@api_router.get("/")
async def list_shares(request: Request):
    """List all share records."""
    share_store: ShareStore = request.app.state.share_store
    return {"shares": [s.model_dump() for s in share_store.list_all()]}


@api_router.delete("/{token}")
async def delete_share(request: Request, token: str):
    """Revoke a share."""
    share_store: ShareStore = request.app.state.share_store
    if not share_store.delete(token):
        raise HTTPException(status_code=404, detail="share not found")
    return {"ok": True}


def _share_or_410(
    share_store: ShareStore,
    config: ServerConfig,
    token: str,
) -> ShareRecord:
    permanent = permanent_share_record(config)
    if permanent is not None and token == permanent.token:
        return permanent

    rec = share_store.get(token)
    if rec is None:
        raise HTTPException(status_code=404, detail="share not found")
    if rec.expires_at:
        try:
            exp = datetime.fromisoformat(rec.expires_at.replace("Z", "+00:00"))
            now = datetime.now(timezone.utc)
            if exp.tzinfo is None:
                exp = exp.replace(tzinfo=timezone.utc)
            if now > exp:
                raise HTTPException(status_code=410, detail="该分享链接已过期")
        except HTTPException:
            raise
        except ValueError:
            logger.warning("invalid expires_at on share %s", token)
    return rec


def _html_page(title: str, body_inner: str) -> str:
    return (
        "<!DOCTYPE html><html><head><meta charset=\"utf-8\"/>"
        f"<title>{html.escape(title)}</title></head><body>{body_inner}</body></html>"
    )


@public_router.get(f"{SHARE_URL_PREFIX}/{{token}}")
async def share_entry(request: Request, token: str):
    """Single shared file is served here; otherwise redirect to trailing-slash index."""
    share_store: ShareStore = request.app.state.share_store
    config: ServerConfig = request.app.state.config
    rec = _share_or_410(share_store, config, token)

    if len(rec.paths) == 1:
        only = rec.paths[0]
        p = _resolve_share_target(config, only)
        if p.is_file():
            return FileResponse(str(p))

    return RedirectResponse(url=f"{SHARE_URL_PREFIX}/{token}/", status_code=307)


@public_router.get(f"{SHARE_URL_PREFIX}/{{token}}/")
async def share_index(request: Request, token: str):
    """HTML index of shared paths (use trailing slash so relative links resolve under /share/{token}/)."""
    share_store: ShareStore = request.app.state.share_store
    config: ServerConfig = request.app.state.config
    rec = _share_or_410(share_store, config, token)

    items: List[str] = []
    for rel in rec.paths:
        fs = _resolve_share_target(config, rel)
        if fs.is_file():
            sz = fs.stat().st_size
            href = html.escape(f"{rel}")
            items.append(f'<li><a href="{href}">{html.escape(rel)}</a> ({sz} bytes)</li>')
        elif fs.is_dir():
            href = html.escape(f"{rel}/")
            items.append(f'<li><a href="{href}">{html.escape(rel)}/</a> (directory)</li>')
        else:
            items.append(f"<li>{html.escape(rel)} (missing)</li>")

    inner = f"<h1>Share</h1><ul>{''.join(items)}</ul>"
    return HTMLResponse(_html_page("share", inner))


@public_router.get(f"{SHARE_URL_PREFIX}/{{token}}/{{path:path}}")
async def share_file(request: Request, token: str, path: str):
    """Serve a file or directory listing under a share."""
    share_store: ShareStore = request.app.state.share_store
    config: ServerConfig = request.app.state.config
    rec = _share_or_410(share_store, config, token)

    rel = path.strip("/")
    if not _is_under_share(rel, rec.paths):
        raise HTTPException(status_code=403, detail="not allowed for this share")

    fs_path = _resolve_share_target(config, rel)
    if not fs_path.exists():
        raise HTTPException(status_code=404, detail="not found")

    if fs_path.is_file():
        return FileResponse(str(fs_path))

    if fs_path.is_dir():
        entries = sorted(fs_path.iterdir(), key=lambda x: x.name.lower())
        lis: List[str] = []
        for ch in entries:
            name = ch.name
            child_rel = f"{rel}/{name}" if rel else name
            if not _is_under_share(child_rel, rec.paths):
                continue
            if ch.is_dir():
                href = html.escape(f"{name}/")
                lis.append(f'<li><a href="{href}">{html.escape(name)}/</a></li>')
            else:
                href = html.escape(name)
                sz = ch.stat().st_size
                lis.append(
                    f'<li><a href="{href}">{html.escape(name)}</a> ({sz} bytes)</li>'
                )
        inner = f"<h1>{html.escape(rel or '.')}</h1><ul>{''.join(lis)}</ul>"
        return HTMLResponse(_html_page(rel or "dir", inner))

    raise HTTPException(status_code=404, detail="not found")
