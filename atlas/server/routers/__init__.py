"""Server router exports."""

from .api import router as api_router
from .downloads import router as paper_resources_router
from .shares import api_router as shares_api_router, public_router as shares_public_router

__all__ = [
    "api_router",
    "paper_resources_router",
    "shares_api_router",
    "shares_public_router",
]
