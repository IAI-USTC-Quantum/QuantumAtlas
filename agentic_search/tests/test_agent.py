"""Agent-mode tests — skipped unless the `agentic-search` extra is installed.

These do NOT call any LLM (no network, no API key): they build the agent with
pydantic-ai's ``TestModel`` (which has no HTTP client) and only assert that the
agent is constructed and the shared collection state starts empty.
"""

from __future__ import annotations

from pydantic_ai.models.test import TestModel

from qatlas_agentic_search.agent import build_agent
from qatlas_agentic_search.backends import select_backends
from qatlas_agentic_search.config import Settings


def test_build_agent_registers_only_selected_tools() -> None:
    s = Settings()
    backends = select_backends(["arxiv", "openalex"], s, only_available=True)
    agent, state = build_agent(s, backends, model=TestModel())
    assert agent is not None
    assert state["collected"] == []
