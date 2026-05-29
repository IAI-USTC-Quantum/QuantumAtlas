"""Paper resource listing API."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Dict, List, Optional

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel

from qatlas.paper_assets import resolve_paper_assets, share_path_for_asset
from qatlas.parser.arxiv_fetcher import ArxivFetcher
from qatlas.server.config import ServerConfig
from qatlas.server.routers.shares import build_share_url, create_share_record

logger = logging.getLogger(__name__)

router = APIRouter()


def _resolve_raw_dir(config: ServerConfig) -> Path:
    return config.get_raw_root()


class PaperAsset(BaseModel):
    """One paper file asset."""

    exists: bool
    url: Optional[str] = None
    size: Optional[int] = None


class PaperImageAsset(BaseModel):
    """One local paper image asset."""

    name: str
    url: str
    size: int


class PaperResourcesResponse(BaseModel):
    """Local raw files for a paper id."""

    arxiv_id: str
    assets: Dict[str, PaperAsset]
    images: List[PaperImageAsset] = []


@router.get("/papers/{arxiv_id:path}/resources", response_model=PaperResourcesResponse)
async def get_paper_resources(request: Request, arxiv_id: str):
    """Report which local files exist for an arXiv id and return share URLs."""
    config: ServerConfig = request.app.state.config
    share_store = request.app.state.share_store
    fetcher = ArxivFetcher()
    norm = arxiv_id.strip()
    if not fetcher._is_valid_arxiv_id(fetcher._normalize_arxiv_id(norm)):
        raise HTTPException(status_code=400, detail="invalid arxiv_id format")

    resolved = resolve_paper_assets(config.get_raw_root(), norm)
    key = resolved["key"]

    share_paths = []
    for kind in ("pdf", "markdown", "json"):
        path = resolved[f"{kind}_path"]
        if path is not None and Path(path).is_file():
            share_paths.append(
                share_path_for_asset(
                    kind,
                    key,
                    asset_path=Path(path),
                    paper_assets_root=config.get_raw_root(),
                )
            )
    images_dir = resolved["images_dir"]
    if images_dir is not None and Path(images_dir).is_dir():
        share_paths.append(
            share_path_for_asset(
                "images",
                key,
                asset_path=Path(images_dir),
                paper_assets_root=config.get_raw_root(),
            )
        )

    share = None
    share_token = config.share_access_token
    share_base_url = config.get_public_base_url() if share_token else None
    if share_paths and not share_token:
        share = create_share_record(
            share_store=share_store,
            config=config,
            paths=share_paths,
            label=f"paper assets: {resolved['arxiv_id']}",
            expires_in=None,
            created_by=None,
        )
        share_token = share["token"]

    def asset(kind: str) -> PaperAsset:
        path = resolved[f"{kind}_path"]
        exists = path is not None and path.is_file()
        url: Optional[str] = None
        size: Optional[int] = None
        if exists and share_token is not None and path is not None:
            rel = share_path_for_asset(
                kind,
                key,
                asset_path=path,
                paper_assets_root=config.get_raw_root(),
            )
            url = build_share_url(share_token, rel, base_url=share_base_url)
            size = path.stat().st_size
        return PaperAsset(exists=exists, url=url, size=size)

    image_assets: List[PaperImageAsset] = []
    if images_dir is not None and share_token is not None:
        for image_path in sorted(Path(images_dir).iterdir(), key=lambda p: p.name.lower()):
            if not image_path.is_file():
                continue
            image_assets.append(
                PaperImageAsset(
                    name=image_path.name,
                    url=build_share_url(
                        share_token,
                        share_path_for_asset(
                            "images",
                            key,
                            asset_path=image_path,
                            paper_assets_root=config.get_raw_root(),
                        ),
                        base_url=share_base_url,
                    ),
                    size=image_path.stat().st_size,
                )
            )

    assets = {
        "pdf": asset("pdf"),
        "markdown": asset("markdown"),
        "json": asset("json"),
    }
    return PaperResourcesResponse(
        arxiv_id=str(resolved["arxiv_id"]),
        assets=assets,
        images=image_assets,
    )
