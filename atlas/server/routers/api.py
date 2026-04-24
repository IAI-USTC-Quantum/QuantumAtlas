"""
API Router

REST API endpoints for programmatic access.
"""

import json
import logging
import os
import subprocess
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Literal, Optional

import requests
from fastapi import APIRouter, BackgroundTasks, HTTPException, Query, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel

from atlas.paper_assets import normalize_arxiv_identifier, resolve_paper_assets
from atlas.parser.arxiv_fetcher import ArxivFetcher
from atlas.parser.mineru_client import MinerUClient
from atlas.runtime_metadata import code_git_info
from atlas.server.config import ServerConfig, get_project_root
from atlas.server.routers.shares import build_external_share_url, create_share_record
from atlas.server.tasks import IngestStore, IngestTask, ShareStore, StepStatus

router = APIRouter()
logger = logging.getLogger(__name__)

StageName = Literal["fetch", "parse", "extract", "wiki", "neo4j"]
STAGE_ORDER: tuple[StageName, ...] = ("fetch", "parse", "extract", "wiki", "neo4j")
STAGE_DESCRIPTIONS: Dict[StageName, str] = {
    "fetch": "Fetch arXiv metadata and PDF into the local paper asset store.",
    "parse": "Parse the local PDF into Markdown with PyMuPDF or MinerU.",
    "extract": "Extract structured algorithm information, either server-side or client-reviewed.",
    "wiki": "Create or update Wiki pages from the fetched metadata and extraction result.",
    "neo4j": "Sync the resulting Wiki pages into Neo4j.",
}
FETCH_MAX_ATTEMPTS = 3
MINERU_MAX_ATTEMPTS = 2
RETRY_DELAY_SECONDS = 0.2


def _configured_wiki_engine(config: ServerConfig, *, enable_neo4j_sync: bool = False):
    from atlas.wiki.engine import WikiEngine

    return WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=enable_neo4j_sync,
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
    arxiv_id: str
    fetch: bool = True
    parse: bool = True
    extract: bool = True
    create_wiki: bool = True
    sync_neo4j: bool = True
    stages: Optional[List[StageName]] = None
    stop_after: Optional[StageName] = None
    parser: Literal["pymupdf", "mineru"] = "pymupdf"
    llm_provider: str = "openai"
    force_fetch: bool = False
    force_parse: bool = False
    mineru_no_cache: bool = False


class ClientReviewedAlgorithm(BaseModel):
    """Algorithm extraction reviewed outside the server."""

    id: Optional[str] = None
    name: Optional[str] = None
    algorithm_id: Optional[str] = None
    algorithm_name: Optional[str] = None
    description: Optional[str] = None
    problem_type: str = "unknown"
    primitives: List[str] = []
    complexity: Dict[str, Any] = {}
    pseudocode: Optional[str] = None
    input_params: List[str] = []
    output_params: List[str] = []
    assumptions: List[str] = []


class ReviewedExtractionRequest(BaseModel):
    """Submit client-side LLM extraction after human review."""

    arxiv_id: str
    algorithm: Optional[ClientReviewedAlgorithm] = None
    algorithm_ir: Optional[Dict[str, Any]] = None
    metadata: Optional[Dict[str, Any]] = None
    create_wiki: bool = True
    sync_neo4j: bool = True
    reviewed_by: Optional[str] = None
    source: str = "client"
    notes: Optional[str] = None


class ContinueIngestRequest(BaseModel):
    """Continue an earlier ingest task from local results."""

    stages: Optional[List[StageName]] = None
    stop_after: Optional[StageName] = None
    parser: Literal["pymupdf", "mineru"] = "pymupdf"
    llm_provider: str = "openai"
    force_fetch: bool = False
    force_parse: bool = False
    mineru_no_cache: bool = False
    algorithm: Optional[ClientReviewedAlgorithm] = None
    algorithm_ir: Optional[Dict[str, Any]] = None
    metadata: Optional[Dict[str, Any]] = None
    create_wiki: bool = True
    sync_neo4j: bool = True
    reviewed_by: Optional[str] = None
    source: str = "client"
    notes: Optional[str] = None


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
    llm_provider: str,
    force_fetch: bool,
    force_parse: bool,
    mineru_no_cache: bool,
    source_task_id: Optional[str] = None,
) -> Dict[str, Any]:
    options: Dict[str, Any] = {
        "fetch": flags["fetch"],
        "parse": flags["parse"],
        "extract": flags["extract"],
        "create_wiki": flags["wiki"],
        "sync_neo4j": flags["neo4j"],
        "parser": parser,
        "llm_provider": llm_provider,
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

    return {
        "enabled": True,
        "branch": branch,
        "commit": commit,
        "upstream": upstream,
        "ahead": ahead,
        "behind": behind,
        "dirty": dirty,
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
            "require_release_tag": config.require_release_tag,
            "git": code_git_info(get_project_root()),
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


@router.post("/wiki/sync/pull")
def wiki_sync_pull(request: Request):
    """Fetch and fast-forward the configured Wiki repository."""
    config: ServerConfig = request.app.state.config
    wiki_dir = _resolve_project_path(config.wiki_dir)
    if not wiki_dir.exists():
        raise HTTPException(status_code=409, detail="wiki directory does not exist")

    before = _git_info(wiki_dir)
    if not before.get("enabled"):
        raise HTTPException(status_code=409, detail="wiki directory is not a git repository")
    if before.get("dirty"):
        raise HTTPException(status_code=409, detail="wiki worktree has local changes")

    old_commit = _git_output(wiki_dir, "rev-parse", "--short", "HEAD")

    fetch = _git_run(wiki_dir, "fetch", "--prune", timeout=30)
    if fetch is None:
        raise HTTPException(status_code=500, detail="git fetch could not be executed")
    if fetch.returncode != 0:
        detail = (fetch.stderr or fetch.stdout or "git fetch failed").strip()
        raise HTTPException(status_code=502, detail=detail)

    after_fetch = _git_info(wiki_dir)
    if after_fetch.get("dirty"):
        raise HTTPException(status_code=409, detail="wiki worktree has local changes")

    pull = _git_run(wiki_dir, "pull", "--ff-only", timeout=30)
    if pull is None:
        raise HTTPException(status_code=500, detail="git pull could not be executed")
    if pull.returncode != 0:
        detail = (pull.stderr or pull.stdout or "git pull --ff-only failed").strip()
        raise HTTPException(status_code=409, detail=detail)

    new_commit = _git_output(wiki_dir, "rev-parse", "--short", "HEAD")
    return {
        "status": "succeeded",
        "changed": old_commit != new_commit,
        "old_commit": old_commit,
        "new_commit": new_commit,
        **_wiki_sync_status(config),
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
        from atlas.knowledge.neo4j_client import Neo4jClient
        from atlas.server.config import get_config

        config = get_config()
        client = Neo4jClient(
            uri=config.neo4j_uri,
            username=config.neo4j_user,
            password=config.neo4j_password,
        )
        client.connect()
        stats = client.get_stats()
        client.close()
        return stats
    except Exception as e:
        return {"error": str(e)}


@router.post("/graph/query")
async def graph_query(request: GraphQueryRequest):
    """Execute a Cypher query."""
    try:
        from atlas.knowledge.neo4j_client import Neo4jClient
        from atlas.server.config import get_config

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
        from atlas.knowledge.neo4j_client import Neo4jClient
        from atlas.server.config import get_config

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


def _friendly_extract_error(exc: Exception) -> str:
    msg = str(exc).lower()
    if "api_key" in msg or "openai" in msg or "anthropic" in msg:
        return "LLM extraction requires OPENAI_API_KEY or ANTHROPIC_API_KEY"
    if "rate" in msg or "429" in msg:
        return "LLM API rate limited, please retry later"
    return f"LLM extraction failed: {exc}"


def _friendly_wiki_error(exc: Exception) -> str:
    if "No space left" in str(exc) or getattr(exc, "errno", None) == 28:
        return f"Failed to write wiki page: {exc}"
    return f"Wiki page creation failed: {exc}"


def _friendly_neo4j_error(exc: Exception) -> str:
    low = str(exc).lower()
    if "refused" in low or "could not connect" in low:
        return f"Neo4j connection failed: {exc}"
    return f"Neo4j sync failed: {exc}"


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


def _requester_label(requester: Optional[str]) -> str:
    """Return a stable display label for requesters in audit logs."""
    if requester is None:
        return "anonymous"
    cleaned = requester.strip()
    return cleaned or "anonymous"


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
            progress={"parser": task.options.get("parser", "pymupdf"), "percent": 1.0},
        )
        return markdown_path

    parser = task.options.get("parser", "pymupdf")
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


def _reviewed_extraction_step(
    body: ReviewedExtractionRequest,
    *,
    reviewer: Optional[str],
) -> Dict[str, Any]:
    payload: Dict[str, Any] = {}
    if body.algorithm_ir:
        payload.update(body.algorithm_ir)
    if body.algorithm:
        payload.update(body.algorithm.model_dump(exclude_none=True))

    algorithm_id = payload.get("algorithm_id") or payload.get("id")
    algorithm_name = payload.get("algorithm_name") or payload.get("name")
    if not algorithm_id or not algorithm_name:
        raise HTTPException(
            status_code=400,
            detail="reviewed extraction requires algorithm_id/id and algorithm_name/name",
        )

    primitives = payload.get("primitives") or []
    if not isinstance(primitives, list):
        raise HTTPException(status_code=400, detail="reviewed extraction primitives must be a list")

    complexity = payload.get("complexity") or {}
    if not isinstance(complexity, dict):
        raise HTTPException(
            status_code=400, detail="reviewed extraction complexity must be an object"
        )

    return {
        "algorithm_id": str(algorithm_id),
        "algorithm_name": str(algorithm_name),
        "description": payload.get("description"),
        "problem_type": payload.get("problem_type") or "unknown",
        "primitives": [str(p) for p in primitives],
        "complexity": complexity,
        "pseudocode": payload.get("pseudocode"),
        "input_params": payload.get("input_params") or [],
        "output_params": payload.get("output_params") or [],
        "assumptions": payload.get("assumptions") or [],
        "algorithm_ir": payload,
        "source": body.source,
        "reviewed": True,
        "reviewed_by": reviewer,
        "review_notes": body.notes,
    }


def _metadata_for_reviewed_submission(
    wiki,
    arxiv_id: str,
    submitted_metadata: Optional[Dict[str, Any]],
) -> Dict[str, Any]:
    metadata = dict(submitted_metadata or {})
    if not metadata:
        resolved = resolve_paper_assets(wiki.raw_dir, arxiv_id)
        json_path = resolved.get("json_path")
        if isinstance(json_path, Path):
            loaded = _load_json_file(json_path)
            if loaded:
                metadata = loaded

    if not metadata:
        raise FileNotFoundError(
            "reviewed extraction needs local raw/json metadata or metadata in the request"
        )

    metadata.setdefault("arxiv_id", normalize_arxiv_identifier(arxiv_id))
    metadata.setdefault("title", "Unknown Title")
    metadata.setdefault("authors", [])
    metadata.setdefault("abstract", "")
    metadata.setdefault("categories", [])

    json_path = wiki.get_paper_asset_path("json", metadata["arxiv_id"])
    json_path.parent.mkdir(parents=True, exist_ok=True)
    if submitted_metadata:
        json_path.write_text(json.dumps(metadata, indent=2, ensure_ascii=False), encoding="utf-8")

    resolved = resolve_paper_assets(wiki.raw_dir, arxiv_id)
    pdf_path = resolved.get("pdf_path")
    return {
        "metadata": metadata,
        "pdf_path": str(pdf_path) if isinstance(pdf_path, Path) else None,
        "json_path": str(json_path),
        "metadata_source": "request" if submitted_metadata else "local_json",
    }


def _finish_ingest_task(task: IngestTask, ingest_store: IngestStore) -> None:
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


def _any_failed(task: IngestTask, stages: tuple[str, ...]) -> bool:
    return any(task.steps.get(stage) and task.steps[stage].status == "failed" for stage in stages)


def execute_reviewed_extraction(
    task_id: str,
    ingest_store: IngestStore,
    config: ServerConfig,
) -> None:
    """Create wiki/graph artifacts from a client-reviewed extraction payload."""
    task = ingest_store.get(task_id)
    if task is None:
        return

    enable_sync = bool(task.options.get("sync_neo4j", True))
    wiki = _configured_wiki_engine(config, enable_neo4j_sync=enable_sync)
    ingester = wiki.ingester
    ingest_result: Dict[str, Any] = {"steps": {}}
    actor = _requester_label(task.requester)

    task.status = "running"
    task.started_at = _utc_now_iso()
    _touch_ingest(task, ingest_store, "reviewed extraction ingest started")

    # Existing metadata replaces the fetch/download layer for this client-driven path.
    st = task.steps["fetch"]
    st.status = "skipped"
    st.started_at = _utc_now_iso()
    st.message = "fetch skipped; using reviewed extraction metadata"
    try:
        fetch_result = _metadata_for_reviewed_submission(
            wiki,
            task.arxiv_id,
            task.options.get("metadata"),
        )
        metadata = fetch_result["metadata"]
        st.result = {
            "pdf_path": fetch_result.get("pdf_path"),
            "json_path": fetch_result.get("json_path"),
            "title": metadata.get("title"),
            "metadata_source": fetch_result.get("metadata_source"),
        }
        ingest_result["steps"]["fetch"] = {
            "pdf_path": fetch_result.get("pdf_path"),
            "metadata": metadata,
        }
    except Exception as e:
        logger.exception("reviewed extraction metadata load failed")
        st.status = "failed"
        st.message = "metadata load failed"
        st.error = _friendly_fetch_error(e, task.arxiv_id)
    st.finished_at = _utc_now_iso()
    _touch_ingest(task, ingest_store, st.message)

    st = task.steps["parse"]
    st.status = "skipped"
    st.started_at = _utc_now_iso()
    st.finished_at = _utc_now_iso()
    st.message = "parse skipped; client supplied reviewed extraction"
    _touch_ingest(task, ingest_store, st.message)

    st = task.steps["extract"]
    st.started_at = _utc_now_iso()
    if task.steps["fetch"].status in ("skipped", "succeeded") and "fetch" in ingest_result["steps"]:
        st.status = "succeeded"
        st.message = "reviewed client extraction accepted"
        st.result = task.options["reviewed_extraction"]
        ingest_result["steps"]["extract"] = task.options["reviewed_extraction"]
    else:
        st.status = "skipped"
        st.message = "reviewed extraction skipped"
        st.error = "skipped because metadata is missing"
    st.finished_at = _utc_now_iso()
    _touch_ingest(task, ingest_store, st.message)

    if task.options.get("create_wiki", True):
        if "fetch" in ingest_result["steps"] and "extract" in ingest_result["steps"]:
            st = task.steps["wiki"]
            st.status = "running"
            st.started_at = _utc_now_iso()
            st.message = "creating wiki pages from reviewed extraction"
            _touch_ingest(task, ingest_store, st.message)
            try:
                pages = ingester._create_wiki_pages(task.arxiv_id, ingest_result)
                st.status = "succeeded"
                st.message = "wiki pages created from reviewed extraction"
                st.result = {
                    "pages_created": len(pages),
                    "page_ids": [p.frontmatter.id for p in pages],
                }
                wiki.update_index()
                wiki.append_to_log(
                    f"[INGEST] {task.arxiv_id}: Applied reviewed extraction "
                    f"({len(pages)} wiki pages, task {task_id}, requested by {actor})"
                )
            except Exception as e:
                logger.exception("reviewed extraction wiki creation failed")
                st.status = "failed"
                st.message = "wiki page creation failed"
                st.error = _friendly_wiki_error(e)
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        else:
            st = task.steps["wiki"]
            st.status = "skipped"
            st.message = "wiki creation skipped"
            st.error = "skipped because metadata or reviewed extraction is missing"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["wiki"]
        st.status = "skipped"
        st.message = "wiki creation skipped by request"
        st.finished_at = _utc_now_iso()
        _touch_ingest(task, ingest_store, st.message)

    if task.options.get("sync_neo4j", True):
        if task.steps["wiki"].status == "succeeded":
            st = task.steps["neo4j"]
            st.status = "running"
            st.started_at = _utc_now_iso()
            st.message = "syncing reviewed extraction pages to Neo4j"
            _touch_ingest(task, ingest_store, st.message)
            try:
                sync_result = wiki.sync_to_neo4j()
                if isinstance(sync_result, dict) and sync_result.get("success") is False:
                    st.status = "failed"
                    st.message = "Neo4j sync failed"
                    st.error = sync_result.get("error", "Neo4j sync failed")
                elif isinstance(sync_result, dict) and sync_result.get("failed", 0) > 0:
                    st.status = "failed"
                    st.message = "Neo4j sync failed"
                    st.error = f"{sync_result.get('failed')} page(s) failed to sync"
                    st.result = sync_result
                else:
                    st.status = "succeeded"
                    st.message = "Neo4j sync completed"
                    st.result = (
                        sync_result if isinstance(sync_result, dict) else {"result": sync_result}
                    )
            except Exception as e:
                logger.exception("reviewed extraction neo4j sync failed")
                st.status = "failed"
                st.message = "Neo4j sync failed"
                st.error = _friendly_neo4j_error(e)
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        else:
            st = task.steps["neo4j"]
            st.status = "skipped"
            st.message = "Neo4j sync skipped"
            st.error = "skipped because wiki step did not succeed"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["neo4j"]
        st.status = "skipped"
        st.message = "Neo4j sync skipped by request"
        st.finished_at = _utc_now_iso()
        _touch_ingest(task, ingest_store, st.message)

    _finish_ingest_task(task, ingest_store)


def execute_ingest(task_id: str, ingest_store: IngestStore, config: ServerConfig) -> None:
    """Background ingest with per-step persistence (runs in a worker thread)."""
    task = ingest_store.get(task_id)
    if task is None:
        return

    enable_sync = bool(task.options.get("sync_neo4j", True))
    wiki = _configured_wiki_engine(config, enable_neo4j_sync=enable_sync)
    ingester = wiki.ingester
    ingest_result: Dict[str, Any] = {"steps": {}}
    actor = _requester_label(task.requester)

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
                    "parser": task.options.get("parser", "pymupdf"),
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

    # --- extract ---
    if task.options.get("extract", True):
        if task.steps["parse"].status == "succeeded" or "parse" in ingest_result["steps"]:
            st = task.steps["extract"]
            st.status = "running"
            st.started_at = _utc_now_iso()
            st.message = "LLM extraction started"
            _touch_ingest(task, ingest_store, st.message)
            try:
                algorithm_ir = ingester._extract_algorithm(
                    task.arxiv_id,
                    task.options.get("llm_provider", "openai"),
                )
                extract_step = {
                    "algorithm_id": algorithm_ir.id,
                    "algorithm_name": algorithm_ir.name,
                    "primitives": algorithm_ir.primitives,
                }
                ingest_result["steps"]["extract"] = extract_step
                st.status = "succeeded"
                st.message = "LLM extraction completed"
                st.result = extract_step
            except Exception as e:
                logger.exception("ingest extract failed")
                st.status = "failed"
                st.message = "LLM extraction failed"
                st.error = _friendly_extract_error(e)
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        else:
            st = task.steps["extract"]
            st.status = "skipped"
            st.message = "LLM extraction skipped"
            st.error = "skipped because parse did not succeed"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["extract"]
        st.status = "skipped"
        st.message = "LLM extraction skipped by request"
        st.finished_at = _utc_now_iso()
        _touch_ingest(task, ingest_store, st.message)

    # --- wiki ---
    if task.options.get("create_wiki", True):
        if _any_failed(task, ("fetch", "parse", "extract")):
            st = task.steps["wiki"]
            st.status = "skipped"
            st.message = "wiki creation skipped"
            st.error = "skipped because an earlier ingest stage failed"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        elif "fetch" in ingest_result["steps"]:
            st = task.steps["wiki"]
            st.status = "running"
            st.started_at = _utc_now_iso()
            st.message = "creating wiki pages"
            _touch_ingest(task, ingest_store, st.message)
            try:
                pages = ingester._create_wiki_pages(task.arxiv_id, ingest_result)
                st.status = "succeeded"
                st.message = "wiki pages created"
                st.result = {
                    "pages_created": len(pages),
                    "page_ids": [p.frontmatter.id for p in pages],
                }
                wiki.update_index()
                wiki.append_to_log(
                    f"[INGEST] {task.arxiv_id}: Created {len(pages)} wiki pages "
                    f"(task {task_id}, requested by {actor})"
                )
            except Exception as e:
                logger.exception("ingest wiki failed")
                st.status = "failed"
                st.message = "wiki page creation failed"
                st.error = _friendly_wiki_error(e)
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        else:
            st = task.steps["wiki"]
            st.status = "skipped"
            st.message = "wiki creation skipped"
            st.error = "skipped because fetch metadata is missing"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["wiki"]
        st.status = "skipped"
        st.message = "wiki creation skipped by request"
        st.finished_at = _utc_now_iso()
        _touch_ingest(task, ingest_store, st.message)

    # --- neo4j ---
    if task.options.get("sync_neo4j", True):
        if task.steps["wiki"].status == "succeeded":
            st = task.steps["neo4j"]
            st.status = "running"
            st.started_at = _utc_now_iso()
            st.message = "syncing wiki to Neo4j"
            _touch_ingest(task, ingest_store, st.message)
            try:
                sync_result = wiki.sync_to_neo4j()
                if not isinstance(sync_result, dict):
                    st.status = "succeeded"
                    st.message = "Neo4j sync completed"
                    st.result = {"result": sync_result}
                elif sync_result.get("success") is False:
                    st.status = "failed"
                    st.message = "Neo4j sync failed"
                    st.error = sync_result.get("error", "Neo4j sync failed")
                elif sync_result.get("failed", 0) > 0:
                    st.status = "failed"
                    st.message = "Neo4j sync failed"
                    st.error = f"{sync_result.get('failed')} page(s) failed to sync"
                    st.result = sync_result
                else:
                    st.status = "succeeded"
                    st.message = "Neo4j sync completed"
                    st.result = sync_result
            except Exception as e:
                logger.exception("ingest neo4j failed")
                st.status = "failed"
                st.message = "Neo4j sync failed"
                st.error = _friendly_neo4j_error(e)
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
        else:
            st = task.steps["neo4j"]
            st.status = "skipped"
            st.message = "Neo4j sync skipped"
            st.error = "skipped because wiki step did not succeed"
            st.finished_at = _utc_now_iso()
            _touch_ingest(task, ingest_store, st.message)
    else:
        st = task.steps["neo4j"]
        st.status = "skipped"
        st.message = "Neo4j sync skipped by request"
        st.finished_at = _utc_now_iso()
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
    """Queue full paper ingestion (fetch → parse → extract → wiki → optional Neo4j)."""
    config: ServerConfig = request.app.state.config
    ingest_store: IngestStore = request.app.state.ingest_store

    fetcher = ArxivFetcher()
    arxiv_norm = fetcher._normalize_arxiv_id(body.arxiv_id)
    if not fetcher._is_valid_arxiv_id(arxiv_norm):
        raise HTTPException(status_code=400, detail="invalid arxiv_id format")

    task_id = str(uuid.uuid4())[:8]
    now = _utc_now_iso()
    requester = request.headers.get(config.user_header) if config.user_header else None
    actor = _requester_label(requester)

    flags = _flags_from_stage_control(
        stages=body.stages,
        stop_after=body.stop_after,
        fallback={
            "fetch": body.fetch,
            "parse": body.parse,
            "extract": body.extract,
            "wiki": body.create_wiki,
            "neo4j": body.sync_neo4j,
        },
    )
    if not any(flags.values()):
        raise HTTPException(status_code=400, detail="no ingest stages selected")
    options = _stage_options(
        flags=flags,
        parser=body.parser,
        llm_provider=body.llm_provider,
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
    try:
        _configured_wiki_engine(config, enable_neo4j_sync=False).append_to_log(
            f"[INGEST] {arxiv_norm} queued by {actor} (task {task_id})"
        )
    except Exception as exc:
        logger.warning("failed to append ingest audit log for task %s: %s", task_id, exc)
    background_tasks.add_task(execute_ingest, task_id, ingest_store, config)

    return IngestQueuedResponse(
        task_id=task_id,
        status="queued",
        message="ingest queued; poll GET /api/ingest/{task_id}",
    )


@router.post(
    "/ingest/paper/reviewed-extraction",
    response_model=IngestQueuedResponse,
    status_code=202,
)
async def ingest_reviewed_extraction(
    request: Request,
    body: ReviewedExtractionRequest,
    background_tasks: BackgroundTasks,
):
    """Queue wiki/graph ingest from client-side LLM extraction after human review."""
    config: ServerConfig = request.app.state.config
    ingest_store: IngestStore = request.app.state.ingest_store

    fetcher = ArxivFetcher()
    arxiv_norm = fetcher._normalize_arxiv_id(body.arxiv_id)
    if not fetcher._is_valid_arxiv_id(arxiv_norm):
        raise HTTPException(status_code=400, detail="invalid arxiv_id format")

    requester = request.headers.get(config.user_header) if config.user_header else None
    reviewer = body.reviewed_by or requester
    reviewed_extraction = _reviewed_extraction_step(body, reviewer=reviewer)

    task_id = str(uuid.uuid4())[:8]
    now = _utc_now_iso()
    actor = _requester_label(requester)

    options: Dict[str, Any] = {
        "source": "client_reviewed_extraction",
        "reviewed_extraction": reviewed_extraction,
        "metadata": body.metadata,
        "create_wiki": body.create_wiki,
        "sync_neo4j": body.sync_neo4j,
    }

    steps = {
        "fetch": StepStatus(),
        "parse": StepStatus(),
        "extract": StepStatus(),
        "wiki": StepStatus(),
        "neo4j": StepStatus(),
    }
    task = IngestTask(
        task_id=task_id,
        arxiv_id=arxiv_norm,
        status="queued",
        message="reviewed extraction queued",
        requester=requester,
        options=options,
        steps=steps,
        submitted_at=now,
        updated_at=now,
    )
    ingest_store.save(task)
    try:
        _configured_wiki_engine(config, enable_neo4j_sync=False).append_to_log(
            f"[INGEST] {arxiv_norm} reviewed extraction queued by {actor} (task {task_id})"
        )
    except Exception as exc:
        logger.warning(
            "failed to append reviewed extraction audit log for task %s: %s",
            task_id,
            exc,
        )
    background_tasks.add_task(execute_reviewed_extraction, task_id, ingest_store, config)

    return IngestQueuedResponse(
        task_id=task_id,
        status="queued",
        message="reviewed extraction queued; poll GET /api/ingest/{task_id}",
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
    reviewer = body.reviewed_by or requester
    new_task_id = str(uuid.uuid4())[:8]
    now = _utc_now_iso()

    if body.algorithm is not None or body.algorithm_ir is not None:
        reviewed_body = ReviewedExtractionRequest(
            arxiv_id=previous.arxiv_id,
            algorithm=body.algorithm,
            algorithm_ir=body.algorithm_ir,
            metadata=body.metadata,
            create_wiki=body.create_wiki,
            sync_neo4j=body.sync_neo4j,
            reviewed_by=body.reviewed_by,
            source=body.source,
            notes=body.notes,
        )
        reviewed_extraction = _reviewed_extraction_step(reviewed_body, reviewer=reviewer)
        options: Dict[str, Any] = {
            "source": "continued_client_reviewed_extraction",
            "source_task_id": task_id,
            "reviewed_extraction": reviewed_extraction,
            "metadata": body.metadata,
            "create_wiki": body.create_wiki,
            "sync_neo4j": body.sync_neo4j,
        }
        task = _new_ingest_task(
            task_id=new_task_id,
            arxiv_id=previous.arxiv_id,
            requester=requester,
            options=options,
            submitted_at=now,
            message="reviewed extraction continuation queued",
        )
        ingest_store.save(task)
        background_tasks.add_task(execute_reviewed_extraction, new_task_id, ingest_store, config)
        return IngestQueuedResponse(
            task_id=new_task_id,
            status="queued",
            message="reviewed extraction continuation queued; poll GET /api/ingest/{task_id}",
        )

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
        llm_provider=body.llm_provider,
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
    wiki = _configured_wiki_engine(request.app.state.config, enable_neo4j_sync=False)
    result = wiki.lint(fix=fix)

    return result
