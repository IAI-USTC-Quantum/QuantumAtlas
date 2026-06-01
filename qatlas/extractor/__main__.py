"""
Algorithm Extractor CLI

Command-line interface for extracting algorithms from arXiv papers.

Usage:
    python -m qatlas.extractor <arxiv_id> --llm-provider openai
    python -m qatlas.extractor <arxiv_id> --llm-provider anthropic --dry-run
    python -m qatlas.extractor <arxiv_id> --output algorithm.yaml
"""

import os
import sys
import logging
from pathlib import Path
from typing import Optional

import click

from .llm_interface import create_llm, LLMInterface
from .algorithm_ir import AlgorithmIR
from .extractor import AlgorithmExtractor


# Configure logging
def setup_logging(verbose: bool = False):
    """Setup logging configuration."""
    level = logging.DEBUG if verbose else logging.INFO
    logging.basicConfig(
        level=level,
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=[logging.StreamHandler(sys.stdout)]
    )


@click.command()
@click.argument("arxiv_id")
@click.option(
    "--llm-provider",
    type=click.Choice(["openai", "anthropic", "claude"], case_sensitive=False),
    default="openai",
    help="LLM provider to use for extraction (default: openai)"
)
@click.option(
    "--model",
    default=None,
    help="Specific model to use (default: provider-specific default)"
)
@click.option(
    "--dry-run",
    is_flag=True,
    help="Extract only; do not write the YAML output"
)
@click.option(
    "--output",
    "-o",
    type=click.Path(),
    help="Save extracted algorithm to YAML file"
)
@click.option(
    "--verbose",
    "-v",
    is_flag=True,
    help="Enable verbose logging"
)
@click.option(
    "--papers-dir",
    default="./papers",
    help="Directory to store downloaded papers (default: ./papers)"
)
def main(
    arxiv_id: str,
    llm_provider: str,
    model: Optional[str],
    dry_run: bool,
    output: Optional[str],
    verbose: bool,
    papers_dir: str,
):
    """
    Extract algorithm information from an arXiv paper.
    
    ARXIV_ID: The arXiv paper ID (e.g., 9508027, 2301.00001)
    
    Examples:
        \b
        # Extract using OpenAI (default)
        python -m qatlas.extractor 9508027
        
        \b
        # Extract using Claude
        python -m qatlas.extractor 9508027 --llm-provider anthropic
        
        \b
        # Dry run - extract but don't write output
        python -m qatlas.extractor 9508027 --dry-run
        
        \b
        # Save to YAML file
        python -m qatlas.extractor 9508027 --output algorithm.yaml
    """
    setup_logging(verbose)
    logger = logging.getLogger(__name__)
    
    logger.info(f"Starting extraction for arXiv:{arxiv_id}")
    logger.info(f"Using LLM provider: {llm_provider}")
    
    # Validate arXiv ID format (basic check)
    arxiv_id = arxiv_id.strip()
    if not arxiv_id:
        click.echo("Error: arXiv ID cannot be empty", err=True)
        sys.exit(1)
    
    # Import parser modules
    try:
        from ..parser.arxiv_fetcher import ArxivFetcher
    except ImportError as e:
        click.echo(f"Error: Failed to import parser modules: {e}", err=True)
        click.echo("Make sure all dependencies are installed: pip install -e .", err=True)
        sys.exit(1)

    # Step 1: Fetch paper from arXiv
    click.echo(f"📥 Fetching paper arXiv:{arxiv_id}...")
    try:
        fetcher = ArxivFetcher(output_dir=papers_dir)
        paper_metadata = fetcher.fetch_metadata(arxiv_id)
        pdf_path = fetcher.fetch_pdf(arxiv_id)
        click.echo(f"✓ Downloaded: {pdf_path}")
    except Exception as e:
        click.echo(f"Error fetching paper: {e}", err=True)
        sys.exit(1)

    # Step 2: Obtain parsed paper text
    # Local PDF parsing was removed from the open-source build. The supported
    # flow is to fetch parsed markdown produced by MinerU; if no markdown is
    # available, fail loudly so the operator runs `qatlas mineru` first.
    click.echo("📄 Loading parsed markdown...")
    try:
        markdown_path = pdf_path.with_suffix(".md")
        if not markdown_path.exists():
            click.echo(
                f"Error: parsed markdown not found at {markdown_path}. "
                "Run `qatlas mineru <arxiv_id>` to generate it (the open-source "
                "build no longer ships a local PDF parser).",
                err=True,
            )
            sys.exit(1)
        paper_text = markdown_path.read_text(encoding="utf-8")
        click.echo(f"✓ Loaded {len(paper_text)} characters from {markdown_path}")
    except Exception as e:
        click.echo(f"Error loading markdown: {e}", err=True)
        sys.exit(1)
    
    # Step 3: Initialize LLM
    click.echo(f"🤖 Initializing {llm_provider} LLM...")
    try:
        llm_kwargs = {}
        if model:
            llm_kwargs["model"] = model
        
        llm = create_llm(llm_provider, **llm_kwargs)
    except ValueError as e:
        click.echo(f"Error: {e}", err=True)
        click.echo("Make sure the required API key is set:", err=True)
        if llm_provider in ["openai"]:
            click.echo("  - OPENAI_API_KEY environment variable", err=True)
        elif llm_provider in ["anthropic", "claude"]:
            click.echo("  - ANTHROPIC_API_KEY environment variable", err=True)
        sys.exit(1)
    except ImportError as e:
        click.echo(f"Error: Missing required package: {e}", err=True)
        sys.exit(1)
    
    # Step 4: Extract algorithm
    click.echo("🔍 Extracting algorithm information...")
    try:
        extractor = AlgorithmExtractor(llm)
        algorithm_ir = extractor.extract_from_paper(
            paper_text=paper_text,
            arxiv_id=arxiv_id,
        )
        click.echo(f"✓ Extracted: {algorithm_ir.name}")
        click.echo(f"  Problem Type: {algorithm_ir.problem_type}")
        click.echo(f"  Confidence: {algorithm_ir.extraction_confidence:.1%}")
    except Exception as e:
        logger.exception("Extraction failed")
        click.echo(f"Error during extraction: {e}", err=True)
        sys.exit(1)
    
    # Step 5: Display results
    click.echo("\n" + "=" * 60)
    click.echo("EXTRACTION RESULTS")
    click.echo("=" * 60)
    click.echo(algorithm_ir.to_summary())
    click.echo("=" * 60)
    
    # Step 6: Save to YAML if requested
    if output:
        click.echo(f"\n💾 Saving to YAML: {output}")
        try:
            extractor.export_to_yaml(algorithm_ir, output)
            click.echo(f"✓ Saved to {output}")
        except Exception as e:
            click.echo(f"Error saving to YAML: {e}", err=True)
    
    # Show token usage
    total_usage = extractor.get_total_token_usage()
    click.echo(f"\n📊 Token Usage:")
    click.echo(f"  Prompt: {total_usage.prompt_tokens:,}")
    click.echo(f"  Completion: {total_usage.completion_tokens:,}")
    click.echo(f"  Total: {total_usage.total_tokens:,}")
    
    click.echo("\n✅ Done!")


@click.group()
def cli():
    """Algorithm Extractor CLI commands."""
    pass


# Add main command to cli group
cli.add_command(main, name="extract")


if __name__ == "__main__":
    # When run directly, use the main command
    # When imported, use the cli group
    if len(sys.argv) > 1 and sys.argv[1] == "extract":
        cli()
    else:
        main()
