"""pytest config for search/tests — offline, no optional deps.

Live-network backend calls are marked ``network`` and excluded by CI's
``-m "not network and not e2e"``.
"""

from __future__ import annotations

collect_ignore: list[str] = []
