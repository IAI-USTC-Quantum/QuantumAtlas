"""
QuantumAtlas Web Server Module

Provides FastAPI-based web interface for:
- Wiki visualization
- Neo4j graph visualization
- REST API endpoints
- Paper ingestion workflow

Usage:
    # Start server
    python -m atlas.server

    # Or with uvicorn
    uvicorn atlas.server.main:app --host 0.0.0.0 --port 8000
"""

from .main import app, create_app
from .config import ServerConfig

__all__ = [
    "app",
    "create_app",
    "ServerConfig",
]