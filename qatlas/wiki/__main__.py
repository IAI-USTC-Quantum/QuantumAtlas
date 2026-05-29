"""
Wiki Browser CLI

Interactive command-line interface for browsing and managing wiki pages.

Commands:
    list        List wiki pages
    show        Show a wiki page
    search      Search wiki pages
    links       Show page links
    lint        Run lint checks
    stats       Show wiki statistics

Usage:
    python -m qatlas.wiki list --type concept
    python -m qatlas.wiki show prim-qft
    python -m qatlas.wiki search "quantum fourier"
    python -m qatlas.wiki links prim-qft --backlinks
    python -m qatlas.wiki lint --fix
"""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from qatlas.wiki.engine import WikiEngine


def cmd_list(args):
    """List wiki pages."""
    engine = WikiEngine()

    tags = args.tags.split(",") if args.tags else None
    pages = engine.list_pages(page_type=args.type, tags=tags, status=args.status)

    if not pages:
        print("No pages found.")
        return

    print(f"\nFound {len(pages)} pages:\n")
    print(f"  {'ID':<30} {'Type':<12} {'Status':<10} {'Title'}")
    print(f"  {'-'*30} {'-'*12} {'-'*10} {'-'*40}")

    for page in pages:
        print(f"  {page.frontmatter.id:<30} {page.frontmatter.type:<12} "
              f"{page.frontmatter.status:<10} {page.frontmatter.title[:40]}")


def cmd_show(args):
    """Show a wiki page."""
    engine = WikiEngine()
    page = engine.get_page(args.page_id)

    if page is None:
        print(f"Page not found: {args.page_id}")
        sys.exit(1)

    if args.raw:
        print(page.to_markdown())
    else:
        # Pretty print
        print(f"\n{'='*60}")
        print(f"ID:    {page.frontmatter.id}")
        print(f"Title: {page.frontmatter.title}")
        print(f"Type:  {page.frontmatter.type}")
        if page.frontmatter.category:
            print(f"Category: {page.frontmatter.category}")
        print(f"Status: {page.frontmatter.status}")
        print(f"Tags: {', '.join(page.frontmatter.tags) or '(none)'}")
        print(f"Created: {page.frontmatter.created_at.strftime('%Y-%m-%d')}")
        if page.frontmatter.updated_at:
            print(f"Updated: {page.frontmatter.updated_at.strftime('%Y-%m-%d')}")
        print(f"{'='*60}\n")
        print(page.content)


def cmd_search(args):
    """Search wiki pages."""
    engine = WikiEngine()
    results = engine.query(args.query, max_results=args.limit)

    if not results:
        print(f"No results for '{args.query}'")
        return

    print(f"\nSearch results for '{args.query}':\n")
    for r in results:
        print(f"  [{r['type']}] {r['id']} (score: {r['score']:.1f})")
        print(f"      {r['title']}")
        snippet = r['snippet'][:80] + "..." if len(r['snippet']) > 80 else r['snippet']
        print(f"      {snippet}")
        print()


def cmd_links(args):
    """Show page links."""
    engine = WikiEngine()

    if args.backlinks:
        links = engine.querier.get_backlinks(args.page_id)
        direction = "Backlinks to"
    else:
        links = engine.querier.get_linked_pages(args.page_id)
        direction = "Links from"

    page = engine.get_page(args.page_id)
    page_title = page.frontmatter.title if page else args.page_id

    print(f"\n{direction} {args.page_id} ({page_title}):\n")

    if not links:
        print("  No links found.")
        return

    for link in links:
        print(f"  - [{link['type']}] {link['id']}: {link['title']}")


def cmd_lint(args):
    """Run lint checks."""
    engine = WikiEngine()
    result = engine.lint(fix=args.fix)

    print(f"\n{'='*40}")
    print(f"Lint Results")
    print(f"{'='*40}")
    print(f"  Total issues: {result['total_issues']}")
    print(f"  Errors:       {result['errors']}")
    print(f"  Warnings:     {result['warnings']}")
    print(f"  Info:         {result['info']}")

    if args.fix and result.get('fixed'):
        print(f"  Fixed:        {len(result['fixed'])}")

    if result['issues'] and args.verbose:
        print(f"\n{'='*40}")
        print("Issues:")
        print(f"{'='*40}")
        for issue in result['issues']:
            severity = issue['severity'].upper()
            print(f"  [{severity}] {issue['page_id']}: {issue['message']}")
            if issue.get('suggestion'):
                print(f"      Suggestion: {issue['suggestion']}")

    # Exit with error code if errors found
    if result['errors'] > 0:
        sys.exit(1)


def cmd_stats(args):
    """Show wiki statistics."""
    engine = WikiEngine()
    stats = engine.get_stats()

    print(f"\n{'='*40}")
    print(f"Wiki Statistics")
    print(f"{'='*40}\n")

    print(f"Total Pages: {stats['total_pages']}\n")

    print("By Type:")
    for type_name, count in stats['by_type'].items():
        print(f"  {type_name}: {count}")

    print("\nBy Status:")
    for status, count in stats['by_status'].items():
        print(f"  {status}: {count}")

    if stats['by_category']:
        print("\nBy Category:")
        for category, count in stats['by_category'].items():
            print(f"  {category}: {count}")

    print(f"\nNeo4j Sync:")
    print(f"  Synced: {stats['synced_to_neo4j']}")
    print(f"  Pending: {stats['needs_sync']}")


def cmd_ingest(args):
    """Ingest a paper into the wiki."""
    engine = WikiEngine()

    print(f"\nIngesting paper: {args.arxiv_id}")
    print("="*40)

    result = engine.ingest_paper(
        args.arxiv_id,
        fetch=not args.no_fetch,
        parse=not args.no_parse,
        extract=not args.no_extract,
    )

    print(f"\nStatus: {result['status']}")

    if result['status'] == 'success':
        print(f"Wiki pages created: {result['wiki_pages']}")

        if 'steps' in result:
            for step, info in result['steps'].items():
                print(f"  {step}: {info}")
    else:
        print(f"Errors: {result['errors']}")
        sys.exit(1)


def cmd_create(args):
    """Create a new wiki page."""
    engine = WikiEngine()
    from qatlas.wiki.page import WikiPage, WikiFrontmatter
    from datetime import datetime

    # Create frontmatter
    fm = WikiFrontmatter(
        id=args.id,
        title=args.title,
        type=args.type,
        category=args.category,
        tags=args.tags.split(",") if args.tags else [],
        status=args.status,
    )

    # Read content from file or stdin
    if args.file:
        content = open(args.file).read()
    elif args.content:
        content = args.content
    else:
        print("Enter content (Ctrl+D to finish):")
        content = sys.stdin.read()

    page = WikiPage(frontmatter=fm, content=content)

    # Determine subdir
    if args.subdir:
        subdir = args.subdir
    else:
        subdir = None  # Auto-detect

    path = engine.save_page(page, subdir=subdir)
    print(f"Created page: {path}")


def _extract_arxiv_id_from_page(page: Any) -> Optional[str]:
    """Recover the canonical arXiv ID from a paper page's external_links.

    Round-tripping via ``frontmatter.id`` is lossy for old-scheme IDs
    (``wiki_source_page_id("quant-ph/0201024")`` does ``/ → -``), so we
    parse the abstract URL instead which preserves the full canonical
    form. Returns None if no arxiv.org link is present.
    """
    for link in getattr(page.frontmatter, "external_links", []) or []:
        url = getattr(link, "url", "") or ""
        marker = "arxiv.org/abs/"
        idx = url.find(marker)
        if idx == -1:
            continue
        tail = url[idx + len(marker):]
        # Strip version suffix and any trailing path/query.
        for sep in ("?", "#", "/"):
            i = tail.find(sep)
            if i != -1:
                tail = tail[:i]
        # arxiv versions: 1234.5678v3 → 1234.5678 (resolver doesn't need version)
        if "v" in tail:
            base, _, ver = tail.rpartition("v")
            if ver.isdigit():
                tail = base
        return tail.strip() or None
    return None


def _load_arxiv_authors_year(engine: WikiEngine, arxiv_id: str) -> Tuple[List[str], Optional[int]]:
    """Pull authors + year from the cached arXiv API JSON sidecar.

    Used by ``cmd_enrich_doi`` so the chain resolver can run author
    cross-check (otherwise Crossref / OpenAlex would only have the title
    to match on, and we'd downgrade everything to ``confidence=medium``).

    Returns ``([], None)`` when the sidecar is missing or malformed —
    the caller still runs the chain, it just gets weaker confidence on
    the matches.
    """
    try:
        path = engine.get_paper_asset_path("json", arxiv_id)
    except Exception:
        return [], None
    if not path or not Path(path).exists():
        return [], None
    try:
        data = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return [], None
    raw_authors = data.get("authors") or []
    authors: List[str] = []
    for a in raw_authors:
        if isinstance(a, str):
            if a.strip():
                authors.append(a.strip())
        elif isinstance(a, dict):
            name = a.get("name") or a.get("display_name") or a.get("full_name") or ""
            if name and isinstance(name, str):
                authors.append(name.strip())
    year: Optional[int] = None
    for key in ("published", "created", "date"):
        raw = data.get(key)
        if isinstance(raw, str) and len(raw) >= 4 and raw[:4].isdigit():
            year = int(raw[:4])
            break
    if year is None and isinstance(data.get("year"), int):
        year = data["year"]
    return authors, year


def cmd_enrich_doi(args):
    """Look up DOIs for arXiv paper pages that are missing them.

    Iterates ``type=source, category=paper`` pages, runs the configured
    resolver chain, and writes the result back via
    ``WikiFrontmatter.doi*`` fields. Pages whose ``doi`` is already set
    are skipped unless ``--force`` is given. Pages whose
    ``doi_source == "unresolved"`` are retried (that marker exists
    precisely so we know which pages to revisit when new resolvers
    appear).
    """
    from atlas.parser.doi import (
        ArxivSelfReportedResolver,
        ChainResolver,
        CrossrefResolver,
        DOIResolver,
        OpenAlexResolver,
        PaperContext,
    )

    engine = WikiEngine()

    sources = [s.strip() for s in (args.source or "arxiv,crossref,openalex").split(",") if s.strip()]
    builders = {
        "arxiv": lambda: ArxivSelfReportedResolver(
            json_path_getter=lambda aid: engine.get_paper_asset_path("json", aid)
        ),
        "crossref": lambda: CrossrefResolver(mailto=args.mailto),
        "openalex": lambda: OpenAlexResolver(mailto=args.mailto),
    }
    resolvers: List[DOIResolver] = []
    for s in sources:
        if s not in builders:
            print(f"warning: unknown resolver '{s}', skipping", file=sys.stderr)
            continue
        resolvers.append(builders[s]())
    if not resolvers:
        print("error: no usable resolvers configured", file=sys.stderr)
        sys.exit(1)
    chain = ChainResolver(resolvers)

    if args.paper:
        page = engine.get_page(args.paper)
        if page is None:
            print(f"error: page not found: {args.paper}", file=sys.stderr)
            sys.exit(1)
        candidates = [page]
    else:
        candidates = [
            p for p in engine.list_pages()
            if p.frontmatter.type == "source" and p.frontmatter.category == "paper"
        ]

    processed = 0
    resolved = 0
    unresolved = 0
    skipped = 0
    limit = args.limit if args.limit and args.limit > 0 else None

    for page in candidates:
        if limit is not None and processed >= limit:
            break
        fm = page.frontmatter
        if fm.doi and not args.force:
            skipped += 1
            continue

        arxiv_id = _extract_arxiv_id_from_page(page)
        if not arxiv_id:
            print(f"skip {fm.id}: no arXiv link", file=sys.stderr)
            skipped += 1
            continue

        authors, year = _load_arxiv_authors_year(engine, arxiv_id)
        ctx = PaperContext(
            arxiv_id=arxiv_id,
            title=fm.title,
            authors=authors,
            year=year,
        )

        processed += 1
        match = chain.resolve(ctx)
        now = datetime.now()
        if match is None:
            unresolved += 1
            print(f"unresolved {fm.id} ({arxiv_id})")
            if not args.dry_run:
                fm.doi = None
                fm.doi_source = "unresolved"
                fm.doi_confidence = None
                fm.doi_resolved_at = now
                engine.save_page(page)
            continue

        resolved += 1
        print(f"  match {fm.id} ({arxiv_id}): {match.doi}  [{match.source}/{match.confidence}]")
        if not args.dry_run:
            fm.doi = match.doi
            fm.doi_source = match.source
            fm.doi_confidence = match.confidence
            fm.doi_resolved_at = now
            engine.save_page(page)

    summary = (
        f"processed={processed} resolved={resolved} unresolved={unresolved} skipped={skipped}"
        + (" (dry-run, nothing written)" if args.dry_run else "")
    )
    print(summary)


def main():
    parser = argparse.ArgumentParser(
        description="QuantumAtlas Wiki Browser",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  List all concepts:
    python -m qatlas.wiki list --type concept

  Show a page:
    python -m qatlas.wiki show prim-qft

  Search:
    python -m qatlas.wiki search "quantum fourier"

  Check wiki health:
    python -m qatlas.wiki lint -v
        """,
    )

    subparsers = parser.add_subparsers(dest="command", help="Commands")

    # list command
    list_parser = subparsers.add_parser("list", help="List wiki pages")
    list_parser.add_argument("--type", help="Filter by page type")
    list_parser.add_argument("--tags", help="Filter by tags (comma-separated)")
    list_parser.add_argument("--status", help="Filter by status")
    list_parser.set_defaults(func=cmd_list)

    # show command
    show_parser = subparsers.add_parser("show", help="Show a wiki page")
    show_parser.add_argument("page_id", help="Page ID to show")
    show_parser.add_argument("--raw", action="store_true", help="Show raw markdown")
    show_parser.set_defaults(func=cmd_show)

    # search command
    search_parser = subparsers.add_parser("search", help="Search wiki")
    search_parser.add_argument("query", help="Search query")
    search_parser.add_argument("--limit", type=int, default=10, help="Max results")
    search_parser.set_defaults(func=cmd_search)

    # links command
    links_parser = subparsers.add_parser("links", help="Show page links")
    links_parser.add_argument("page_id", help="Page ID")
    links_parser.add_argument("--backlinks", action="store_true",
                               help="Show backlinks instead of forward links")
    links_parser.set_defaults(func=cmd_links)

    # lint command
    lint_parser = subparsers.add_parser("lint", help="Run lint checks")
    lint_parser.add_argument("--fix", action="store_true", help="Auto-fix issues")
    lint_parser.add_argument("--verbose", "-v", action="store_true", help="Show details")
    lint_parser.set_defaults(func=cmd_lint)

    # stats command
    stats_parser = subparsers.add_parser("stats", help="Show wiki statistics")
    stats_parser.set_defaults(func=cmd_stats)

    # ingest command
    ingest_parser = subparsers.add_parser("ingest", help="Ingest a paper into wiki")
    ingest_parser.add_argument("arxiv_id", help="arXiv paper ID")
    ingest_parser.add_argument("--no-fetch", action="store_true", help="Skip fetching")
    ingest_parser.add_argument("--no-parse", action="store_true", help="Skip parsing")
    ingest_parser.add_argument("--no-extract", action="store_true", help="Skip LLM extraction")
    ingest_parser.set_defaults(func=cmd_ingest)

    # create command
    create_parser = subparsers.add_parser("create", help="Create a new wiki page")
    create_parser.add_argument("id", help="Page ID")
    create_parser.add_argument("--title", required=True, help="Page title")
    create_parser.add_argument("--type", default="concept",
                                choices=["concept", "entity", "source", "comparison"])
    create_parser.add_argument("--category", help="Category (for entities)")
    create_parser.add_argument("--tags", help="Tags (comma-separated)")
    create_parser.add_argument("--status", default="draft",
                                choices=["draft", "review", "published"])
    create_parser.add_argument("--content", help="Page content")
    create_parser.add_argument("--file", "-f", help="Read content from file")
    create_parser.add_argument("--subdir", help="Target subdirectory")
    create_parser.set_defaults(func=cmd_create)

    # enrich-doi command
    enrich_parser = subparsers.add_parser(
        "enrich-doi",
        help="Resolve missing DOIs for arXiv paper pages",
        description=(
            "Run the DOI resolver chain (arxiv-self → Crossref → OpenAlex by default) "
            "against paper pages whose `doi` frontmatter is unset, and persist matches "
            "back to the page. Pages already carrying a DOI are skipped unless --force."
        ),
    )
    enrich_parser.add_argument(
        "--source",
        default="arxiv,crossref,openalex",
        help="Comma-separated resolver order (default: arxiv,crossref,openalex)",
    )
    enrich_parser.add_argument(
        "--paper",
        help="Restrict to a single paper page ID (e.g. paper-arxiv-1234.5678)",
    )
    enrich_parser.add_argument(
        "--force", action="store_true",
        help="Re-resolve pages that already have a DOI",
    )
    enrich_parser.add_argument(
        "--dry-run", action="store_true",
        help="Print matches without writing back to disk",
    )
    enrich_parser.add_argument(
        "--limit", type=int, default=0,
        help="Stop after processing N pages (0 = no limit)",
    )
    enrich_parser.add_argument(
        "--mailto", default=None,
        help="Contact email for Crossref/OpenAlex polite pool (recommended)",
    )
    enrich_parser.set_defaults(func=cmd_enrich_doi)

    args = parser.parse_args()

    if args.command is None:
        parser.print_help()
        sys.exit(1)

    args.func(args)


if __name__ == "__main__":
    main()
