"""``qatlas contrib claim`` — localhost Claim-drafting contributor workflow.

A human-in-the-loop, localhost WebUI + agent that drafts one or more **Claims**
against a paper (paper-of-record + near-verbatim NL statement + stated/implicit
assumptions + reference IDs) and, on **Confirm**, files **one** ``type/theorem``
gitea issue per click on the upstream Lean-content repo (``agony/qatlas-lean``)
under the user's own gitea token. See ADR 0005.

The QuantumAtlas server is involved only for **reads** (paper bytes via the
suspend-and-wait endpoints, reference resolution via ``/api/papers/lookup``);
the **write** (filing the gitea issue) stays gitea-direct (ADR 0007).
"""

from __future__ import annotations
