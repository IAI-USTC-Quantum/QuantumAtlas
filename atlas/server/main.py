"""FastAPI application entry point."""

from contextlib import asynccontextmanager
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, Request
from fastapi.responses import HTMLResponse
from fastapi.staticfiles import StaticFiles

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

    print("🚀 QuantumAtlas Server starting...")
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

    # Mount static files
    static_dir = Path(__file__).parent / "static"
    if static_dir.exists():
        app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")

    # Register routers
    from .routers import api, downloads, shares

    app.include_router(api.router, prefix="/api", tags=["api"])
    app.include_router(downloads.router, prefix="/api", tags=["paper-resources"])
    app.include_router(shares.api_router, prefix="/api/shares", tags=["shares"])
    app.include_router(shares.public_router, tags=["shares-public"])

    def web_index(request: Request) -> HTMLResponse:
        """Serve the Vite web shell."""
        index_path = static_dir / "web" / "index.html"
        if not index_path.is_file():
            return HTMLResponse(
                "<!doctype html><title>QuantumAtlas</title><div id=\"root\">QuantumAtlas</div>"
            )
        content = index_path.read_text(encoding="utf-8")
        return HTMLResponse(content)

    @app.get("/", response_class=HTMLResponse)
    async def web_home(request: Request):
        return web_index(request)

    @app.get("/token", response_class=HTMLResponse)
    async def web_token(request: Request):
        return web_index(request)

    @app.get("/wiki", response_class=HTMLResponse)
    @app.get("/wiki/", response_class=HTMLResponse)
    async def web_wiki(request: Request):
        return web_index(request)

    @app.get("/wiki/search", response_class=HTMLResponse)
    async def web_wiki_search(request: Request):
        return web_index(request)

    @app.get("/wiki/page/{page_id:path}", response_class=HTMLResponse)
    async def web_wiki_page(request: Request, page_id: str):
        return web_index(request)

    @app.get("/graph", response_class=HTMLResponse)
    @app.get("/graph/", response_class=HTMLResponse)
    async def web_graph(request: Request):
        return web_index(request)

    @app.get("/graph/node/{node_path:path}", response_class=HTMLResponse)
    async def web_graph_node(request: Request, node_path: str):
        return web_index(request)

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
