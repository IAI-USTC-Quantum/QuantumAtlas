"""
API Router

REST API endpoints for programmatic access.
"""

from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from typing import Optional, List, Dict, Any

router = APIRouter()


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
    extract: bool = True
    sync_neo4j: bool = True


class IngestResponse(BaseModel):
    task_id: str
    status: str
    message: str


class GraphQueryRequest(BaseModel):
    query: str
    limit: int = 50


# === Wiki API ===

@router.get("/pages", response_model=PageListResponse)
async def list_pages(
    page_type: Optional[str] = None,
    tags: Optional[str] = None,
    status: Optional[str] = None,
):
    """List all wiki pages."""
    from atlas.wiki.engine import WikiEngine

    wiki = WikiEngine(enable_neo4j_sync=False)

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
async def get_page(page_id: str):
    """Get a wiki page by ID."""
    from atlas.wiki.engine import WikiEngine

    wiki = WikiEngine(enable_neo4j_sync=False)
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
async def get_stats():
    """Get wiki statistics."""
    from atlas.wiki.engine import WikiEngine

    wiki = WikiEngine(enable_neo4j_sync=False)
    return wiki.get_stats()


@router.get("/search")
async def search_pages(q: str, limit: int = 10):
    """Search wiki pages."""
    from atlas.wiki.engine import WikiEngine

    wiki = WikiEngine(enable_neo4j_sync=False)
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

@router.post("/ingest/paper", response_model=IngestResponse)
async def ingest_paper(request: IngestRequest):
    """Ingest a paper into the wiki."""
    import uuid
    from atlas.wiki.engine import WikiEngine
    from atlas.server.config import get_config

    config = get_config()
    wiki = WikiEngine(
        wiki_dir=config.wiki_dir,
        raw_dir=config.raw_dir,
        enable_neo4j_sync=request.sync_neo4j,
    )

    task_id = str(uuid.uuid4())[:8]

    # TODO: Run ingestion as background task
    try:
        result = wiki.ingest_paper(
            request.arxiv_id,
            extract=request.extract,
            sync_neo4j=request.sync_neo4j,
        )

        return IngestResponse(
            task_id=task_id,
            status=result["status"],
            message=f"Ingested {len(result['wiki_pages'])} pages",
        )
    except Exception as e:
        return IngestResponse(
            task_id=task_id,
            status="error",
            message=str(e),
        )


# === Lint API ===

@router.get("/lint")
async def run_lint(fix: bool = False):
    """Run wiki lint checks."""
    from atlas.wiki.engine import WikiEngine

    wiki = WikiEngine(enable_neo4j_sync=False)
    result = wiki.lint(fix=fix)

    return result