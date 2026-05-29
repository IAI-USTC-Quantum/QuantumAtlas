"""Try multiple resolvers in order, return the first acceptable match.

`ChainResolver` returns:
  * the first `high`-confidence hit, OR
  * the first `medium`-confidence hit (only if no later resolver finds high), OR
  * None when nobody had an opinion.

Resolver errors are swallowed (logged) so one upstream outage doesn't
kill the whole chain — the chain reports None when nothing landed.
"""

from __future__ import annotations

import logging
from typing import Iterable, List, Optional

from .protocol import DOIMatch, DOIResolver, PaperContext

logger = logging.getLogger(__name__)


class ChainResolver:
    name = "chain"

    def __init__(self, resolvers: Iterable[DOIResolver]):
        self._resolvers: List[DOIResolver] = list(resolvers)
        if not self._resolvers:
            raise ValueError("ChainResolver needs at least one inner resolver")

    @property
    def resolvers(self) -> List[DOIResolver]:
        return list(self._resolvers)

    def resolve(self, paper: PaperContext) -> Optional[DOIMatch]:
        best: Optional[DOIMatch] = None
        for r in self._resolvers:
            try:
                match = r.resolve(paper)
            except Exception as exc:  # pragma: no cover - defensive
                logger.warning("%s raised on %s: %s", r.name, paper.arxiv_id, exc)
                continue
            if match is None:
                continue
            if match.confidence == "high":
                return match
            if best is None:
                best = match
        return best
