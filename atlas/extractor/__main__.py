"""
Algorithm Extractor CLI

Command-line interface for extracting algorithms from arXiv papers.

Usage:
    python -m atlas.extractor <arxiv_id> --llm-provider openai
    python -m atlas.extractor <arxiv_id> --llm-provider anthropic --dry-run
    python -m atlas.extractor <arxiv_id> --output algorithm.yaml
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
from ..knowledge.neo4j_client import Neo4jClient


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
    help="Extract but don't save to knowledge graph"
)
@click.option(
    "--output",
    "-o",
    type=click.Path(),
    help="Save extracted algorithm to YAML file"
)
@click.option(
    "--save-to-kg",
    is_flag=True,
    default=False,
    help="Save extracted algorithm to Neo4j knowledge graph"
)
@click.option(
    "--neo4j-uri",
    default=None,
    envvar="NEO4J_URI",
    help="Neo4j URI (default: bolt://localhost:7687)"
)
@click.option(
    "--neo4j-user",
    default=None,
    envvar="NEO4J_USER",
    help="Neo4j username (default: neo4j)"
)
@click.option(
    "--neo4j-password",
    default=None,
    envvar="NEO4J_PASSWORD",
    help="Neo4j password"
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
    save_to_kg: bool,
    neo4j_uri: Optional[str],
    neo4j_user: Optional[str],
    neo4j_password: Optional[str],
    verbose: bool,
    papers_dir: str,
):
    """
    Extract algorithm information from an arXiv paper.
    
    ARXIV_ID: The arXiv paper ID (e.g., 9508027, 2301.00001)
    
    Examples:
        \b
        # Extract using OpenAI (default)
        python -m atlas.extractor 9508027
        
        \b
        # Extract using Claude
        python -m atlas.extractor 9508027 --llm-provider anthropic
        
        \b
        # Dry run - extract but don't save
        python -m atlas.extractor 9508027 --dry-run
        
        \b
        # Save to YAML file
        python -m atlas.extractor 9508027 --output algorithm.yaml
        
        \b
        # Save to knowledge graph
        python -m atlas.extractor 9508027 --save-to-kg
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
        from ..parser.pdf_parser import PDFParser
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
    
    # Step 2: Parse PDF to text
    click.echo("📄 Parsing PDF...")
    try:
        parser = PDFParser()
        paper_text = parser.parse(str(pdf_path))
        click.echo(f"✓ Extracted {len(paper_text)} characters")
    except Exception as e:
        click.echo(f"Error parsing PDF: {e}", err=True)
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
            paper_metadata=paper_metadata,
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
    
    # Step 7: Save to knowledge graph if requested
    if save_to_kg and not dry_run:
        click.echo("\n🗄️  Saving to knowledge graph...")
        try:
            neo4j = Neo4jClient(
                uri=neo4j_uri,
                username=neo4j_user,
                password=neo4j_password,
            )
            neo4j.connect()
            
            results = extractor.save_to_knowledge_graph(neo4j, algorithm_ir)
            
            click.echo(f"✓ Created Algorithm node: {results['algorithm_id']}")
            if results['paper_id']:
                click.echo(f"✓ Created Paper node: {results['paper_id']}")
            if results['primitives_linked']:
                click.echo(f"✓ Linked primitives: {', '.join(results['primitives_linked'])}")
            
            neo4j.close()
        except Exception as e:
            click.echo(f"Error saving to knowledge graph: {e}", err=True)
            if verbose:
                logger.exception("Knowledge graph save failed")
    
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


@cli.command()
@click.argument("yaml_file", type=click.Path(exists=True))
@click.option(
    "--neo4j-uri",
    default=None,
    envvar="NEO4J_URI",
    help="Neo4j URI"
)
@click.option(
    "--neo4j-user",
    default=None,
    envvar="NEO4J_USER",
    help="Neo4j username"
)
@click.option(
    "--neo4j-password",
    default=None,
    envvar="NEO4J_PASSWORD",
    help="Neo4j password"
)
@click.option("--verbose", "-v", is_flag=True)
def import_yaml(
    yaml_file: str,
    neo4j_uri: Optional[str],
    neo4j_user: Optional[str],
    neo4j_password: Optional[str],
    verbose: bool,
):
    """
    Import an algorithm from YAML file to knowledge graph.
    
    YAML_FILE: Path to the YAML file containing algorithm IR
    """
    setup_logging(verbose)
    
    click.echo(f"📖 Loading algorithm from {yaml_file}...")
    try:
        algorithm_ir = AlgorithmIR.from_yaml(filepath=yaml_file)
        click.echo(f"✓ Loaded: {algorithm_ir.name}")
    except Exception as e:
        click.echo(f"Error loading YAML: {e}", err=True)
        sys.exit(1)
    
    click.echo("🗄️  Saving to knowledge graph...")
    try:
        neo4j = Neo4jClient(
            uri=neo4j_uri,
            username=neo4j_user,
            password=neo4j_password,
        )
        neo4j.connect()
        
        # Create a dummy extractor to use save method
        llm = create_llm("openai")  # Won't be used
        extractor = AlgorithmExtractor(llm)
        
        results = extractor.save_to_knowledge_graph(neo4j, algorithm_ir)
        
        click.echo(f"✓ Created Algorithm node: {results['algorithm_id']}")
        if results['paper_id']:
            click.echo(f"✓ Created Paper node: {results['paper_id']}")
        
        neo4j.close()
        click.echo("\n✅ Import complete!")
    except Exception as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)


# Add main command to cli group
cli.add_command(main, name="extract")


if __name__ == "__main__":
    # When run directly, use the main command
    # When imported, use the cli group
    if len(sys.argv) > 1 and sys.argv[1] in ["extract", "import-yaml"]:
        cli()
    else:
        main()
