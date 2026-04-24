"""
Server Routers Module

Contains routers for different API sections.
"""

from .api import router as api_router
from .downloads import router as paper_resources_router
from .graph import router as graph_router
from .shares import api_router as shares_api_router, public_router as shares_public_router
from .wiki import router as wiki_router

__all__ = [
    "wiki_router",
    "graph_router",
    "api_router",
    "paper_resources_router",
    "shares_api_router",
    "shares_public_router",
]
