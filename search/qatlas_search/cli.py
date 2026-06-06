"""``qatlas-search`` — academic-style literature search for QuantumAtlas.

Runs the selected backends concurrently, then merges + ranks the hits by exact
term/phrase match + citation count (lexical, no vector store, no LLM). This is
the infrastructure search tool; the LLM-orchestrated layer lives in the separate
``agentic-search`` repo, which consumes these backends.

Examples::

    qatlas-search "surface code threshold"
    qatlas-search '"quantum supremacy" sampling' --tools arxiv,openalex --json
    qatlas-search --list-tools
"""

from __future__ import annotations

import argparse
import json
import sys

from qatlas_search import __version__
from qatlas_search.backends import all_backends, select_backends
from qatlas_search.config import get_settings
from qatlas_search.engine import run_direct
from qatlas_search.models import Paper, SearchQuery


def _list_tools(settings) -> int:
    print("Available search tools (✓ = ready with current config):\n")
    for b in all_backends():
        ok = "✓" if b.available(settings) else "✗"
        key = " (needs API key)" if b.requires_key else ""
        print(f"  {ok} {b.name:<18} cost={b.cost_tier:<6}{key}")
    print(
        "\nDefault selection: "
        + ", ".join(settings.default_tool_list())
        + "\nConfigure keys/emails via QATLAS_SEARCH_* env vars (see README)."
    )
    return 0


def _paper_dict(p: Paper) -> dict:
    return {
        "title": p.title,
        "authors": p.authors,
        "year": p.year,
        "citations": p.citations,
        "doi": p.doi,
        "arxiv_id": p.arxiv_id,
        "url": p.url,
        "venue": p.venue,
        "source": p.source,
        "score": round(p.score, 4),
    }


def _print_human(papers: list[Paper], top: int) -> None:
    if not papers:
        print("No results.", file=sys.stderr)
        return
    for i, p in enumerate(papers[:top], 1):
        cites = "?" if p.citations is None else str(p.citations)
        year = p.year or "----"
        ident = p.doi or p.arxiv_id or ""
        print(f"{i:>2}. [{year}] {p.title}")
        print(
            f"    score={p.score:.3f}  citations={cites}  sources={p.source}"
            + (f"  {ident}" if ident else "")
        )
        if p.url:
            print(f"    {p.url}")


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas-search",
        description="Academic-style (lexical + citation) search for QuantumAtlas.",
    )
    p.add_argument("query", nargs="?", help='Search query (use "quotes" for exact phrases)')
    p.add_argument("--version", action="version", version=f"qatlas-search {__version__}")
    p.add_argument("--list-tools", action="store_true", help="List backends and exit")
    p.add_argument(
        "--tools",
        help="Comma-separated backend allow-list (default: from config). "
        "e.g. arxiv,openalex,semantic_scholar,crossref,internal",
    )
    p.add_argument("--max-results", type=int, default=None, help="Max results per backend")
    p.add_argument("--top", type=int, default=15, help="How many ranked results to show")
    p.add_argument("--json", action="store_true", help="Emit JSON instead of text")
    p.add_argument("-v", "--verbose", action="store_true", help="Show per-backend counts/errors")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv if argv is not None else sys.argv[1:])
    settings = get_settings()
    if args.max_results:
        settings.max_results_per_tool = args.max_results

    if args.list_tools:
        return _list_tools(settings)

    if not args.query:
        print("error: a query is required (or use --list-tools)", file=sys.stderr)
        return 2

    requested = (
        [t.strip() for t in args.tools.split(",") if t.strip()]
        if args.tools
        else settings.default_tool_list()
    )
    backends = select_backends(requested, settings, only_available=True)
    if not backends:
        print(
            "error: none of the requested tools are available with the current "
            "config. Run `qatlas-search --list-tools` to see what's ready.",
            file=sys.stderr,
        )
        return 1

    query = SearchQuery.parse(args.query, max_results=settings.max_results_per_tool)

    if args.verbose:
        print(
            f"# tools: {', '.join(b.name for b in backends)}",
            file=sys.stderr,
        )

    outcome = run_direct(query, backends, settings)
    if args.verbose:
        for name, count in outcome.per_backend_counts.items():
            print(f"#   {name}: {count} hits", file=sys.stderr)
        for name, err in outcome.errors.items():
            print(f"#   {name} ERROR: {err}", file=sys.stderr)

    if args.json:
        print(
            json.dumps(
                {
                    "query": query.text,
                    "results": [_paper_dict(p) for p in outcome.papers[: args.top]],
                },
                ensure_ascii=False,
                indent=2,
            )
        )
    else:
        _print_human(outcome.papers, args.top)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
