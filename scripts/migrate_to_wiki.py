#!/usr/bin/env python
"""
Migration Script: YAML Primitives → Wiki Pages

Migrates existing YAML primitive definitions to the new wiki structure.

Usage:
    python scripts/migrate_to_wiki.py --dry-run    # Preview changes
    python scripts/migrate_to_wiki.py              # Execute migration
    python scripts/migrate_to_wiki.py --papers     # Also migrate papers
"""

import argparse
import json
import logging
import os
import shutil
import sys
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

# Add project root to path
project_root = Path(__file__).resolve().parent.parent
if str(project_root) not in sys.path:
    sys.path.insert(0, str(project_root))

logging.basicConfig(level=logging.INFO, format='%(levelname)s: %(message)s')
logger = logging.getLogger(__name__)


def find_project_root() -> Path:
    """Find the project root directory."""
    current = Path(__file__).resolve()
    for parent in current.parents:
        if (parent / "atlas").is_dir() and (parent / "pyproject.toml").exists():
            return parent
    return Path.cwd()


def migrate_primitives(
    project_root: Path,
    dry_run: bool = False,
) -> Dict[str, Any]:
    """
    Migrate YAML primitives to wiki pages.

    Args:
        project_root: Project root directory
        dry_run: If True, don't actually write files

    Returns:
        Migration statistics
    """
    from atlas.designer.primitive_loader import PrimitiveLoader, PrimitiveDefinition
    from atlas.wiki.templates import PageTemplate

    primitives_dir = project_root / "atlas" / "knowledge_graph" / "primitives"
    wiki_primitives_dir = project_root / "wiki" / "entities" / "primitives"

    if not primitives_dir.exists():
        logger.warning(f"Primitives directory not found: {primitives_dir}")
        return {"migrated": 0, "skipped": 0, "errors": []}

    # Load primitives using existing loader (use default path)
    loader = PrimitiveLoader()
    primitives = loader.get_all_primitives()

    stats = {
        "migrated": 0,
        "skipped": 0,
        "errors": [],
        "files": [],
    }

    logger.info(f"Found {len(primitives)} primitives to migrate")

    for prim in primitives:
        try:
            # Convert primitive ID to wiki format
            wiki_id = prim.id.replace("primitive_", "prim-")

            # Check if wiki page already exists
            wiki_path = wiki_primitives_dir / f"{wiki_id}.md"
            if wiki_path.exists():
                logger.info(f"  Skipping {prim.id} (wiki page exists)")
                stats["skipped"] += 1
                continue

            # Create wiki page
            complexity = {}
            if prim.complexity:
                complexity = {
                    "gate_count": prim.complexity.get("gate_count", "Unknown"),
                    "depth": prim.complexity.get("depth", "Unknown"),
                    "qubits": prim.complexity.get("qubits", "Unknown"),
                }

            # Convert references to wiki link format
            references = []
            for ref in (prim.references or []):
                if ref.startswith("arxiv:"):
                    arxiv_id = ref.replace("arxiv:", "")
                    references.append(f"arxiv-{arxiv_id}")
                else:
                    references.append(ref)

            # Convert prerequisites to wiki format
            prerequisites = []
            for pre in (prim.prerequisites or []):
                prerequisites.append(pre.replace("primitive_", "prim-"))

            page = PageTemplate.primitive_entity(
                id=wiki_id,
                name=prim.name,
                summary=prim.description or f"{prim.name} is a quantum primitive.",
                definition=prim.definition,
                complexity=complexity,
                references=references,
                prerequisites=prerequisites,
                tags=prim.tags or [prim.category] if hasattr(prim, 'category') else [],
            )
            page.frontmatter.status = "published"

            if dry_run:
                logger.info(f"  [DRY-RUN] Would create: {wiki_path}")
            else:
                wiki_primitives_dir.mkdir(parents=True, exist_ok=True)
                page.save(wiki_path)
                logger.info(f"  Created: {wiki_path}")

            stats["migrated"] += 1
            stats["files"].append(str(wiki_path))

        except Exception as e:
            logger.error(f"  Error migrating {prim.id}: {e}")
            stats["errors"].append(f"{prim.id}: {str(e)}")

    return stats


def migrate_papers(
    project_root: Path,
    dry_run: bool = False,
) -> Dict[str, Any]:
    """
    Migrate existing papers directory to RAW_DIR and wiki/.

    Args:
        project_root: Project root directory
        dry_run: If True, don't actually write files

    Returns:
        Migration statistics
    """
    old_papers_dir = project_root / "papers"
    raw_dir = Path(os.getenv("QATLAS_RAW_DIR") or os.getenv("RAW_DIR", "raw"))
    if not raw_dir.is_absolute():
        raw_dir = project_root / raw_dir
    wiki_sources_dir = project_root / "wiki" / "sources" / "papers"

    stats = {
        "pdfs": 0,
        "markdown": 0,
        "json": 0,
        "wiki_pages": 0,
        "errors": [],
    }

    if not old_papers_dir.exists():
        logger.info("No existing papers directory to migrate")
        return stats

    from atlas.paper_assets import safe_paper_key, wiki_source_page_id

    # Create target directories
    for subdir in ["pdf", "markdown", "json"]:
        (raw_dir / subdir).mkdir(parents=True, exist_ok=True)
    (raw_dir / "images").mkdir(parents=True, exist_ok=True)
    wiki_sources_dir.mkdir(parents=True, exist_ok=True)

    # Migrate PDFs
    for pdf in old_papers_dir.glob("*.pdf"):
        target = raw_dir / "pdf" / pdf.name
        if dry_run:
            logger.info(f"  [DRY-RUN] Would copy: {pdf} -> {target}")
        else:
            shutil.copy2(pdf, target)
            logger.info(f"  Copied PDF: {pdf.name}")
        stats["pdfs"] += 1

    # Migrate markdown
    for md in old_papers_dir.glob("*.md"):
        target = raw_dir / "markdown" / md.name
        if dry_run:
            logger.info(f"  [DRY-RUN] Would copy: {md} -> {target}")
        else:
            shutil.copy2(md, target)
            logger.info(f"  Copied Markdown: {md.name}")
        stats["markdown"] += 1

    # Migrate JSON and create wiki pages
    for js in old_papers_dir.glob("*.json"):
        arxiv_id = js.stem.replace("__", "/")
        key = safe_paper_key(arxiv_id)
        target = raw_dir / "json" / f"{key}.json"
        if dry_run:
            logger.info(f"  [DRY-RUN] Would copy: {js} -> {target}")
        else:
            shutil.copy2(js, target)
            logger.info(f"  Copied JSON: {js.name}")
        stats["json"] += 1

        # Create wiki page from JSON metadata
        try:
            with open(js) as f:
                metadata = json.load(f)

            from atlas.wiki.templates import PageTemplate

            page = PageTemplate.source_paper(
                arxiv_id=arxiv_id,
                title=metadata.get("title", f"Paper {arxiv_id}"),
                authors=metadata.get("authors", []),
                abstract=metadata.get("abstract", ""),
                published=metadata.get("published"),
                doi=metadata.get("doi"),
                categories=metadata.get("categories"),
            )
            page.frontmatter.status = "published"

            page_id = wiki_source_page_id(arxiv_id)
            wiki_path = wiki_sources_dir / f"{page_id}.md"
            if dry_run:
                logger.info(f"  [DRY-RUN] Would create: {wiki_path}")
            else:
                page.save(wiki_path)
                logger.info(f"  Created wiki page: {page_id}.md")
            stats["wiki_pages"] += 1

        except Exception as e:
            logger.error(f"  Error creating wiki page for {js.name}: {e}")
            stats["errors"].append(f"{js.name}: {str(e)}")

    return stats


def update_gitignore(project_root: Path, dry_run: bool = False) -> None:
    """Update .gitignore to reflect new directory structure."""
    gitignore_path = project_root / ".gitignore"

    new_entries = [
        "# Wiki and Raw directories (Issue #18)",
        "raw/pdf/*.pdf",
        "raw/markdown/*.md",
        "raw/json/*.json",
        "raw/images/",
        "wiki/log.md",
    ]

    if not gitignore_path.exists():
        logger.warning(".gitignore not found")
        return

    content = gitignore_path.read_text()

    if "Wiki and Raw directories" in content:
        logger.info("  .gitignore already updated")
        return

    if dry_run:
        logger.info("  [DRY-RUN] Would update .gitignore")
        return

    with open(gitignore_path, "a") as f:
        f.write("\n" + "\n".join(new_entries) + "\n")

    logger.info("  Updated .gitignore")


def main():
    parser = argparse.ArgumentParser(
        description="Migrate to wiki architecture (Issue #18)"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Preview changes without writing files",
    )
    parser.add_argument(
        "--papers",
        action="store_true",
        help="Also migrate papers directory",
    )
    parser.add_argument(
        "--primitives",
        action="store_true",
        default=True,
        help="Migrate YAML primitives to wiki (default: True)",
    )
    parser.add_argument(
        "--gitignore",
        action="store_true",
        default=True,
        help="Update .gitignore (default: True)",
    )

    args = parser.parse_args()

    project_root = find_project_root()
    logger.info(f"Project root: {project_root}")

    if args.dry_run:
        logger.info("=== DRY RUN MODE - No files will be modified ===\n")

    # Migrate primitives
    if args.primitives:
        logger.info("\n📦 Migrating primitives...")
        prim_stats = migrate_primitives(project_root, dry_run=args.dry_run)
        logger.info(f"   Migrated: {prim_stats['migrated']}")
        logger.info(f"   Skipped: {prim_stats['skipped']}")
        if prim_stats['errors']:
            logger.error(f"   Errors: {len(prim_stats['errors'])}")

    # Migrate papers
    if args.papers:
        logger.info("\n📄 Migrating papers...")
        paper_stats = migrate_papers(project_root, dry_run=args.dry_run)
        logger.info(f"   PDFs: {paper_stats['pdfs']}")
        logger.info(f"   Markdown: {paper_stats['markdown']}")
        logger.info(f"   JSON: {paper_stats['json']}")
        logger.info(f"   Wiki pages: {paper_stats['wiki_pages']}")
        if paper_stats['errors']:
            logger.error(f"   Errors: {len(paper_stats['errors'])}")

    # Update gitignore
    if args.gitignore:
        logger.info("\n📝 Updating .gitignore...")
        update_gitignore(project_root, dry_run=args.dry_run)

    logger.info("\n✨ Migration complete!")

    if args.dry_run:
        logger.info("\nRun without --dry-run to apply changes.")


if __name__ == "__main__":
    main()
