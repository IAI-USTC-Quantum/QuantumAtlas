"""
Paper Parser CLI

Usage:
    python -m qatlas.parser <arxiv_id>
    python -m qatlas.parser 9508027
    python -m qatlas.parser arXiv:9508027

Options:
    --output-dir, -o    Output directory for downloaded files
    --no-pdf            Skip PDF download
    --save-markdown, -m Save parsed content as Markdown
    --save-json, -j     Save parsed content as JSON
    --wiki              Ingest into wiki (new path, recommended)
    --wiki-only         Wiki only, skip Neo4j sync
    --import-to-neo4j   Import directly to Neo4j (legacy path)
"""

import sys
import argparse
from pathlib import Path


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description="Fetch and parse arXiv papers",
        prog="atlas-parser"
    )

    parser.add_argument(
        "arxiv_id",
        help="arXiv paper ID (e.g., 9508027 or arXiv:9508027)"
    )

    parser.add_argument(
        "-o", "--output-dir",
        default="./papers",
        help="Output directory for downloaded files (default: ./papers)"
    )

    parser.add_argument(
        "--no-pdf",
        action="store_true",
        help="Skip PDF download"
    )

    parser.add_argument(
        "-m", "--save-markdown",
        action="store_true",
        help="Save parsed content as Markdown"
    )

    parser.add_argument(
        "-j", "--save-json",
        action="store_true",
        help="Save parsed content as JSON"
    )

    # New path: Wiki integration
    parser.add_argument(
        "--wiki",
        action="store_true",
        help="Ingest paper into wiki (recommended new path)"
    )

    # LLM extraction options
    parser.add_argument(
        "--extract",
        action="store_true",
        help="Extract algorithm info using LLM (requires --wiki)"
    )

    parser.add_argument(
        "--llm-provider",
        default="openai",
        choices=["openai", "anthropic"],
        help="LLM provider for extraction (default: openai)"
    )

    args = parser.parse_args()

    print(f"🔬 QuantumAtlas Paper Parser")
    print(f"=" * 50)

    # ========== New Path: Wiki Integration ==========
    if args.wiki:
        print(f"\n📚 Wiki Ingestion Mode")
        print(f"=" * 50)

        try:
            from qatlas.wiki.engine import WikiEngine

            from qatlas.config import ServerConfig

            config = ServerConfig.from_env()
            engine = WikiEngine(
                wiki_dir=config.wiki_dir,
                raw_dir=config.raw_dir,
            )

            result = engine.ingest_paper(
                args.arxiv_id,
                fetch=not args.no_pdf,
                parse=True,
                extract=args.extract,
                create_wiki=True,
                llm_provider=args.llm_provider,
            )

            print(f"\n📊 Ingestion Result:")
            print(f"   Status: {result['status']}")

            if result['status'] == 'success':
                print(f"   Wiki pages created: {len(result['wiki_pages'])}")
                for page_id in result['wiki_pages']:
                    print(f"     - {page_id}")

                if result.get('steps'):
                    if 'fetch' in result['steps']:
                        print(f"\n   Paper: {result['steps']['fetch']['metadata']['title']}")
                    if 'extract' in result['steps']:
                        print(f"   Algorithm: {result['steps']['extract']['algorithm_name']}")
                        print(f"   Primitives: {result['steps']['extract']['primitives']}")
            else:
                print(f"   Errors: {result['errors']}")
                sys.exit(1)

        except Exception as e:
            print(f"❌ Error during wiki ingestion: {e}")
            import traceback
            traceback.print_exc()
            sys.exit(1)

        print(f"\n✨ Done!")
        return

    # ========== Standalone Parse Path (no wiki) ==========
    # Step 1: Fetch from arXiv
    print(f"\n📥 Fetching paper: {args.arxiv_id}")

    try:
        from .arxiv_fetcher import ArxivFetcher
        fetcher = ArxivFetcher(output_dir=args.output_dir)
        pdf_path, metadata = fetcher.fetch(
            args.arxiv_id,
            download_pdf=not args.no_pdf
        )
    except Exception as e:
        print(f"❌ Error fetching paper: {e}")
        sys.exit(1)

    print(f"✅ Title: {metadata['title']}")
    print(f"✅ Authors: {', '.join(metadata['authors'])}")
    print(f"✅ Categories: {', '.join(metadata['categories'])}")

    if pdf_path:
        print(f"✅ PDF saved to: {pdf_path}")

    # Step 2: Parse PDF — only MinerU is supported.
    # Local PDF parsing inside this CLI has been removed; the recommended
    # flow is to run MinerU locally and push the result with
    # `qatlas contrib mineru`.
    if args.save_markdown or args.save_json:
        print(
            "\n⚠️  --save-markdown / --save-json are only available via MinerU; "
            "run `qatlas contrib mineru <arxiv_id>` to parse with your own token "
            "and push the markdown + images back to the server.",
            file=sys.stderr,
        )

    print(f"\n✨ Done!")


if __name__ == "__main__":
    main()
