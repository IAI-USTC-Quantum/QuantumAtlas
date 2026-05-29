"""
API Router

REST API endpoints for programmatic access.
"""

import json
import logging
import os
import re
import subprocess
import time
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Dict, List, Literal, Optional

import requests
from fastapi import APIRouter, BackgroundTasks, File, HTTPException, Query, Request, UploadFile
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict

from qatlas.paper_assets import (
    normalize_arxiv_identifier,
    paper_asset_path,
    paper_shard,
    paper_storage_key,
    resolve_paper_assets,
    share_path_for_asset,
)
from qatlas.parser.arxiv_fetcher import ArxivFetcher
from qatlas.parser.mineru_client import MinerUClient
from qatlas.server.config import ServerConfig, get_project_root
from qatlas.server.routers.shares import build_external_share_url, build_share_url, create_share_record
from qatlas.server.tasks import IngestStore, IngestTask, ShareStore, StepStatus

router = APIRouter()
logger = logging.getLogger(__name__)

StageName = Literal["fetch", "parse"]
STAGE_ORDER: tuple[StageName, ...] = ("fetch", "parse")
STAGE_DESCRIPTIONS: Dict[StageName, str] = {
    "fetch": "Fetch arXiv metadata and PDF into the local paper asset store.",
    "parse": "Parse the local PDF into Markdown with PyMuPDF or MinerU.",
}
FETCH_MAX_ATTEMPTS = 3
MINERU_MAX_ATTEMPTS = 2
RETRY_DELAY_SECONDS = 0.2


def _configured_wiki_engine(config: ServerConfig, *, enable_neo4j_sync: bool = False):
    from qatlas.wiki.engine import WikiEngine

    return WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=enable_neo4j_sync,
        ensure_directories=False,
        wiki_content_writable=False,
    )


# === Models ===


class PageResponse(BaseModel):
    id: str
    title: str
    type: str
    category: Optional[str] = None
    tags: List[str] = []
    status: str = "draft"
    content: Optional[str] = None
    created_at: Optional[str] = None
    updated_at: Optional[str] = None


class PageListResponse(BaseModel):
    total: int
    pages: List[Dict[str, Any]]


class IngestRequest(BaseModel):
    """Server-side ingest is bounded to fetch + parse (ff-only wiki policy)."""

    model_config = ConfigDict(extra="forbid")

    arxiv_id: str
    fetch: bool = True
    parse: bool = True
    stages: Optional[List[StageName]] = None
    stop_after: Optional[StageName] = None
    parser: Literal["pymupdf", "mineru"]
    force_fetch: bool = False
    force_parse: bool = False
    mineru_no_cache: bool = False


class ContinueIngestRequest(BaseModel):
    """Continue an earlier ingest task from local results."""

    model_config = ConfigDict(extra="forbid")

    stages: Optional[List[StageName]] = None
    stop_after: Optional[StageName] = None
    parser: Literal["pymupdf", "mineru"]
    force_fetch: bool = False
    force_parse: bool = False
    mineru_no_cache: bool = False


class IngestQueuedResponse(BaseModel):
    """Response when a paper ingest is queued."""

    task_id: str
    status: str
    message: str


class GraphQueryRequest(BaseModel):
    query: str
    limit: int = 50


def _flags_from_stage_control(
    *,
    stages: Optional[List[StageName]],
    stop_after: Optional[StageName],
    fallback: Dict[StageName, bool],
) -> Dict[StageName, bool]:
    """Resolve modern stage controls while preserving legacy boolean flags."""
    if stages is not None:
        selected = set(stages)
        return {stage: stage in selected for stage in STAGE_ORDER}

    if stop_after is not None:
        last = STAGE_ORDER.index(stop_after)
        selected = set(STAGE_ORDER[: last + 1])
        return {stage: stage in selected for stage in STAGE_ORDER}

    return dict(fallback)


def _stage_options(
    *,
    flags: Dict[StageName, bool],
    parser: str,
    force_fetch: bool,
    force_parse: bool,
    mineru_no_cache: bool,
    source_task_id: Optional[str] = None,
) -> Dict[str, Any]:
    options: Dict[str, Any] = {
        "fetch": flags["fetch"],
        "parse": flags["parse"],
        "parser": parser,
        "force_fetch": force_fetch,
        "force_parse": force_parse,
        "mineru_no_cache": mineru_no_cache,
    }
    if source_task_id:
        options["source_task_id"] = source_task_id
    return options


def _stage_done_for_continue(task: IngestTask, stage: StageName) -> bool:
    step = task.steps.get(stage)
    if step is None:
        return False
    if step.status == "succeeded":
        return True
    if stage == "fetch" and step.status == "skipped" and step.result:
        return bool(step.result.get("pdf_path"))
    return False


def _remaining_stage_flags(task: IngestTask) -> Dict[StageName, bool]:
    first_remaining = len(STAGE_ORDER)
    for index, stage in enumerate(STAGE_ORDER):
        if not _stage_done_for_continue(task, stage):
            first_remaining = index
            break
    selected = set(STAGE_ORDER[first_remaining:])
    return {stage: stage in selected for stage in STAGE_ORDER}


def _continue_stage_flags(
    task: IngestTask,
    *,
    stages: Optional[List[StageName]],
    stop_after: Optional[StageName],
) -> Dict[StageName, bool]:
    if stages is not None:
        selected = set(stages)
        return {stage: stage in selected for stage in STAGE_ORDER}

    remaining = _remaining_stage_flags(task)
    if stop_after is None:
        return remaining

    last = STAGE_ORDER.index(stop_after)
    return {
        stage: remaining[stage] and STAGE_ORDER.index(stage) <= last
        for stage in STAGE_ORDER
    }


def _new_ingest_task(
    *,
    task_id: str,
    arxiv_id: str,
    requester: Optional[str],
    options: Dict[str, Any],
    submitted_at: str,
    message: str = "ingest queued",
) -> IngestTask:
    return IngestTask(
        task_id=task_id,
        arxiv_id=arxiv_id,
        status="queued",
        message=message,
        requester=requester,
        options=options,
        steps={stage: StepStatus() for stage in STAGE_ORDER},
        submitted_at=submitted_at,
        updated_at=submitted_at,
    )


# === Server Info API ===


def _resolve_project_path(value: str) -> Path:
    path = Path(value)
    if not path.is_absolute():
        path = get_project_root() / path
    return path.resolve()


def _is_external_to_project(path: Path) -> bool:
    project_root = get_project_root().resolve()
    try:
        path.relative_to(project_root)
        return False
    except ValueError:
        return True


def _git_output(cwd: Path, *args: str) -> Optional[str]:
    result = _git_run(cwd, *args, timeout=2)
    if result is None or result.returncode != 0:
        return None
    return result.stdout.strip()


def _git_run(
    cwd: Path, *args: str, timeout: int = 10
) -> Optional[subprocess.CompletedProcess[str]]:
    try:
        return subprocess.run(
            ["git", *args],
            cwd=str(cwd),
            capture_output=True,
            check=False,
            text=True,
            timeout=timeout,
        )
    except (FileNotFoundError, OSError, subprocess.TimeoutExpired):
        return None


def _git_counts(path: Path, upstream: Optional[str]) -> tuple[Optional[int], Optional[int]]:
    if upstream is None:
        return None, None
    counts = _git_output(path, "rev-list", "--left-right", "--count", f"HEAD...{upstream}")
    if not counts:
        return None, None
    parts = counts.split()
    if len(parts) != 2:
        return None, None
    try:
        ahead = int(parts[0])
        behind = int(parts[1])
    except ValueError:
        return None, None
    return ahead, behind


def _git_info(path: Path) -> Dict[str, Any]:
    if not path.exists():
        return {"enabled": False}
    inside = _git_output(path, "rev-parse", "--is-inside-work-tree")
    if inside != "true":
        return {"enabled": False}

    branch = _git_output(path, "branch", "--show-current") or None
    commit = _git_output(path, "rev-parse", "--short", "HEAD") or None
    upstream = (
        _git_output(path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
        or None
    )
    ahead, behind = _git_counts(path, upstream)
    status = _git_output(path, "status", "--porcelain")
    dirty = None if status is None else bool(status)
    warnings = []
    if branch not in {"main", "master"}:
        warnings.append(
            {
                "code": "wiki_branch_not_main",
                "message": "Wiki repo is not checked out on main or master.",
                "branch": branch,
            }
        )

    return {
        "enabled": True,
        "branch": branch,
        "commit": commit,
        "upstream": upstream,
        "ahead": ahead,
        "behind": behind,
        "dirty": dirty,
        "warnings": warnings,
    }


def _wiki_sync_status(config: ServerConfig) -> Dict[str, Any]:
    wiki_dir = _resolve_project_path(config.wiki_dir)
    return {
        "wiki": {
            "exists": wiki_dir.exists(),
            "external": _is_external_to_project(wiki_dir),
        },
        "git": _git_info(wiki_dir),
    }


@router.get("/server/info")
def server_info(request: Request):
    """Return a safe server configuration summary without local filesystem paths."""
    config: ServerConfig = request.app.state.config
    sync_status = _wiki_sync_status(config)

    return {
        "mode": "server",
        "version": request.app.version,
        "code": {
            "tag": f"v{request.app.version}",
        },
        "wiki": {
            "exists": sync_status["wiki"]["exists"],
            "external": sync_status["wiki"]["external"],
            "git": sync_status["git"],
        },
        "assets": {
            "public_base_url": config.get_public_base_url(),
            "share_access_token_enabled": bool(config.share_access_token),
        },
        "audit": {
            "user_header_enabled": bool(config.user_header),
        },
    }


@router.get("/wiki/sync/status")
def wiki_sync_status(request: Request):
    """Return local Wiki Git status without contacting remotes."""
    return _wiki_sync_status(request.app.state.config)


@router.get("/session/token")
def session_token(request: Request):
    """Return the current Caddy token for explicit copy-to-CLI workflows."""
    return JSONResponse(
        {"token": request.cookies.get("AUTHP_ACCESS_TOKEN", "")},
        headers={
            "Cache-Control": "no-store",
            "Pragma": "no-cache",
        },
    )


@router.post("/wiki/sync/pull")
def wiki_sync_pull(request: Request):
    """Fetch and fast-forward the configured Wiki repository."""
    config: ServerConfig = request.app.state.config
    wiki_dir = _resolve_project_path(config.wiki_dir)
    if not wiki_dir.exists():
        logger.warning("wiki sync failed: directory does not exist: %s", wiki_dir)
        raise HTTPException(status_code=409, detail="wiki directory does not exist")

    before = _git_info(wiki_dir)
    if not before.get("enabled"):
        logger.warning("wiki sync failed: directory is not a git repository: %s", wiki_dir)
        raise HTTPException(status_code=409, detail="wiki directory is not a git repository")
    if before.get("dirty"):
        logger.warning("wiki sync failed: worktree has local changes before fetch")
        raise HTTPException(status_code=409, detail="wiki worktree has local changes")

    old_commit = _git_output(wiki_dir, "rev-parse", "--short", "HEAD")

    fetch = _git_run(wiki_dir, "fetch", "--prune", timeout=30)
    if fetch is None:
        logger.exception("wiki sync failed: git fetch could not be executed")
        raise HTTPException(status_code=500, detail="git fetch could not be executed")
    if fetch.returncode != 0:
        detail = (fetch.stderr or fetch.stdout or "git fetch failed").strip()
        logger.warning(
            "wiki sync failed: git fetch returned %s: %s",
            fetch.returncode,
            detail,
        )
        raise HTTPException(status_code=502, detail=detail)

    after_fetch = _git_info(wiki_dir)
    if after_fetch.get("dirty"):
        logger.warning("wiki sync failed: worktree has local changes after fetch")
        raise HTTPException(status_code=409, detail="wiki worktree has local changes")

    pull = _git_run(wiki_dir, "pull", "--ff-only", timeout=30)
    if pull is None:
        logger.exception("wiki sync failed: git pull could not be executed")
        raise HTTPException(status_code=500, detail="git pull could not be executed")
    if pull.returncode != 0:
        detail = (pull.stderr or pull.stdout or "git pull --ff-only failed").strip()
        logger.warning(
            "wiki sync failed: git pull --ff-only returned %s: %s",
            pull.returncode,
            detail,
        )
        raise HTTPException(status_code=409, detail=detail)

    new_commit = _git_output(wiki_dir, "rev-parse", "--short", "HEAD")
    return {
        "status": "succeeded",
        "changed": old_commit != new_commit,
        "old_commit": old_commit,
        "new_commit": new_commit,
        **_wiki_sync_status(config),
    }


# === Paper asset uploads ===

# Strict arXiv ID patterns for uploads. Version suffix (vN) is required so the
# stored filename matches the canonical RAW_DIR layout (e.g. 9508027v1.pdf,
# 2501.00010v1.pdf) used elsewhere in the project.
_NEW_STYLE_ARXIV_RE = re.compile(r"^\d{4}\.\d{4,6}v\d+$")
_OLD_STYLE_ARXIV_RE = re.compile(r"^[a-z][a-z\-]*(?:\.[A-Z]{2})?/\d{7}v\d+$")
_UPLOAD_MAX_PDF_BYTES = 100 * 1024 * 1024
_UPLOAD_MAX_MARKDOWN_BYTES = 25 * 1024 * 1024
_UPLOAD_MAX_METADATA_BYTES = 2 * 1024 * 1024


def _normalize_arxiv_for_upload(arxiv_id: str) -> str:
    """Normalize and strictly validate an arXiv id submitted for upload.

    Accepts the two officially-documented arXiv id schemes:
      * New style (post April 2007): ``YYMM.NNNN`` or ``YYMM.NNNNN`` (4-5 digit
        sequence), e.g. ``2501.00010``.
      * Old style (pre April 2007): ``category/YYMMNNN`` where ``category`` is
        the lowercase archive name with optional ``.XX`` subject class,
        e.g. ``quant-ph/9508027`` or ``cond-mat.str-el/0701123``.

    A trailing version suffix (``vN``) is required so the file name on disk
    unambiguously identifies which arXiv revision is being contributed. The
    storage layout in ``RAW_DIR`` always carries the version, e.g.
    ``raw/pdf/9508/9508027v1.pdf`` or ``raw/pdf/2501/2501.00010v1.pdf``.
    """
    canonical = normalize_arxiv_identifier(arxiv_id)
    if _NEW_STYLE_ARXIV_RE.match(canonical) or _OLD_STYLE_ARXIV_RE.match(canonical):
        return canonical
    raise HTTPException(
        status_code=400,
        detail=(
            f"invalid arxiv_id for upload: {arxiv_id!r}. "
            "Expected new-style 'YYMM.NNNNNvN' (post April 2007, e.g. '2501.00010v1') "
            "or old-style 'category/YYMMNNNvN' (pre April 2007, e.g. 'quant-ph/9508027v1'). "
            "An explicit version suffix is required."
        ),
    )


async def _save_upload_file(
    *,
    upload: UploadFile,
    destination: Path,
    max_bytes: int,
    label: str,
) -> int:
    """Stream an UploadFile to disk with a hard size cap. Returns bytes written."""
    destination.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = destination.with_suffix(destination.suffix + ".part")
    bytes_written = 0
    try:
        with tmp_path.open("wb") as out:
            while True:
                chunk = await upload.read(1 << 20)
                if not chunk:
                    break
                bytes_written += len(chunk)
                if bytes_written > max_bytes:
                    raise HTTPException(
                        status_code=413,
                        detail=f"{label} exceeds maximum upload size of {max_bytes} bytes",
                    )
                out.write(chunk)
        if bytes_written == 0:
            raise HTTPException(status_code=400, detail=f"{label} upload was empty")
        tmp_path.replace(destination)
    except HTTPException:
        tmp_path.unlink(missing_ok=True)
        raise
    except Exception:
        tmp_path.unlink(missing_ok=True)
        raise
    finally:
        await upload.close()
    return bytes_written


def _relative_raw_path(path: Path, raw_root: Path) -> str:
    try:
        return path.relative_to(raw_root).as_posix()
    except ValueError:
        return str(path)


# === MinerU claim / lease ===

# A "claim" is a short-lived lease declaring that a specific contributor is
# currently running MinerU against a paper's PDF and will upload the resulting
# markdown shortly. The point is to keep concurrent contributors from
# redundantly burning their own MinerU quota on the same paper.
#
# Storage: one JSON file per arxiv_id under DATA_DIR/mineru-claims/{key}.json.
# Atomicity is achieved by O_EXCL create; expired claims are overwritten via a
# tmp + posix_rename swap. Successful upload-markdown deletes the claim.

_CLAIM_DEFAULT_TTL_SECONDS = 1800  # 30 minutes
_CLAIM_MIN_TTL_SECONDS = 60
_CLAIM_MAX_TTL_SECONDS = 7200  # 2 hours


def _claims_dir(config: ServerConfig) -> Path:
    return config.get_data_root() / "mineru-claims"


def _claim_path(config: ServerConfig, arxiv_id: str) -> Path:
    return _claims_dir(config) / f"{paper_storage_key(arxiv_id)}.json"


def _read_claim(path: Path) -> Optional[Dict[str, Any]]:
    if not path.is_file():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return None
    if not isinstance(data, dict):
        return None
    return data


def _claim_is_active(claim: Optional[Dict[str, Any]], now: datetime) -> bool:
    if claim is None:
        return False
    expires_raw = claim.get("expires_at")
    if not isinstance(expires_raw, str):
        return False
    try:
        expires = datetime.fromisoformat(expires_raw)
    except ValueError:
        return False
    if expires.tzinfo is None:
        expires = expires.replace(tzinfo=timezone.utc)
    return expires > now


def _delete_claim(config: ServerConfig, arxiv_id: str) -> None:
    """Best-effort delete of any claim for this paper (used by upload-markdown)."""
    try:
        _claim_path(config, arxiv_id).unlink(missing_ok=True)
    except OSError:
        logger.warning("failed to remove mineru claim for %s", arxiv_id, exc_info=True)


def _enumerate_needs_mineru(
    raw_root: Path,
    config: ServerConfig,
    *,
    limit: int,
    include_claimed: bool,
) -> tuple[List[Dict[str, Any]], int, int]:
    """Walk RAW_DIR/pdf and return papers with PDF but no markdown.

    Returns (papers, total_unclaimed, total_with_claims).
    """
    pdf_root = raw_root / "pdf"
    md_root = raw_root / "markdown"
    if not pdf_root.is_dir():
        return [], 0, 0

    now = datetime.now(timezone.utc)
    papers: List[Dict[str, Any]] = []
    total_unclaimed = 0
    total_claimed = 0

    pdf_files = sorted(pdf_root.glob("*/*.pdf")) + sorted(pdf_root.glob("*.pdf"))
    seen_keys: set[str] = set()
    for pdf_path in pdf_files:
        key = pdf_path.stem
        if key in seen_keys:
            continue
        seen_keys.add(key)

        shard = paper_shard(key)
        md_candidates = [md_root / f"{key}.md"]
        if shard:
            md_candidates.append(md_root / shard / f"{key}.md")
        if any(c.is_file() for c in md_candidates):
            continue

        canonical = _canonical_arxiv_from_key(key)
        claim = _read_claim(_claim_path(config, canonical))
        claimed = _claim_is_active(claim, now)
        if claimed:
            total_claimed += 1
        else:
            total_unclaimed += 1

        if claimed and not include_claimed:
            continue
        if len(papers) >= limit:
            # Keep counting totals but stop collecting.
            continue

        papers.append(
            {
                "arxiv_id": canonical,
                "key": key,
                "pdf_path": _relative_raw_path(pdf_path, raw_root),
                "claimed": claimed,
                "claim_expires_at": claim.get("expires_at") if claim and claimed else None,
                "claim_requester": claim.get("requester") if claim and claimed else None,
            }
        )

    return papers, total_unclaimed, total_claimed


def _canonical_arxiv_from_key(key: str) -> str:
    """Reverse paper_storage_key for old-style ids to the form expected on the wire.

    Old-style storage keys drop the category prefix (e.g. 9508027v1). Without
    talking to the metadata layer we can't reliably know the category, so we
    just return the storage key as-is for both old and new style; clients that
    need to interact with the paper resources endpoint can use either form,
    since resolve_paper_assets accepts both.
    """
    return key


@router.get("/papers/needs-mineru")
async def list_needs_mineru(
    request: Request,
    limit: int = Query(10, ge=1, le=100, description="Maximum papers to return"),
    include_claimed: bool = Query(
        False,
        description="When true, include papers that are currently claimed by another contributor.",
    ),
):
    """List papers that have a PDF in RAW_DIR but no parsed markdown yet.

    By default only returns papers that no one else is currently working on
    (i.e., no active mineru claim). Use ``include_claimed=true`` to also see
    in-flight work for diagnostics.
    """
    config: ServerConfig = request.app.state.config
    papers, total_unclaimed, total_claimed = _enumerate_needs_mineru(
        config.get_raw_root(),
        config,
        limit=limit,
        include_claimed=include_claimed,
    )
    return {
        "papers": papers,
        "returned": len(papers),
        "total_unclaimed": total_unclaimed,
        "total_claimed": total_claimed,
    }


def _build_pdf_share_url(config: ServerConfig, share_store, arxiv_id: str) -> Optional[str]:
    """Return the share URL for a paper's PDF when it exists in RAW_DIR.

    Mirrors the logic in routers/downloads.py for the PDF-only case, so the
    mineru claim response can hand the contributor a directly-fetchable URL
    without a second round-trip.
    """
    resolved = resolve_paper_assets(config.get_raw_root(), arxiv_id)
    pdf_path = resolved.get("pdf_path")
    if not isinstance(pdf_path, Path) or not pdf_path.is_file():
        return None
    share_path = share_path_for_asset(
        "pdf",
        resolved["key"],
        asset_path=pdf_path,
        paper_assets_root=config.get_raw_root(),
    )
    share_token = config.share_access_token
    share_base_url = config.get_public_base_url() if share_token else None
    if share_token is None:
        share = create_share_record(
            share_store=share_store,
            config=config,
            paths=[share_path],
            label=f"mineru pdf: {arxiv_id}",
            expires_in=None,
            created_by=None,
        )
        share_token = share["token"]
    return build_share_url(share_token, share_path, base_url=share_base_url)


@router.post("/papers/{arxiv_id:path}/mineru-claim", status_code=201)
async def claim_mineru(
    request: Request,
    arxiv_id: str,
    ttl_seconds: int = Query(
        _CLAIM_DEFAULT_TTL_SECONDS,
        ge=_CLAIM_MIN_TTL_SECONDS,
        le=_CLAIM_MAX_TTL_SECONDS,
        description=(
            "Lease duration in seconds. Other contributors will see this paper "
            "as 'claimed' until the TTL expires or the claim is released."
        ),
    ),
):
    """Atomically claim a paper for MinerU processing.

    Returns the share URL of the PDF (ready to feed to MinerU) and a claim id
    that releases the lease either implicitly (on successful upload-markdown)
    or explicitly via DELETE.

    Status codes:
      * 201 — claim granted.
      * 404 — no PDF exists for this arxiv_id; nothing to claim.
      * 409 — markdown already exists, or another contributor holds an active
        claim. The response body includes ``claim_expires_at`` so the caller
        can decide whether to wait or skip.
    """
    canonical = _normalize_arxiv_for_upload(arxiv_id)
    config: ServerConfig = request.app.state.config
    raw_root = config.get_raw_root()

    # PDF presence is the precondition; refuse to claim a paper we cannot serve.
    pdf_path = paper_asset_path(raw_root, "pdf", canonical)
    pdf_exists = pdf_path.is_file()
    if not pdf_exists:
        # Try versionless / sharded lookup before failing.
        resolved = resolve_paper_assets(raw_root, canonical)
        resolved_pdf = resolved.get("pdf_path")
        if isinstance(resolved_pdf, Path) and resolved_pdf.is_file():
            pdf_path = resolved_pdf
            pdf_exists = True
    if not pdf_exists:
        raise HTTPException(
            status_code=404,
            detail=f"no PDF in RAW_DIR for {canonical}; upload it first via /api/papers/{{arxiv_id}}/upload-pdf",
        )

    # No work to do if a markdown is already present.
    md_path = paper_asset_path(raw_root, "markdown", canonical)
    md_resolved = resolve_paper_assets(raw_root, canonical).get("markdown_path")
    if md_path.is_file() or (isinstance(md_resolved, Path) and md_resolved.is_file()):
        raise HTTPException(
            status_code=409,
            detail=f"markdown already exists for {canonical}; nothing to do",
        )

    claim_path = _claim_path(config, canonical)
    claim_path.parent.mkdir(parents=True, exist_ok=True)

    now = datetime.now(timezone.utc)
    expires_at = now + timedelta(seconds=ttl_seconds)
    claim_id = uuid.uuid4().hex
    requester = request.headers.get(config.user_header) if config.user_header else None

    share_url = _build_pdf_share_url(config, request.app.state.share_store, canonical)
    if share_url is None:
        raise HTTPException(status_code=500, detail="failed to build share URL for PDF")

    payload: Dict[str, Any] = {
        "claim_id": claim_id,
        "arxiv_id": canonical,
        "key": paper_storage_key(canonical),
        "requester": requester,
        "created_at": now.isoformat(),
        "expires_at": expires_at.isoformat(),
        "ttl_seconds": ttl_seconds,
        "pdf_url": share_url,
    }

    # Atomic O_EXCL create; if it exists, validate or overwrite-on-expiry.
    serialized = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    try:
        fd = os.open(claim_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    except FileExistsError:
        existing = _read_claim(claim_path)
        if _claim_is_active(existing, now):
            assert existing is not None
            raise HTTPException(
                status_code=409,
                detail={
                    "message": f"{canonical} is already claimed",
                    "claim_id": existing.get("claim_id"),
                    "claim_expires_at": existing.get("expires_at"),
                    "claim_requester": existing.get("requester"),
                },
            )
        # Expired or corrupt; replace atomically.
        tmp = claim_path.with_suffix(".json.tmp")
        tmp.write_bytes(serialized)
        tmp.replace(claim_path)
    else:
        try:
            os.write(fd, serialized)
        finally:
            os.close(fd)

    logger.info(
        "mineru claim granted for %s to %s (claim_id=%s, ttl=%ss)",
        canonical,
        requester or "anonymous",
        claim_id,
        ttl_seconds,
    )
    return payload


@router.delete("/papers/{arxiv_id:path}/mineru-claim/{claim_id}", status_code=204)
async def release_mineru_claim(request: Request, arxiv_id: str, claim_id: str):
    """Explicitly release a mineru claim (e.g. when the client aborted).

    Idempotent: releasing a non-existent or already-expired claim returns 204.
    Mismatched claim_id returns 409 to avoid accidentally killing someone
    else's active claim.
    """
    canonical = _normalize_arxiv_for_upload(arxiv_id)
    config: ServerConfig = request.app.state.config
    claim_path = _claim_path(config, canonical)
    existing = _read_claim(claim_path)
    if existing is None:
        return JSONResponse(status_code=204, content=None)
    if existing.get("claim_id") != claim_id:
        # Don't let one client release another's lease.
        if _claim_is_active(existing, datetime.now(timezone.utc)):
            raise HTTPException(
                status_code=409,
                detail="claim_id does not match the active claim",
            )
    try:
        claim_path.unlink(missing_ok=True)
    except OSError:
        logger.warning("failed to remove mineru claim file for %s", canonical, exc_info=True)
    return JSONResponse(status_code=204, content=None)


@router.post("/papers/{arxiv_id:path}/upload-pdf", status_code=201)
async def upload_paper_pdf(
    request: Request,
    arxiv_id: str,
    pdf: UploadFile = File(..., description="The paper PDF, multipart/form-data field 'pdf'"),
    metadata: Optional[UploadFile] = File(
        None,
        description=(
            "Optional arXiv metadata JSON (title, authors, abstract, ...), multipart "
            "field 'metadata'. Will be stored at RAW_DIR/json/<key>.json."
        ),
    ),
    overwrite: bool = Query(
        False,
        description="Replace existing PDF/JSON when true; otherwise 409 if a file is already present.",
    ),
):
    """Accept a contributed paper PDF (and optional metadata) into RAW_DIR.

    The arXiv id in the path must include a version suffix (e.g. ``quant-ph/9508027v1``
    or ``2501.00010v1``). Files are stored at the canonical sharded paths used
    by ``paper_asset_path``: ``RAW_DIR/pdf/<shard>/<key>.pdf`` and, when
    metadata is provided, ``RAW_DIR/json/<shard>/<key>.json``.
    """
    canonical = _normalize_arxiv_for_upload(arxiv_id)
    config: ServerConfig = request.app.state.config
    raw_root = config.get_raw_root()

    pdf_path = paper_asset_path(raw_root, "pdf", canonical)
    json_path = paper_asset_path(raw_root, "json", canonical) if metadata is not None else None

    if pdf_path.exists() and not overwrite:
        raise HTTPException(
            status_code=409,
            detail=f"PDF already exists at {_relative_raw_path(pdf_path, raw_root)}; pass overwrite=true to replace",
        )
    if json_path is not None and json_path.exists() and not overwrite:
        raise HTTPException(
            status_code=409,
            detail=f"metadata already exists at {_relative_raw_path(json_path, raw_root)}; pass overwrite=true to replace",
        )

    content_type = (pdf.content_type or "").lower()
    if content_type and "pdf" not in content_type and content_type != "application/octet-stream":
        raise HTTPException(
            status_code=415,
            detail=f"expected application/pdf for 'pdf' part, got {pdf.content_type!r}",
        )

    pdf_bytes = await _save_upload_file(
        upload=pdf, destination=pdf_path, max_bytes=_UPLOAD_MAX_PDF_BYTES, label="pdf"
    )
    if not pdf_path.read_bytes()[:5].startswith(b"%PDF-"):
        pdf_path.unlink(missing_ok=True)
        raise HTTPException(status_code=400, detail="uploaded file does not look like a PDF (missing %PDF- header)")

    metadata_bytes = 0
    if metadata is not None and json_path is not None:
        metadata_bytes = await _save_upload_file(
            upload=metadata,
            destination=json_path,
            max_bytes=_UPLOAD_MAX_METADATA_BYTES,
            label="metadata",
        )
        try:
            json.loads(json_path.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            json_path.unlink(missing_ok=True)
            raise HTTPException(status_code=400, detail=f"metadata must be valid utf-8 JSON: {exc}") from None

    requester = request.headers.get(config.user_header) if config.user_header else None
    logger.info(
        "uploaded pdf for %s by %s (%d bytes, metadata=%d bytes) -> %s",
        canonical,
        requester or "anonymous",
        pdf_bytes,
        metadata_bytes,
        pdf_path,
    )

    return {
        "arxiv_id": canonical,
        "key": paper_storage_key(canonical),
        "pdf_path": _relative_raw_path(pdf_path, raw_root),
        "pdf_bytes": pdf_bytes,
        "metadata_path": _relative_raw_path(json_path, raw_root) if json_path is not None else None,
        "metadata_bytes": metadata_bytes or None,
        "uploaded_by": requester,
        "overwritten": bool(overwrite and pdf_path.exists()),
    }


@router.post("/papers/{arxiv_id:path}/upload-markdown", status_code=201)
async def upload_paper_markdown(
    request: Request,
    arxiv_id: str,
    markdown: UploadFile = File(
        ..., description="Parsed markdown, multipart/form-data field 'markdown'"
    ),
    overwrite: bool = Query(
        False,
        description="Replace existing markdown when true; otherwise 409 if a file is already present.",
    ),
    source: Optional[str] = Query(
        None,
        max_length=64,
        description="Tool that produced the markdown, e.g. 'mineru' or 'pymupdf'. Recorded in audit log only.",
    ),
):
    """Accept a contributed parsed markdown into RAW_DIR/markdown/.

    Typically used by clients that ran MinerU locally with their own API token,
    or by reviewers who hand-edited a parse result. The arXiv id in the path
    must include a version suffix.
    """
    canonical = _normalize_arxiv_for_upload(arxiv_id)
    config: ServerConfig = request.app.state.config
    raw_root = config.get_raw_root()

    md_path = paper_asset_path(raw_root, "markdown", canonical)
    if md_path.exists() and not overwrite:
        raise HTTPException(
            status_code=409,
            detail=f"markdown already exists at {_relative_raw_path(md_path, raw_root)}; pass overwrite=true to replace",
        )

    md_bytes = await _save_upload_file(
        upload=markdown,
        destination=md_path,
        max_bytes=_UPLOAD_MAX_MARKDOWN_BYTES,
        label="markdown",
    )
    try:
        md_path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        md_path.unlink(missing_ok=True)
        raise HTTPException(status_code=400, detail=f"markdown must be valid utf-8: {exc}") from None

    requester = request.headers.get(config.user_header) if config.user_header else None
    logger.info(
        "uploaded markdown for %s by %s (source=%s, %d bytes) -> %s",
        canonical,
        requester or "anonymous",
        source or "unspecified",
        md_bytes,
        md_path,
    )

    # Successful markdown upload releases any outstanding mineru claim.
    _delete_claim(config, canonical)

    return {
        "arxiv_id": canonical,
        "key": paper_storage_key(canonical),
        "markdown_path": _relative_raw_path(md_path, raw_root),
        "markdown_bytes": md_bytes,
        "source": source,
        "uploaded_by": requester,
        "overwritten": bool(overwrite),
    }


# === Wiki API ===


@router.get("/pages", response_model=PageListResponse)
async def list_pages(
    request: Request,
    page_type: Optional[str] = None,
    tags: Optional[str] = None,
    status: Optional[str] = None,
):
    """List all wiki pages."""
    wiki = _configured_wiki_engine(request.app.state.config, enable_neo4j_sync=False)

    tag_list = tags.split(",") if tags else None
    pages = wiki.list_pages(page_type=page_type, tags=tag_list, status=status)

    return PageListResponse(
        total=len(pages),
        pages=[
            {
                "id": p.frontmatter.id,
                "title": p.frontmatter.title,
                "type": p.frontmatter.type,
                "category": p.frontmatter.category,
                "status": p.frontmatter.status,
                "tags": p.frontmatter.tags,
            }
            for p in pages
        ],
    )


@router.get("/pages/{page_id}", response_model=PageResponse)
async def get_page(request: Request, page_id: str):
    """Get a wiki page by ID."""
    wiki = _configured_wiki_engine(request.app.state.config, enable_neo4j_sync=False)
    page = wiki.get_page(page_id)

    if page is None:
        raise HTTPException(status_code=404, detail=f"Page not found: {page_id}")

    return PageResponse(
        id=page.frontmatter.id,
        title=page.frontmatter.title,
        type=page.frontmatter.type,
        category=page.frontmatter.category,
        tags=page.frontmatter.tags,
        status=page.frontmatter.status,
        content=page.content,
        created_at=page.frontmatter.created_at.isoformat() if page.frontmatter.created_at else None,
        updated_at=page.frontmatter.updated_at.isoformat() if page.frontmatter.updated_at else None,
    )


@router.get("/stats")
async def get_stats(request: Request):
    """Get wiki statistics."""
    wiki = _configured_wiki_engine(request.app.state.config, enable_neo4j_sync=False)
    return wiki.get_stats()


@router.get("/search")
async def search_pages(request: Request, q: str, limit: int = 10):
    """Search wiki pages."""
    wiki = _configured_wiki_engine(request.app.state.config, enable_neo4j_sync=False)
    results = wiki.query(q, max_results=limit)

    return {"query": q, "total": len(results), "results": results}


# === Graph API ===


@router.get("/graph/stats")
async def graph_stats():
    """Get Neo4j graph statistics."""
    try:
        from qatlas.knowledge.neo4j_client import Neo4jClient
        from qatlas.server.config import get_config

        config = get_config()
        client = Neo4jClient(
            uri=config.neo4j_uri,
            username=config.neo4j_user,
            password=config.neo4j_password,
        )
        client.connect()
        label_counts = client.get_stats()
        with client.session() as session:
            result = session.run("MATCH ()-[r]->() RETURN count(r) as count")
            relationships = result.single()["count"]
        client.close()
        return {
            "nodes": sum(label_counts.values()),
            "relationships": relationships,
            "labels": list(label_counts.keys()),
            "label_counts": label_counts,
        }
    except Exception as e:
        return {"error": str(e)}


@router.post("/graph/query")
async def graph_query(request: GraphQueryRequest):
    """Execute a Cypher query."""
    try:
        from qatlas.knowledge.neo4j_client import Neo4jClient
        from qatlas.server.config import get_config

        config = get_config()
        client = Neo4jClient(
            uri=config.neo4j_uri,
            username=config.neo4j_user,
            password=config.neo4j_password,
        )
        client.connect()

        with client.session() as session:
            result = session.run(f"{request.query} LIMIT {request.limit}")
            records = [dict(record) for record in result]

        client.close()
        return {"query": request.query, "records": records}
    except Exception as e:
        return {"error": str(e)}


@router.get("/graph/schema")
async def graph_schema():
    """Get Neo4j graph schema."""
    try:
        from qatlas.knowledge.neo4j_client import Neo4jClient
        from qatlas.server.config import get_config

        config = get_config()
        client = Neo4jClient(
            uri=config.neo4j_uri,
            username=config.neo4j_user,
            password=config.neo4j_password,
        )
        client.connect()

        # Get node labels
        with client.session() as session:
            labels_result = session.run("CALL db.labels()")
            labels = [r["label"] for r in labels_result]

            # Get relationship types
            rels_result = session.run("CALL db.relationshipTypes()")
            rel_types = [r["relationshipType"] for r in rels_result]

        client.close()
        return {"labels": labels, "relationship_types": rel_types}
    except Exception as e:
        return {"error": str(e)}


# === Ingest API ===


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def _friendly_fetch_error(exc: Exception, arxiv_id: str) -> str:
    msg = str(exc).lower()
    if "not found" in msg or "404" in msg:
        return f"Paper not found on arXiv: {arxiv_id}"
    if "timeout" in msg or isinstance(exc, requests.Timeout):
        return "Failed to download PDF: connection or read timeout"
    return f"Failed to fetch paper: {exc}"


def _friendly_parse_error(exc: Exception) -> str:
    msg = str(exc)
    low = msg.lower()
    if "mineru" in low:
        return f"MinerU parsing failed: {exc}"
    if "public_base_url" in low:
        return str(exc)
    if "pymupdf" in low or "fitz" in low or "import" in low:
        return "PDF parser unavailable: pymupdf may not be installed"
    if "encrypt" in low or "password" in low:
        return "PDF parsing failed: file appears to be corrupted or encrypted"
    return f"PDF parsing failed: {exc}"


def _is_retryable_fetch_error(exc: Exception) -> bool:
    if isinstance(exc, requests.HTTPError):
        status_code = getattr(exc.response, "status_code", None)
        return status_code in {408, 429, 500, 502, 503, 504}
    return isinstance(
        exc,
        (
            requests.exceptions.ConnectionError,
            requests.exceptions.Timeout,
            requests.exceptions.ChunkedEncodingError,
        ),
    )


def _retry_progress(
    *,
    phase: str,
    attempt: int,
    max_attempts: int,
    error: Optional[Exception] = None,
    extra: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    progress: Dict[str, Any] = {
        "phase": phase,
        "attempt": attempt,
        "max_attempts": max_attempts,
        "will_retry": attempt < max_attempts,
    }
    if error is not None:
        progress["last_error"] = str(error)
    if extra:
        progress.update(extra)
    return progress


def _touch_ingest(
    task: IngestTask, ingest_store: IngestStore, message: Optional[str] = None
) -> None:
    task.updated_at = _utc_now_iso()
    if message:
        task.message = message
    ingest_store.save(task)


def _update_step(
    task: IngestTask,
    ingest_store: IngestStore,
    step_name: str,
    *,
    status: Optional[str] = None,
    message: Optional[str] = None,
    progress: Optional[Dict[str, Any]] = None,
    result: Optional[Dict[str, Any]] = None,
    error: Optional[str] = None,
) -> StepStatus:
    step = task.steps[step_name]
    if status is not None:
        step.status = status
    if message is not None:
        step.message = message
        task.message = message
    if progress is not None:
        step.progress = progress
    if result is not None:
        step.result = result
    if error is not None:
        step.error = error
    task.updated_at = _utc_now_iso()
    ingest_store.save(task)
    return step


def _load_json_file(path: Path) -> Optional[Dict[str, Any]]:
    if not path.is_file():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return None


def _existing_fetch_result(wiki, arxiv_id: str) -> Optional[Dict[str, Any]]:
    resolved = resolve_paper_assets(wiki.raw_dir, arxiv_id)
    pdf_path = resolved.get("pdf_path")
    json_path = resolved.get("json_path")
    if not isinstance(pdf_path, Path) or not pdf_path.is_file():
        return None

    metadata = _load_json_file(json_path) if isinstance(json_path, Path) else None
    if metadata is None:
        metadata = {
            "arxiv_id": normalize_arxiv_identifier(arxiv_id),
            "title": "Unknown Title",
            "authors": [],
            "abstract": "",
            "categories": [],
        }

    return {
        "pdf_path": str(pdf_path),
        "metadata": metadata,
        "reused": True,
    }


def _existing_parse_result(wiki, arxiv_id: str) -> Optional[Dict[str, Any]]:
    resolved = resolve_paper_assets(wiki.raw_dir, arxiv_id)
    markdown_path = resolved.get("markdown_path")
    if not isinstance(markdown_path, Path) or not markdown_path.is_file():
        return None
    return {
        "markdown_path": str(markdown_path),
        "reused": True,
    }


def _content_range_total(value: Optional[str]) -> Optional[int]:
    if not value or "/" not in value:
        return None
    total = value.rsplit("/", 1)[-1]
    if total.isdigit():
        return int(total)
    return None


def _download_pdf_with_progress(
    *,
    task: IngestTask,
    ingest_store: IngestStore,
    url: str,
    pdf_path: Path,
    session: requests.Session,
    attempt: int = 1,
    max_attempts: int = 1,
) -> Path:
    pdf_path.parent.mkdir(parents=True, exist_ok=True)
    part_path = pdf_path.with_suffix(pdf_path.suffix + ".part")
    existing = part_path.stat().st_size if part_path.exists() else 0
    headers = {"Range": f"bytes={existing}-"} if existing else {}

    response = session.get(url, stream=True, timeout=(10, 300), headers=headers)
    response.raise_for_status()

    mode = "ab" if existing and response.status_code == 206 else "wb"
    downloaded = existing if mode == "ab" else 0
    if mode == "wb":
        existing = 0

    total = _content_range_total(response.headers.get("content-range"))
    if total is None:
        content_length = response.headers.get("content-length")
        if content_length and content_length.isdigit():
            total = int(content_length) + existing

    _update_step(
        task,
        ingest_store,
        "fetch",
        message="downloading pdf",
        progress={
            "phase": "download",
            "attempt": attempt,
            "max_attempts": max_attempts,
            "bytes_downloaded": downloaded,
            "bytes_total": total,
            "percent": (downloaded / total if total else None),
            "resumed": existing > 0,
        },
    )

    last_saved = downloaded
    with open(part_path, mode) as out:
        for chunk in response.iter_content(1024 * 64):
            if not chunk:
                continue
            out.write(chunk)
            downloaded += len(chunk)
            if downloaded - last_saved >= 1024 * 1024:
                _update_step(
                    task,
                    ingest_store,
                    "fetch",
                    message="downloading pdf",
                    progress={
                        "phase": "download",
                        "attempt": attempt,
                        "max_attempts": max_attempts,
                        "bytes_downloaded": downloaded,
                        "bytes_total": total,
                        "percent": (min(downloaded / total, 1.0) if total else None),
                        "resumed": existing > 0,
                    },
                )
                last_saved = downloaded

    os.replace(part_path, pdf_path)
    _update_step(
        task,
        ingest_store,
        "fetch",
        message="pdf download completed",
        progress={
            "phase": "download",
            "attempt": attempt,
            "max_attempts": max_attempts,
            "bytes_downloaded": pdf_path.stat().st_size,
            "bytes_total": total or pdf_path.stat().st_size,
            "percent": 1.0,
            "resumed": existing > 0,
        },
    )
    return pdf_path


def _fetch_paper_for_ingest(
    task: IngestTask, ingest_store: IngestStore, wiki, ingester
) -> Dict[str, Any]:
    if not task.options.get("force_fetch", False):
        existing = _existing_fetch_result(wiki, task.arxiv_id)
        if existing is not None:
            return existing

    canonical = normalize_arxiv_identifier(task.arxiv_id)
    metadata: Dict[str, Any]
    for attempt in range(1, FETCH_MAX_ATTEMPTS + 1):
        _update_step(
            task,
            ingest_store,
            "fetch",
            message=f"fetching arXiv metadata (attempt {attempt}/{FETCH_MAX_ATTEMPTS})",
            progress=_retry_progress(
                phase="metadata",
                attempt=attempt,
                max_attempts=FETCH_MAX_ATTEMPTS,
            ),
        )
        try:
            metadata = ingester.arxiv_fetcher.fetch_metadata(canonical)
            break
        except Exception as exc:
            if not _is_retryable_fetch_error(exc):
                raise
            if attempt == FETCH_MAX_ATTEMPTS:
                _update_step(
                    task,
                    ingest_store,
                    "fetch",
                    message=f"metadata fetch failed after {FETCH_MAX_ATTEMPTS} attempts",
                    progress=_retry_progress(
                        phase="metadata",
                        attempt=attempt,
                        max_attempts=FETCH_MAX_ATTEMPTS,
                        error=exc,
                    ),
                )
                raise
            _update_step(
                task,
                ingest_store,
                "fetch",
                message=(
                    f"metadata fetch failed; retrying "
                    f"({attempt}/{FETCH_MAX_ATTEMPTS})"
                ),
                progress=_retry_progress(
                    phase="metadata",
                    attempt=attempt,
                    max_attempts=FETCH_MAX_ATTEMPTS,
                    error=exc,
                ),
            )
            time.sleep(RETRY_DELAY_SECONDS)
    else:
        raise RuntimeError("metadata fetch retry loop exited unexpectedly")
    asset_id = normalize_arxiv_identifier(metadata.get("arxiv_id", canonical))

    json_path = wiki.get_paper_asset_path("json", asset_id)
    json_path.parent.mkdir(parents=True, exist_ok=True)
    json_path.write_text(json.dumps(metadata, indent=2), encoding="utf-8")

    resolved = resolve_paper_assets(wiki.raw_dir, canonical)
    existing_pdf = resolved.get("pdf_path")
    pdf_path = (
        existing_pdf
        if isinstance(existing_pdf, Path)
        and existing_pdf.is_file()
        and not task.options.get("force_fetch", False)
        else wiki.get_paper_asset_path("pdf", canonical)
    )

    if not pdf_path.is_file() or task.options.get("force_fetch", False):
        pdf_url = metadata.get("pdf_url")
        if not pdf_url:
            raise ValueError("arXiv metadata did not include a PDF URL")
        for attempt in range(1, FETCH_MAX_ATTEMPTS + 1):
            try:
                _download_pdf_with_progress(
                    task=task,
                    ingest_store=ingest_store,
                    url=pdf_url,
                    pdf_path=pdf_path,
                    session=ingester.arxiv_fetcher.session,
                    attempt=attempt,
                    max_attempts=FETCH_MAX_ATTEMPTS,
                )
                break
            except Exception as exc:
                if not _is_retryable_fetch_error(exc):
                    raise
                if attempt == FETCH_MAX_ATTEMPTS:
                    _update_step(
                        task,
                        ingest_store,
                        "fetch",
                        message=f"pdf download failed after {FETCH_MAX_ATTEMPTS} attempts",
                        progress=_retry_progress(
                            phase="download",
                            attempt=attempt,
                            max_attempts=FETCH_MAX_ATTEMPTS,
                            error=exc,
                        ),
                    )
                    raise
                _update_step(
                    task,
                    ingest_store,
                    "fetch",
                    message=f"pdf download failed; retrying ({attempt}/{FETCH_MAX_ATTEMPTS})",
                    progress=_retry_progress(
                        phase="download",
                        attempt=attempt,
                        max_attempts=FETCH_MAX_ATTEMPTS,
                        error=exc,
                    ),
                )
                time.sleep(RETRY_DELAY_SECONDS)

    return {
        "pdf_path": str(pdf_path),
        "metadata": metadata,
        "json_path": str(json_path),
        "reused": False,
    }


def _share_path_for_raw_path(config: ServerConfig, path: Path) -> str:
    raw_root = config.get_raw_root()
    rel = path.resolve().relative_to(raw_root).as_posix()
    return f"papers/{rel}"


def _public_share_url_for_path(config: ServerConfig, path: Path) -> str:
    """Return an absolute tokenized share URL for a local RAW_DIR asset."""
    rel = _share_path_for_raw_path(config, path)
    token = config.share_access_token
    if token is None:
        share_store = ShareStore(config.get_data_root() / "shares")
        ttl = config.default_share_expires_in
        if ttl is not None:
            ttl = max(ttl, int(config.mineru_timeout))
        share = create_share_record(
            share_store=share_store,
            config=config,
            paths=[rel],
            label=f"mineru asset: {path.name}",
            expires_in=ttl,
            created_by=None,
        )
        token = share["token"]
    return build_external_share_url(config, token, rel)


def _parse_with_mineru(
    *,
    task: IngestTask,
    ingest_store: IngestStore,
    config: ServerConfig,
    wiki,
    metadata: Optional[Dict[str, Any]],
    pdf_path: Path,
) -> Path:
    if not config.mineru_api_token:
        raise RuntimeError("MINERU_API_TOKEN is required when parser='mineru'")

    markdown_path = wiki.ingester._resolve_asset_path("markdown", task.arxiv_id, metadata)
    markdown_path.parent.mkdir(parents=True, exist_ok=True)

    public_pdf_url = _public_share_url_for_path(config, pdf_path)
    client = MinerUClient(
        config.mineru_api_token,
        base_url=config.mineru_api_base_url,
    )
    poll_interval = max(float(config.mineru_poll_interval), 1.0)
    last_error: Optional[Exception] = None

    for attempt in range(1, MINERU_MAX_ATTEMPTS + 1):
        try:
            mineru_task_id = client.submit_url_task(
                url=public_pdf_url,
                data_id=normalize_arxiv_identifier(task.arxiv_id).replace("/", "_"),
                model_version=config.mineru_model_version,
                language=config.mineru_language,
                enable_formula=config.mineru_enable_formula,
                enable_table=config.mineru_enable_table,
                is_ocr=config.mineru_is_ocr,
                no_cache=bool(task.options.get("mineru_no_cache", False)),
            )
            started = time.monotonic()

            _update_step(
                task,
                ingest_store,
                "parse",
                message=(
                    f"submitted MinerU parse task "
                    f"(attempt {attempt}/{MINERU_MAX_ATTEMPTS})"
                ),
                progress={
                    "parser": "mineru",
                    "attempt": attempt,
                    "max_attempts": MINERU_MAX_ATTEMPTS,
                    "mineru_task_id": mineru_task_id,
                    "state": "submitted",
                    "public_pdf_url_configured": True,
                },
            )

            while True:
                state_payload = client.get_task(mineru_task_id)
                state = str(state_payload.get("state", "unknown"))
                progress = state_payload.get("extract_progress")
                _update_step(
                    task,
                    ingest_store,
                    "parse",
                    message=f"MinerU parse {state}",
                    progress={
                        "parser": "mineru",
                        "attempt": attempt,
                        "max_attempts": MINERU_MAX_ATTEMPTS,
                        "mineru_task_id": mineru_task_id,
                        "state": state,
                        "extract_progress": progress,
                        "public_pdf_url_configured": True,
                    },
                )

                if state == "done":
                    zip_url = state_payload.get("full_zip_url")
                    if not zip_url:
                        raise RuntimeError("MinerU task finished without full_zip_url")
                    client.download_markdown_from_zip(str(zip_url), markdown_path)
                    _update_step(
                        task,
                        ingest_store,
                        "parse",
                        message="MinerU markdown downloaded",
                        progress={
                            "parser": "mineru",
                            "attempt": attempt,
                            "max_attempts": MINERU_MAX_ATTEMPTS,
                            "mineru_task_id": mineru_task_id,
                            "state": state,
                            "extract_progress": progress,
                            "full_zip_url": zip_url,
                            "public_pdf_url_configured": True,
                            "percent": 1.0,
                        },
                    )
                    return markdown_path
                if state == "failed":
                    raise RuntimeError(state_payload.get("err_msg") or "MinerU task failed")
                if time.monotonic() - started > config.mineru_timeout:
                    raise TimeoutError(f"MinerU task timed out after {config.mineru_timeout}s")
                time.sleep(poll_interval)
        except Exception as exc:
            last_error = exc
            if attempt == MINERU_MAX_ATTEMPTS:
                _update_step(
                    task,
                    ingest_store,
                    "parse",
                    message=f"MinerU parse failed after {MINERU_MAX_ATTEMPTS} attempts",
                    progress={
                        "parser": "mineru",
                        "attempt": attempt,
                        "max_attempts": MINERU_MAX_ATTEMPTS,
                        "state": "failed",
                        "last_error": str(exc),
                        "will_retry": False,
                        "public_pdf_url_configured": True,
                    },
                )
                break
            _update_step(
                task,
                ingest_store,
                "parse",
                message=f"MinerU parse failed; retrying ({attempt}/{MINERU_MAX_ATTEMPTS})",
                progress={
                    "parser": "mineru",
                    "attempt": attempt,
                    "max_attempts": MINERU_MAX_ATTEMPTS,
                    "state": "retrying",
                    "last_error": str(exc),
                    "will_retry": True,
                    "public_pdf_url_configured": True,
                },
            )
            time.sleep(RETRY_DELAY_SECONDS)

    raise RuntimeError(last_error or "MinerU parse failed")


def _parse_paper_for_ingest(
    *,
    task: IngestTask,
    ingest_store: IngestStore,
    config: ServerConfig,
    wiki,
    ingester,
    metadata: Optional[Dict[str, Any]],
    pdf_path: Optional[str],
) -> Path:
    markdown_path = ingester._resolve_asset_path("markdown", task.arxiv_id, metadata)
    if markdown_path.is_file() and not task.options.get("force_parse", False):
        _update_step(
            task,
            ingest_store,
            "parse",
            message="reusing existing markdown",
            progress={"parser": task.options["parser"], "percent": 1.0},
        )
        return markdown_path

    parser = task.options["parser"]
    if parser == "mineru":
        if not pdf_path:
            raise FileNotFoundError("PDF not found for MinerU parsing")
        return _parse_with_mineru(
            task=task,
            ingest_store=ingest_store,
            config=config,
            wiki=wiki,
            metadata=metadata,
            pdf_path=Path(pdf_path),
        )

    _update_step(
        task,
        ingest_store,
        "parse",
        message="parsing pdf with PyMuPDF",
        progress={"parser": "pymupdf"},
    )
    return ingester._parse_pdf(task.arxiv_id, metadata, pdf_path=pdf_path)



def execute_ingest(task_id: str, ingest_store: IngestStore, config: ServerConfig) -> None:
    """Background ingest with per-step persistence (runs in a worker thread)."""
    task = ingest_store.get(task_id)
    if task is None:
        return

    wiki = _configured_wiki_engine(config, enable_neo4j_sync=False)
    ingester = wiki.ingester
    ingest_result: Dict[str, Any] = {"steps": {}}

    task.status = "running"
    task.started_at = _utc_now_iso()
    _touch_ingest(task, ingest_store, "ingest started")

    metadata: Optional[Dict[str, Any]] = None

    # --- fetch ---
    if task.options.get("fetch", True):
        st = task.steps["fetch"]
        st.status = "running"
        st.started_at = _utc_now_iso()
        st.message = "fetch step started"
        _touch_ingest(task, ingest_store, "fetch step started")
        try:
            fetch_result = _fetch_paper_for_ingest(task, ingest_store, wiki, ingester)
            metadata = fetch_result.get("metadata")
            st.status = "succeeded"
            st.message = (
                "reused existing pdf" if fetch_result.get("reused") else "pdf and metadata fetched"
            )
            st.progress = {
                **(st.progress or {}),
                "phase": "complete",
                "percent": 1.0,
            }
            st.result = {
                "pdf_path": fetch_result["pdf_path"],
                "title": metadata.get("title") if metadata else None,
                "json_path": fetch_result.get("json_path"),
                "reused": fetch_result.get("reused", False),
            }
            ingest_result["steps"]["fetch"] = {
                "pdf_path": fetch_result["pdf_path"],
                "metadata": metadata,
            }
        except Exception as e:
            logger.exception("ingest fetch failed")
            st.status = "failed"
            st.message = "fetch failed"
            st.error = _friendly_fetch_error(e, task.arxiv_id)
        st.finished_at = _utc_now_iso()
        _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["fetch"]
        st.status = "skipped"
        st.message = "fetch skipped by request"
        st.finished_at = _utc_now_iso()
        existing = _existing_fetch_result(wiki, task.arxiv_id)
        if existing is not None:
            metadata = existing.get("metadata")
            st.result = {
                "pdf_path": existing["pdf_path"],
                "title": metadata.get("title") if metadata else None,
                "reused": True,
            }
            ingest_result["steps"]["fetch"] = {
                "pdf_path": existing["pdf_path"],
                "metadata": metadata,
            }
            st.message = "fetch skipped; using existing local paper assets"
        _touch_ingest(task, ingest_store, st.message)

    fetch_st = task.steps["fetch"].status
    fetch_ok = fetch_st in ("succeeded", "skipped")

    # --- parse ---
    if task.options.get("parse", True):
        if fetch_ok and "fetch" in ingest_result["steps"]:
            st = task.steps["parse"]
            st.status = "running"
            st.started_at = _utc_now_iso()
            st.message = "parse step started"
            _touch_ingest(task, ingest_store, "parse step started")
            try:
                fetch_step = ingest_result["steps"].get("fetch", {})
                md_path = _parse_paper_for_ingest(
                    task=task,
                    ingest_store=ingest_store,
                    config=config,
                    wiki=wiki,
                    ingester=ingester,
                    metadata=metadata,
                    pdf_path=fetch_step.get("pdf_path"),
                )
                st.status = "succeeded"
                st.message = "markdown ready"
                st.progress = {
                    **(st.progress or {}),
                    "percent": 1.0,
                }
                st.result = {
                    "markdown_path": str(md_path),
                    "parser": task.options["parser"],
                }
                ingest_result["steps"]["parse"] = {"markdown_path": str(md_path)}
            except Exception as e:
                logger.exception("ingest parse failed")
                st.status = "failed"
                st.message = "parse failed"
                st.error = _friendly_parse_error(e)
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        else:
            st = task.steps["parse"]
            if task.steps["fetch"].status == "failed":
                st.status = "skipped"
                st.message = "parse skipped"
                st.error = "skipped because fetch failed"
            else:
                st.status = "failed"
                st.message = "parse failed"
                st.error = "local PDF metadata is missing; run fetch first or keep fetch=true"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["parse"]
        st.status = "skipped"
        st.message = "parse skipped by request"
        st.finished_at = _utc_now_iso()
        existing_parse = _existing_parse_result(wiki, task.arxiv_id)
        if existing_parse is not None:
            st.result = existing_parse
            st.message = "parse skipped; using existing local markdown"
            ingest_result["steps"]["parse"] = {
                "markdown_path": existing_parse["markdown_path"],
            }
        _touch_ingest(task, ingest_store, st.message)

    statuses = [s.status for s in task.steps.values()]
    if all(s in ("succeeded", "skipped") for s in statuses):
        task.status = "succeeded"
    elif any(s == "failed" for s in statuses) and any(s == "succeeded" for s in statuses):
        task.status = "partial"
    elif any(s == "failed" for s in statuses):
        task.status = "failed"
    else:
        task.status = "succeeded"

    task.finished_at = _utc_now_iso()
    task.message = f"ingest {task.status}"
    _touch_ingest(task, ingest_store, task.message)


@router.post("/ingest/paper", response_model=IngestQueuedResponse, status_code=202)
async def ingest_paper(
    request: Request,
    body: IngestRequest,
    background_tasks: BackgroundTasks,
):
    """Queue paper ingestion for server-managed raw assets and task state."""
    config: ServerConfig = request.app.state.config
    ingest_store: IngestStore = request.app.state.ingest_store

    fetcher = ArxivFetcher()
    arxiv_norm = fetcher._normalize_arxiv_id(body.arxiv_id)
    if not fetcher._is_valid_arxiv_id(arxiv_norm):
        raise HTTPException(status_code=400, detail="invalid arxiv_id format")

    task_id = str(uuid.uuid4())[:8]
    now = _utc_now_iso()
    requester = request.headers.get(config.user_header) if config.user_header else None

    flags = _flags_from_stage_control(
        stages=body.stages,
        stop_after=body.stop_after,
        fallback={
            "fetch": body.fetch,
            "parse": body.parse,
        },
    )
    if not any(flags.values()):
        raise HTTPException(status_code=400, detail="no ingest stages selected")
    options = _stage_options(
        flags=flags,
        parser=body.parser,
        force_fetch=body.force_fetch,
        force_parse=body.force_parse,
        mineru_no_cache=body.mineru_no_cache,
    )
    if body.stages is not None:
        options["requested_stages"] = body.stages
    if body.stop_after is not None:
        options["stop_after"] = body.stop_after

    task = _new_ingest_task(
        task_id=task_id,
        arxiv_id=arxiv_norm,
        requester=requester,
        options=options,
        submitted_at=now,
    )
    ingest_store.save(task)
    background_tasks.add_task(execute_ingest, task_id, ingest_store, config)

    return IngestQueuedResponse(
        task_id=task_id,
        status="queued",
        message="ingest queued; poll GET /api/ingest/{task_id}",
    )



@router.get("/ingest/stages")
async def ingest_stages():
    """Return the ordered ingest stages clients can display and control."""
    return {
        "order": list(STAGE_ORDER),
        "stages": [
            {
                "name": stage,
                "description": STAGE_DESCRIPTIONS[stage],
                "can_stop_after": True,
            }
            for stage in STAGE_ORDER
        ],
        "controls": {
            "stop_after": "Run from the beginning through this stage.",
            "stages": "Run exactly these stages, reusing local assets where earlier stages are skipped.",
            "continue": "POST /api/ingest/{task_id}/continue to start a new task from local results.",
        },
    }


@router.post(
    "/ingest/{task_id}/continue",
    response_model=IngestQueuedResponse,
    status_code=202,
)
async def continue_ingest_task(
    request: Request,
    task_id: str,
    body: ContinueIngestRequest,
    background_tasks: BackgroundTasks,
):
    """Queue a continuation task that reuses local results from an earlier ingest."""
    config: ServerConfig = request.app.state.config
    ingest_store: IngestStore = request.app.state.ingest_store
    previous = ingest_store.get(task_id)
    if previous is None:
        raise HTTPException(status_code=404, detail="ingest task not found")

    requester = request.headers.get(config.user_header) if config.user_header else None
    new_task_id = str(uuid.uuid4())[:8]
    now = _utc_now_iso()

    flags = _continue_stage_flags(
        previous,
        stages=body.stages,
        stop_after=body.stop_after,
    )
    if not any(flags.values()):
        raise HTTPException(status_code=400, detail="no ingest stages selected to continue")

    options = _stage_options(
        flags=flags,
        parser=body.parser,
        force_fetch=body.force_fetch,
        force_parse=body.force_parse,
        mineru_no_cache=body.mineru_no_cache,
        source_task_id=task_id,
    )
    if body.stages is not None:
        options["requested_stages"] = body.stages
    if body.stop_after is not None:
        options["stop_after"] = body.stop_after

    task = _new_ingest_task(
        task_id=new_task_id,
        arxiv_id=previous.arxiv_id,
        requester=requester,
        options=options,
        submitted_at=now,
        message="ingest continuation queued",
    )
    ingest_store.save(task)
    background_tasks.add_task(execute_ingest, new_task_id, ingest_store, config)

    return IngestQueuedResponse(
        task_id=new_task_id,
        status="queued",
        message="ingest continuation queued; poll GET /api/ingest/{task_id}",
    )


@router.get("/ingest/{task_id}")
async def get_ingest_task(request: Request, task_id: str):
    """Return ingest task status and per-step details."""
    ingest_store: IngestStore = request.app.state.ingest_store
    task = ingest_store.get(task_id)
    if task is None:
        raise HTTPException(status_code=404, detail="ingest task not found")
    return task.model_dump(mode="json")


@router.get("/ingests")
async def list_ingest_tasks(request: Request, limit: int = Query(50, ge=1, le=500)):
    """List recent ingest tasks."""
    ingest_store: IngestStore = request.app.state.ingest_store
    tasks = ingest_store.list_all(limit=limit)
    return {"total": len(tasks), "tasks": [t.model_dump(mode="json") for t in tasks]}


# === Lint API ===


@router.get("/lint")
async def run_lint(request: Request, fix: bool = False):
    """Run wiki lint checks."""
    if fix:
        raise HTTPException(
            status_code=400,
            detail="server-side wiki fixes are disabled; run wiki lint fixes from a writable client checkout",
        )
    wiki = _configured_wiki_engine(request.app.state.config, enable_neo4j_sync=False)
    result = wiki.lint(fix=fix)

    return result
