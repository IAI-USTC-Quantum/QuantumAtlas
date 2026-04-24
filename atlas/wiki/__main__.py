"""
Wiki Browser CLI

Interactive command-line interface for browsing and managing wiki pages.

Commands:
    list        List wiki pages
    show        Show a wiki page
    search      Search wiki pages
    links       Show page links
    lint        Run lint checks
    sync        Sync to Neo4j
    stats       Show wiki statistics

Usage:
    python -m atlas.wiki list --type concept
    python -m atlas.wiki show prim-qft
    python -m atlas.wiki search "quantum fourier"
    python -m atlas.wiki links prim-qft --backlinks
    python -m atlas.wiki lint --fix
    python -m atlas.wiki sync
"""

import argparse
import sys
from typing import List, Optional

from atlas.wiki.engine import WikiEngine


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


def cmd_sync(args):
    """Sync to Neo4j."""
    engine = WikiEngine(enable_neo4j_sync=True)

    print(f"\nSyncing wiki to Neo4j...")

    if args.page_id:
        result = engine.sync_to_neo4j(args.page_id)
        print(f"  Page: {result.get('page_id')}")
        print(f"  Success: {result.get('success')}")
        if result.get('error'):
            print(f"  Error: {result['error']}")
        if result.get('neo4j_id'):
            print(f"  Neo4j ID: {result['neo4j_id']}")
    else:
        result = engine.sync_to_neo4j()
        print(f"  Total: {result.get('total', 0)}")
        print(f"  Synced: {result.get('synced', 0)}")
        print(f"  Failed: {result.get('failed', 0)}")
        print(f"  Skipped: {result.get('skipped', 0)}")


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
        sync_neo4j=not args.wiki_only,
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
    from atlas.wiki.page import WikiPage, WikiFrontmatter
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


def main():
    parser = argparse.ArgumentParser(
        description="QuantumAtlas Wiki Browser",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  List all concepts:
    python -m atlas.wiki list --type concept

  Show a page:
    python -m atlas.wiki show prim-qft

  Search:
    python -m atlas.wiki search "quantum fourier"

  Check wiki health:
    python -m atlas.wiki lint -v

  Sync to Neo4j:
    python -m atlas.wiki sync
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

    # sync command
    sync_parser = subparsers.add_parser("sync", help="Sync to Neo4j")
    sync_parser.add_argument("page_id", nargs="?", help="Page ID (all if not specified)")
    sync_parser.set_defaults(func=cmd_sync)

    # stats command
    stats_parser = subparsers.add_parser("stats", help="Show wiki statistics")
    stats_parser.set_defaults(func=cmd_stats)

    # ingest command
    ingest_parser = subparsers.add_parser("ingest", help="Ingest a paper into wiki")
    ingest_parser.add_argument("arxiv_id", help="arXiv paper ID")
    ingest_parser.add_argument("--no-fetch", action="store_true", help="Skip fetching")
    ingest_parser.add_argument("--no-parse", action="store_true", help="Skip parsing")
    ingest_parser.add_argument("--no-extract", action="store_true", help="Skip LLM extraction")
    ingest_parser.add_argument("--wiki-only", action="store_true", help="Skip Neo4j sync")
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

    args = parser.parse_args()

    if args.command is None:
        parser.print_help()
        sys.exit(1)

    args.func(args)


if __name__ == "__main__":
    main()
