"""
FastAPI Application Entry Point

Main application for QuantumAtlas web interface.
"""

import os
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, Request
from fastapi.responses import HTMLResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates

from atlas import __version__

from .config import ServerConfig, get_config


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Application lifespan handler."""
    # Startup
    config: ServerConfig = app.state.config
    app.state.wiki_engine = None
    app.state.neo4j_client = None

    from atlas.server.config import get_project_root
    from atlas.server.tasks import IngestStore, ShareStore
    from atlas.runtime_metadata import validate_release_tag, write_code_version_manifests

    root = get_project_root()
    data_root = config.get_data_root()
    raw_root = config.get_raw_root()
    if config.require_release_tag:
        validate_release_tag(root)
    write_code_version_manifests(root, [raw_root, data_root])
    app.state.share_store = ShareStore(data_root / "shares")
    app.state.ingest_store = IngestStore(data_root / "ingests")

    print(f"🚀 QuantumAtlas Server starting...")
    print(f"   Wiki directory: {config.wiki_dir}")
    print(f"   Neo4j URI: {config.neo4j_uri}")

    yield

    # Shutdown
    if app.state.neo4j_client:
        app.state.neo4j_client.close()
    print("👋 QuantumAtlas Server stopped")


def create_app(config: Optional[ServerConfig] = None) -> FastAPI:
    """Create FastAPI application with configuration."""

    if config is None:
        config = ServerConfig.from_env()

    app = FastAPI(
        title="QuantumAtlas",
        description="AI-driven Quantum Algorithm Implementation Framework",
        version=__version__,
        lifespan=lifespan,
        docs_url="/api/docs",
        redoc_url="/api/redoc",
    )

    # Store config
    app.state.config = config

    # Setup templates
    template_dir = Path(__file__).parent / "templates"
    templates = Jinja2Templates(directory=str(template_dir))
    templates.env.globals["app_version"] = __version__
    app.state.templates = templates

    # Mount static files
    static_dir = Path(__file__).parent / "static"
    if static_dir.exists():
        app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")

    # Register routers
    from .routers import api, downloads, graph, shares, wiki

    app.include_router(wiki.router, prefix="/wiki", tags=["wiki"])
    app.include_router(graph.router, prefix="/graph", tags=["graph"])
    app.include_router(api.router, prefix="/api", tags=["api"])
    app.include_router(downloads.router, prefix="/api", tags=["paper-resources"])
    app.include_router(shares.api_router, prefix="/api/shares", tags=["shares"])
    app.include_router(shares.public_router, tags=["shares-public"])

    # Home page route
    @app.get("/", response_class=HTMLResponse)
    async def home(request: Request):
        """Render home page."""
        # Get wiki stats
        try:
            from atlas.wiki.engine import WikiEngine

            wiki_engine = WikiEngine(
                wiki_dir=config.wiki_dir,
                raw_dir=config.raw_dir,
                enable_neo4j_sync=False,
            )
            stats = wiki_engine.get_stats()
            recent_pages = wiki_engine.querier.get_recent_pages(5)
        except Exception:
            stats = {"total_pages": 0, "by_type": {}, "by_status": {}}
            recent_pages = []

        return templates.TemplateResponse(
            request,
            "index.html",
            {
                "stats": stats,
                "recent_pages": recent_pages,
                "config": config,
            },
        )

    # Health check
    @app.get("/health")
    async def health():
        """Health check endpoint."""
        return {"status": "healthy", "version": __version__}

    return app


# Create default app instance
app = create_app()


def main():
    """Run the server."""
    import uvicorn

    config = get_config()
    uvicorn.run(
        "atlas.server.main:app",
        host=config.host,
        port=config.port,
        reload=config.debug,
    )


if __name__ == "__main__":
    main()
