"""Agent mode — a Pydantic-AI agent that orchestrates the enabled backends.

Optional: requires the ``agentic-search`` extra (``pydantic-ai-slim[openai]``).
``pydantic_ai`` is imported lazily inside ``run_agent`` so that ``import
qatlas_agentic_search`` and the whole *direct* mode work in a plain install with
no extra and no LLM key.

Design choices from the review:
* The agent is handed **only** the user-allowed, available backends as tools —
  the LLM picks among them and decides how many to call (cost/latency tradeoff),
  but it cannot reach a tool the user excluded.
* Each tool returns already-ranked rows from ``run_direct`` for that single
  backend, plus the backend's cost tier, so the model can reason about budget.
* The final ranking is still done deterministically by ``ranking.rank`` over the
  union the agent gathered — the LLM curates/explains, it does not invent scores.
"""

from __future__ import annotations

from qatlas_agentic_search.backends.base import Backend
from qatlas_agentic_search.config import Settings
from qatlas_agentic_search.engine import run_direct
from qatlas_agentic_search.models import Paper, SearchQuery
from qatlas_agentic_search.ranking import rank

_SYSTEM = """You are an academic literature search orchestrator for quantum
computing. You value EXACT term/phrase matching and CITATION COUNT over fuzzy
semantic similarity — this is scholarly search, not vector recall.

You are given one or more search tools, each wrapping a different source
(arXiv, OpenAlex, Semantic Scholar, Crossref, the QuantumAtlas internal graph).
Decide which tools to call and with what query to best answer the user, keeping
the number of calls reasonable (respect cost/latency tiers: fast < medium <
slow). Prefer corroboration across sources. When you have enough evidence,
return a concise ranked summary: for each top paper give title, year, citation
count (if known), the sources that found it, and a one-line relevance note.
Do not fabricate citation counts or papers."""


def _shared_state() -> dict:
    # Accumulates every Paper the agent's tool calls surfaced, for a final
    # deterministic re-rank after the run.
    return {"collected": []}


def build_agent(settings: Settings, backends: list[Backend], model=None):
    """Construct a Pydantic-AI Agent exposing one tool per backend.

    ``model`` defaults to ``settings.llm_model`` (an OpenAI-compatible model
    string); tests pass a ``pydantic_ai.models.test.TestModel`` so no provider
    HTTP client is created.
    """
    from pydantic_ai import Agent  # lazy: needs the extra

    state = _shared_state()
    agent = Agent(model or settings.llm_model, system_prompt=_SYSTEM)

    def _make_tool(backend: Backend):
        def tool(query: str, max_results: int = 8) -> list[dict]:
            sq = SearchQuery.parse(query, max_results=max_results)
            outcome = run_direct(sq, [backend], settings)
            state["collected"].extend(outcome.papers)
            return [
                {
                    "title": p.title,
                    "year": p.year,
                    "citations": p.citations,
                    "authors": p.authors[:5],
                    "doi": p.doi,
                    "arxiv_id": p.arxiv_id,
                    "url": p.url,
                    "source": p.source,
                }
                for p in outcome.papers
            ]

        tool.__name__ = f"search_{backend.name}"
        tool.__doc__ = (
            f"Search the '{backend.name}' source (cost tier: {backend.cost_tier}). "
            f"Returns ranked papers with citation counts where available."
        )
        return tool

    for b in backends:
        # tool_plain: these tools take no RunContext, just plain args.
        agent.tool_plain(_make_tool(b))

    return agent, state


def run_agent(
    query: SearchQuery, backends: list[Backend], settings: Settings
) -> tuple[str, list[Paper]]:
    """Run the agent. Returns (LLM narrative, deterministically ranked papers)."""
    agent, state = build_agent(settings, backends)
    result = agent.run_sync(query.text)
    collected: list[Paper] = state["collected"]
    ranked = rank(query, collected, settings)
    return str(result.output), ranked
