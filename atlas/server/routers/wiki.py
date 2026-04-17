"""
Wiki Router

Handles Wiki page visualization and management.
"""

from fastapi import APIRouter, Request, HTTPException, Form
from fastapi.responses import HTMLResponse, RedirectResponse
from typing import Optional, List

router = APIRouter()


@router.get("/", response_class=HTMLResponse)
async def wiki_home(request: Request):
    """Wiki home page - list all pages."""
    from atlas.wiki.engine import WikiEngine

    config = request.app.state.config
    wiki_engine = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=False,
    )

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
    from atlas.wiki.engine import WikiEngine

    config = request.app.state.config
    wiki_engine = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=False,
    )

    page = wiki_engine.get_page(page_id)
    if page is None:
        raise HTTPException(status_code=404, detail=f"Page not found: {page_id}")

    # Get backlinks
    backlinks = wiki_engine.querier.get_backlinks(page_id)

    # Get linked pages
    linked_pages = wiki_engine.querier.get_linked_pages(page_id)

    templates = request.app.state.templates
    return templates.TemplateResponse(
        request,
        "wiki/page.html",
        {
            "page": page,
            "backlinks": backlinks,
            "linked_pages": linked_pages,
        },
    )


@router.get("/search", response_class=HTMLResponse)
async def wiki_search(request: Request, q: str = ""):
    """Search wiki pages."""
    from atlas.wiki.engine import WikiEngine

    config = request.app.state.config
    wiki_engine = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=False,
    )

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


@router.get("/edit/{page_id}", response_class=HTMLResponse)
async def wiki_edit_form(request: Request, page_id: str):
    """Show edit form for a wiki page."""
    from atlas.wiki.engine import WikiEngine

    config = request.app.state.config
    wiki_engine = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=False,
    )

    page = wiki_engine.get_page(page_id)
    if page is None:
        raise HTTPException(status_code=404, detail=f"Page not found: {page_id}")

    templates = request.app.state.templates
    return templates.TemplateResponse(
        request,
        "wiki/edit.html",
        {
            "page": page,
        },
    )


@router.post("/edit/{page_id}")
async def wiki_edit_save(
    request: Request,
    page_id: str,
    content: str = Form(...),
    title: str = Form(...),
    tags: str = Form(default=""),
):
    """Save edited wiki page."""
    from atlas.wiki.engine import WikiEngine

    config = request.app.state.config
    wiki_engine = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=False,
    )

    # Parse tags
    tag_list = [t.strip() for t in tags.split(",") if t.strip()]

    # Update page
    wiki_engine.update_page(
        page_id,
        content=content,
        title=title,
        tags=tag_list,
    )

    return RedirectResponse(url=f"/wiki/page/{page_id}", status_code=303)


@router.get("/new", response_class=HTMLResponse)
async def wiki_new_form(request: Request):
    """Show form to create new wiki page."""
    templates = request.app.state.templates
    return templates.TemplateResponse(
        request,
        "wiki/new.html",
        {},
    )


@router.post("/new")
async def wiki_new_create(
    request: Request,
    page_id: str = Form(...),
    title: str = Form(...),
    page_type: str = Form(...),
    category: str = Form(default=""),
    content: str = Form(default=""),
    tags: str = Form(default=""),
):
    """Create new wiki page."""
    from atlas.wiki.engine import WikiEngine
    from atlas.wiki.page import WikiPage, WikiFrontmatter

    config = request.app.state.config
    wiki_engine = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=False,
    )

    # Check if page exists
    if wiki_engine.get_page(page_id):
        raise HTTPException(status_code=400, detail=f"Page already exists: {page_id}")

    # Parse tags
    tag_list = [t.strip() for t in tags.split(",") if t.strip()]

    # Create page
    page = WikiPage(
        frontmatter=WikiFrontmatter(
            id=page_id,
            title=title,
            type=page_type,
            category=category or None,
            tags=tag_list,
            status="draft",
        ),
        content=content,
    )

    wiki_engine.save_page(page)

    return RedirectResponse(url=f"/wiki/page/{page_id}", status_code=303)