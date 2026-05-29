"""Read the DOI arXiv themselves recorded in the paper's metadata.

This is the cheapest possible resolver: it just reads the cached arXiv API
response JSON sidecar. Confidence is always `high` because the value came
from the same arXiv record that defined the arxiv_id we're enriching —
there's no matching step to be uncertain about.

When the JSON sidecar is missing (older asset layouts) or doesn't contain
a DOI, returns None so the chain can fall through to Crossref / OpenAlex.
"""

from __future__ import annotations

import json
import logging
from pathlib import Path
from typing import Callable, Optional

from .protocol import DOIMatch, PaperContext, normalize_doi

logger = logging.getLogger(__name__)


class ArxivSelfReportedResolver:
    """Resolver that reads the cached arXiv API response.

    `json_path_getter` takes an arxiv_id and returns the path to the cached
    metadata JSON (typically `wiki_engine.get_paper_asset_path("json", id)`).
    Decoupled so this resolver doesn't pull in WikiEngine.
    """

    name = "arxiv-self"

    def __init__(self, json_path_getter: Callable[[str], Path]):
        self._json_path_getter = json_path_getter

    def resolve(self, paper: PaperContext) -> Optional[DOIMatch]:
        try:
            path = self._json_path_getter(paper.arxiv_id)
        except Exception as exc:  # pragma: no cover - defensive
            logger.debug("arxiv-self: path lookup failed for %s: %s", paper.arxiv_id, exc)
            return None
        if not path or not path.exists():
            return None
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            logger.debug("arxiv-self: cannot read %s: %s", path, exc)
            return None
        doi = normalize_doi(str(data.get("doi") or ""))
        if not doi:
            return None
        return DOIMatch(
            doi=doi,
            source="arxiv",
            confidence="high",
            raw_record={"arxiv_id": paper.arxiv_id, "json_path": str(path)},
        )
