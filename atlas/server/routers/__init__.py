"""
Server Routers Module

Contains routers for different API sections.
"""

from .wiki import router as wiki_router
from .graph import router as graph_router
from .api import router as api_router

__all__ = [
    "wiki_router",
    "graph_router",
    "api_router",
]