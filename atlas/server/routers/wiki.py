"""Wiki Router."""

import re
from fastapi import APIRouter, Request, HTTPException
from fastapi.responses import HTMLResponse
from typing import Optional

from atlas.paper_assets import resolve_paper_assets, share_path_for_asset
from atlas.server.routers.shares import build_share_url, create_share_record

router = APIRouter()

IMAGE_OMITTED_RE = re.compile(r"_Image omitted during migration: images/([^_]+)_")


def _configured_wiki_engine(request: Request, *, enable_neo4j_sync: bool = False):
    from atlas.wiki.engine import WikiEngine

    config = request.app.state.config
    return WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=enable_neo4j_sync,
    )


def _arxiv_id_from_page(page) -> Optional[str]:
    for link in getattr(page.frontmatter, "external_links", []) or []:
        if link.kind == "paper" and "/abs/" in link.url:
            return link.url.rstrip("/").split("/abs/", 1)[1]
    return None


def _inject_source_asset_links(page, request: Request) -> tuple[str, list[dict], Optional[dict]]:
    """Rewrite source-page image placeholders to short-lived share links."""
    if page.frontmatter.type != "source":
        return page.content, [], None

    arxiv_id = _arxiv_id_from_page(page)
    if not arxiv_id:
        return page.content, [], None

    config = request.app.state.config
    share_store = request.app.state.share_store
    assets = resolve_paper_assets(config.get_raw_root(), arxiv_id)
    key = assets["key"]

    share_paths = []
    asset_links = []
    for kind in ("pdf", "markdown", "json"):
        path = assets[f"{kind}_path"]
        if path is None or not path.is_file():
            continue
        rel = share_path_for_asset(kind, key)
        share_paths.append(rel)
        asset_links.append({"label": kind.upper(), "url": rel, "size": path.stat().st_size})

    images_dir = assets["images_dir"]
    if images_dir is not None and images_dir.is_dir():
        share_paths.append(share_path_for_asset("images", key))

    if not share_paths:
        return page.content, [], None

    if config.share_access_token:
        share = {
            "token": config.share_access_token,
            "url_prefix": build_share_url(config.share_access_token),
            "paths": share_paths,
            "expires_at": None,
            "label": "configured permanent paper asset share",
        }
    else:
        share = create_share_record(
            share_store=share_store,
            config=config,
            paths=share_paths,
            label=f"page assets: {page.frontmatter.id}",
            expires_in=None,
            created_by=None,
        )

    rendered_content = page.content
    if images_dir is not None and images_dir.is_dir():
        def repl(match: re.Match[str]) -> str:
            filename = match.group(1)
            image_path = images_dir / filename
            if not image_path.is_file():
                return match.group(0)
            rel = share_path_for_asset("images", key, filename)
            url = build_share_url(share["token"], rel)
            return f"![{filename}]({url})"

        rendered_content = IMAGE_OMITTED_RE.sub(repl, rendered_content)

    for item in asset_links:
        item["url"] = build_share_url(share["token"], item["url"])

    return rendered_content, asset_links, share


@router.get("/", response_class=HTMLResponse)
async def wiki_home(request: Request):
    """Wiki home page - list all pages."""
    wiki_engine = _configured_wiki_engine(request, enable_neo4j_sync=False)

    # Get all pages grouped by type
    all_pages = wiki_engine.list_pages()
    pages_by_type = {}
    for page in all_pages:
        page_type = page.frontmatter.type
        if page_type not in pages_by_type:
            pages_by_type[page_type] = []
        pages_by_type[page_type].append({
            "id": page.frontmatter.id,
            "title": page.frontmatter.title,
            "category": page.frontmatter.category,
            "status": page.frontmatter.status,
            "tags": page.frontmatter.tags,
        })

    stats = wiki_engine.get_stats()

    templates = request.app.state.templates
    return templates.TemplateResponse(
        request,
        "wiki/list.html",
        {
            "pages_by_type": pages_by_type,
            "stats": stats,
        },
    )


@router.get("/page/{page_id}", response_class=HTMLResponse)
async def wiki_page(request: Request, page_id: str):
    """Display a wiki page."""
    wiki_engine = _configured_wiki_engine(request, enable_neo4j_sync=False)

    page = wiki_engine.get_page(page_id)
    if page is None:
        raise HTTPException(status_code=404, detail=f"Page not found: {page_id}")

    # Get backlinks
    backlinks = wiki_engine.querier.get_backlinks(page_id)

    # Get linked pages
    linked_pages = wiki_engine.querier.get_linked_pages(page_id)

    rendered_content, asset_links, share = _inject_source_asset_links(page, request)
    render_page = page.model_copy(deep=True)
    render_page.content = rendered_content

    templates = request.app.state.templates
    return templates.TemplateResponse(
        request,
        "wiki/page.html",
        {
            "page": render_page,
            "backlinks": backlinks,
            "linked_pages": linked_pages,
            "asset_links": asset_links,
            "share": share,
        },
    )


@router.get("/search", response_class=HTMLResponse)
async def wiki_search(request: Request, q: str = ""):
    """Search wiki pages."""
    wiki_engine = _configured_wiki_engine(request, enable_neo4j_sync=False)

    results = []
    if q:
        results = wiki_engine.query(q, max_results=20)

    templates = request.app.state.templates
    return templates.TemplateResponse(
        request,
        "wiki/search.html",
        {
            "query": q,
            "results": results,
        },
    )
