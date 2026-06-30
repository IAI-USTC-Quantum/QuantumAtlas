"""Minimal gitea REST client for the Claim contrib flow.

Two operations only:

* **idempotency check** — find an OPEN issue already carrying the same
  ``claim_id`` (DECIDED in ADR 0005's round-2 note: refuse + surface the existing
  issue URL; do NOT auto-update — the daemon may be mid-proof — and do NOT
  duplicate — it pollutes the daemon's dedup). Mirrors the daemon's dedup: scan
  issue bodies for a line-prefix ``claim_id:`` (also matches a legacy
  ``unit_id:`` line so issues filed before the rename are still caught).
* **create issue** — file one ``type/theorem`` + ``status/ready`` issue.

The write path stays gitea-direct under the user's own token (ADR 0005); the QA
server is never involved in filing.
"""

from __future__ import annotations

import requests
from pydantic import BaseModel


class GiteaConfig(BaseModel):
    base_url: str  # e.g. https://git.example.com (no trailing slash)
    repo: str  # owner/name, e.g. agony/qatlas-lean
    token: str
    verify: bool = True


class GiteaError(Exception):
    pass


class GiteaClient:
    """Thin wrapper over gitea's ``/api/v1/repos/{owner}/{repo}`` surface.

    ``session`` is injectable so tests can substitute a fake transport.
    """

    def __init__(self, config: GiteaConfig, session: requests.Session | None = None):
        self._cfg = config
        self._session = session or requests.Session()

    # -- low level ---------------------------------------------------------

    def _repo_url(self, path: str) -> str:
        base = self._cfg.base_url.rstrip("/")
        return f"{base}/api/v1/repos/{self._cfg.repo}/{path.lstrip('/')}"

    def _headers(self) -> dict[str, str]:
        return {
            "Authorization": f"token {self._cfg.token}",
            "Accept": "application/json",
            "Content-Type": "application/json",
        }

    # -- idempotency -------------------------------------------------------

    def find_open_issue_by_claim_id(self, claim_id: str) -> dict | None:
        """Return the first OPEN issue whose body carries ``claim_id: <claim_id>``
        (line-prefix match; a legacy ``unit_id: <claim_id>`` line also matches so
        issues filed before the rename are still deduped), or None.

        Pages through open issues; gitea caps page size at 50.
        """
        claim_id = claim_id.strip()
        page = 1
        while True:
            resp = self._session.get(
                self._repo_url("issues"),
                headers=self._headers(),
                params={"state": "open", "type": "issues", "limit": 50, "page": page},
                verify=self._cfg.verify,
                timeout=30,
            )
            if resp.status_code != 200:
                raise GiteaError(f"list issues HTTP {resp.status_code}: {resp.text[:200]}")
            items = resp.json()
            if not items:
                return None
            for it in items:
                if _body_has_claim_id(it.get("body", "") or "", claim_id):
                    return it
            if len(items) < 50:
                return None
            page += 1

    # -- labels ------------------------------------------------------------

    def resolve_label_ids(self, names: list[str]) -> list[int]:
        """Resolve label NAMES to gitea label IDs (create-issue takes ids, not
        names). Unknown labels are silently skipped — the daemon can add them at
        pickup; a missing label must never block filing an otherwise-good Claim.
        """
        resp = self._session.get(
            self._repo_url("labels"),
            headers=self._headers(),
            params={"limit": 50},
            verify=self._cfg.verify,
            timeout=30,
        )
        if resp.status_code != 200:
            return []
        by_name = {lbl.get("name"): lbl.get("id") for lbl in resp.json()}
        return [by_name[n] for n in names if n in by_name and by_name[n] is not None]

    # -- create ------------------------------------------------------------

    def create_issue(self, title: str, body: str, labels: list[str]) -> dict:
        """File one issue. ``labels`` are names; resolved to ids best-effort.
        Returns the created issue dict (``number``, ``html_url``, …).
        """
        label_ids = self.resolve_label_ids(labels) if labels else []
        payload: dict = {"title": title, "body": body}
        if label_ids:
            payload["labels"] = label_ids
        resp = self._session.post(
            self._repo_url("issues"),
            headers=self._headers(),
            json=payload,
            verify=self._cfg.verify,
            timeout=30,
        )
        if resp.status_code not in (200, 201):
            raise GiteaError(f"create issue HTTP {resp.status_code}: {resp.text[:300]}")
        return resp.json()


def _body_has_claim_id(body: str, claim_id: str) -> bool:
    # QA writes ``claim_id:``; ``unit_id:`` is matched too so an issue filed
    # before the rename is still recognised by the idempotency check.
    for ln in body.splitlines():
        for prefix in ("claim_id:", "unit_id:"):
            if ln.startswith(prefix) and ln.split(":", 1)[1].strip() == claim_id:
                return True
    return False
