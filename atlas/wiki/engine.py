"""
Wiki Engine Core

Main orchestrator for wiki operations including page CRUD, ingestion, querying, and linting.

The WikiEngine provides a unified interface for:
- Page management (get, save, delete, list)
- Ingest workflow (paper → wiki pages → Neo4j sync)
- Query workflow (search and retrieve)
- Lint workflow (health checks)

Usage:
    engine = WikiEngine(wiki_dir="wiki", raw_dir="raw")

    # Page CRUD
    page = engine.get_page("prim-qft")
    engine.save_page(page)

    # Ingest
    result = engine.ingest_paper("9508027")

    # Query
    results = engine.query("quantum fourier transform")

    # Lint
    issues = engine.lint()

    # Sync to Neo4j
    engine.sync_to_neo4j("prim-qft")
"""

import logging
import os
from pathlib import Path
from typing import Any, Dict, List, Optional, Type

from atlas.paper_assets import safe_paper_key

from .page import WikiPage, WikiFrontmatter
from .templates import PageTemplate

logger = logging.getLogger(__name__)

SYSTEM_MARKDOWN_FILES = {"index.md", "log.md", "README.md"}


class WikiEngine:
    """
    Main wiki engine for QuantumAtlas knowledge base.

    Responsibilities:
    - Manage wiki pages (CRUD operations)
    - Ensure directory structure exists
    - Orchestrate ingest/query/lint workflows
    - Coordinate with Neo4j sync

    The wiki directory structure:
    wiki/
    ├── index.md           # Main index
    ├── log.md             # Activity log
    ├── concepts/          # Concept definitions
    ├── entities/
    │   ├── algorithms/    # Algorithm entities
    │   ├── primitives/    # Primitive entities
    │   └── people/        # Person entities
    ├── sources/
    │   └── papers/        # Paper summaries
    └── comparisons/       # Comparative analysis
    """

    # Subdirectory mapping for page types
    TYPE_TO_SUBDIR: Dict[str, str] = {
        "concept": "concepts",
        "entity": "entities",  # Further routing based on category
        "source": "sources/papers",
        "comparison": "comparisons",
    }

    # Category routing for entity pages
    ENTITY_CATEGORY_TO_SUBDIR: Dict[str, str] = {
        "algorithm": "entities/algorithms",
        "primitive": "entities/primitives",
        "person": "entities/people",
    }

    def __init__(
        self,
        wiki_dir: Optional[str] = None,
        raw_dir: Optional[str] = None,
        enable_neo4j_sync: bool = True,
        project_root: Optional[str] = None,
    ):
        """
        Initialize wiki engine.

        Args:
            wiki_dir: Path to wiki directory (default: ./wiki)
            raw_dir: Path to canonical raw asset directory (default: ./raw)
            enable_neo4j_sync: Whether to enable Neo4j synchronization
            project_root: Project root directory (auto-detected if None)
        """
        # Auto-detect project root
        if project_root is None:
            # Look for atlas module to find project root
            current = Path(__file__).resolve()
            for parent in current.parents:
                if (parent / "atlas").is_dir():
                    project_root = str(parent)
                    break
            if project_root is None:
                project_root = os.getcwd()

        self.project_root = Path(project_root)

        # Set directories relative to project root. CLI callers usually configure these
        # through the environment, while tests and embedded callers pass them explicitly.
        self.wiki_dir = self._resolve_path(wiki_dir or os.getenv("WIKI_DIR", "wiki"))
        self.raw_dir = self._resolve_path(raw_dir or os.getenv("RAW_DIR", "raw"))

        # Ensure directory structure exists
        self._ensure_directories()

        # Initialize workflow components (lazy)
        self._ingester = None
        self._querier = None
        self._linter = None
        self._neo4j_sync = None
        self._enable_neo4j_sync = enable_neo4j_sync

        logger.debug(
            f"WikiEngine initialized: wiki_dir={self.wiki_dir}, "
            f"raw_dir={self.raw_dir}"
        )

    def _resolve_path(self, path: str) -> Path:
        """Resolve a path relative to the project root when needed."""
        candidate = Path(path)
        if not candidate.is_absolute():
            candidate = self.project_root / candidate
        return candidate.resolve()

    def _ensure_directories(self) -> None:
        """Create wiki and raw asset directory structure if not exists."""
        # Wiki subdirectories
        wiki_subdirs = [
            "concepts",
            "entities/algorithms",
            "entities/primitives",
            "entities/people",
            "sources/papers",
            "comparisons",
        ]
        for subdir in wiki_subdirs:
            (self.wiki_dir / subdir).mkdir(parents=True, exist_ok=True)

        self.raw_dir.mkdir(parents=True, exist_ok=True)

        for subdir in ["pdf", "markdown", "json", "images"]:
            (self.raw_dir / subdir).mkdir(parents=True, exist_ok=True)

    def get_paper_asset_dir(self, kind: str) -> Path:
        """Return one paper asset subdirectory."""
        if kind not in {"pdf", "markdown", "json", "images"}:
            raise ValueError(f"unknown paper asset kind: {kind}")
        return self.raw_dir / kind

    def get_paper_asset_path(self, kind: str, arxiv_id: str) -> Path:
        """Return the canonical asset path for a paper."""
        key = safe_paper_key(arxiv_id)
        if kind == "pdf":
            return self.get_paper_asset_dir("pdf") / f"{key}.pdf"
        if kind == "markdown":
            return self.get_paper_asset_dir("markdown") / f"{key}.md"
        if kind == "json":
            return self.get_paper_asset_dir("json") / f"{key}.json"
        if kind == "images":
            return self.get_paper_asset_dir("images") / key
        raise ValueError(f"unknown paper asset kind: {kind}")

    # === Page CRUD Operations ===

    def _iter_page_files(self):
        """Yield Markdown files that represent actual wiki pages."""
        for filepath in self.wiki_dir.rglob("*.md"):
            if filepath.name in SYSTEM_MARKDOWN_FILES:
                continue
            yield filepath

    def get_page(self, page_id: str) -> Optional[WikiPage]:
        """
        Get a wiki page by ID.

        Searches across all wiki subdirectories for a page matching the ID.

        Args:
            page_id: Unique page identifier

        Returns:
            WikiPage if found, None otherwise
        """
        for filepath in self._iter_page_files():
            try:
                page = WikiPage.from_file(filepath)
                if page.frontmatter.id == page_id:
                    return page
            except Exception as e:
                logger.warning(f"Failed to parse {filepath}: {e}")
                continue
        return None

    def get_page_by_path(self, rel_path: str) -> Optional[WikiPage]:
        """
        Get a wiki page by relative file path.

        Args:
            rel_path: Relative path from wiki_dir (e.g., "concepts/qft.md")

        Returns:
            WikiPage if found, None otherwise
        """
        filepath = self.wiki_dir / rel_path
        if filepath.exists() and filepath.suffix == ".md":
            try:
                return WikiPage.from_file(filepath)
            except Exception as e:
                logger.warning(f"Failed to parse {filepath}: {e}")
        return None

    def save_page(
        self,
        page: WikiPage,
        subdir: Optional[str] = None,
        filename: Optional[str] = None,
    ) -> Path:
        """
        Save a wiki page to disk.

        Args:
            page: WikiPage to save
            subdir: Target subdirectory (auto-detected from type if not provided)
            filename: Target filename (derived from page ID if not provided)

        Returns:
            Path to saved file
        """
        if subdir is None:
            subdir = self._get_subdir_for_page(page)

        if filename is None:
            filename = f"{page.frontmatter.id}.md"

        filepath = self.wiki_dir / subdir / filename
        filepath.parent.mkdir(parents=True, exist_ok=True)

        saved_path = page.save(filepath)
        logger.info(f"Saved wiki page: {saved_path}")

        return saved_path

    def delete_page(self, page_id: str) -> bool:
        """
        Delete a wiki page by ID.

        Args:
            page_id: Page ID to delete

        Returns:
            True if page was deleted, False if not found
        """
        filepath = self._find_page_file(page_id)
        if filepath:
            filepath.unlink()
            logger.info(f"Deleted wiki page: {filepath}")
            return True
        return False

    def list_pages(
        self,
        page_type: Optional[str] = None,
        category: Optional[str] = None,
        tags: Optional[List[str]] = None,
        status: Optional[str] = None,
    ) -> List[WikiPage]:
        """
        List wiki pages with optional filtering.

        Args:
            page_type: Filter by page type (concept, entity, source, comparison)
            category: Filter by category (algorithm, primitive, person)
            tags: Filter by tags (pages must have at least one matching tag)
            status: Filter by publication status

        Returns:
            List of matching WikiPages
        """
        pages = []

        for filepath in self._iter_page_files():
            try:
                page = WikiPage.from_file(filepath)

                # Apply filters
                if page_type and page.frontmatter.type != page_type:
                    continue
                if category and page.frontmatter.category != category:
                    continue
                if tags and not any(t in page.frontmatter.tags for t in tags):
                    continue
                if status and page.frontmatter.status != status:
                    continue

                pages.append(page)
            except Exception as e:
                logger.warning(f"Failed to parse {filepath}: {e}")

        return pages

    def update_page(self, page_id: str, content: Optional[str] = None, **fm_updates) -> Optional[WikiPage]:
        """
        Update an existing wiki page.

        Args:
            page_id: Page ID to update
            content: New content (keep existing if None)
            **fm_updates: Frontmatter field updates

        Returns:
            Updated WikiPage if found, None otherwise
        """
        page = self.get_page(page_id)
        if page is None:
            return None

        # Update content
        if content is not None:
            page.content = content

        # Update frontmatter
        for key, value in fm_updates.items():
            if hasattr(page.frontmatter, key):
                setattr(page.frontmatter, key, value)

        # Update timestamp
        page.update_timestamp()

        # Save back to file
        self.save_page(page)

        return page

    # === Workflow Methods ===

    @property
    def ingester(self):
        """Lazy initialization of WikiIngester."""
        if self._ingester is None:
            from .ingester import WikiIngester
            self._ingester = WikiIngester(self)
        return self._ingester

    @property
    def querier(self):
        """Lazy initialization of WikiQuerier."""
        if self._querier is None:
            from .querier import WikiQuerier
            self._querier = WikiQuerier(self)
        return self._querier

    @property
    def linter(self):
        """Lazy initialization of WikiLinter."""
        if self._linter is None:
            from .linter import WikiLinter
            self._linter = WikiLinter(self)
        return self._linter

    @property
    def neo4j_sync(self):
        """Lazy initialization of Neo4j sync."""
        if self._neo4j_sync is None and self._enable_neo4j_sync:
            try:
                from .sync.neo4j_sync import Neo4jWikiSync
                self._neo4j_sync = Neo4jWikiSync()
            except ImportError:
                logger.warning("Neo4j sync disabled: neo4j driver not available")
                self._neo4j_sync = None
        return self._neo4j_sync

    def ingest_paper(self, arxiv_id: str, **kwargs) -> Dict[str, Any]:
        """
        Ingest a paper into the wiki.

        Workflow:
        1. Fetch paper (if not in raw/)
        2. Parse PDF to markdown
        3. Extract algorithm info via LLM
        4. Create wiki pages
        5. Sync to Neo4j (if enabled)

        Args:
            arxiv_id: arXiv paper ID
            **kwargs: Additional options for ingestion

        Returns:
            Dict with ingest results
        """
        return self.ingester.ingest_paper(arxiv_id, **kwargs)

    def query(
        self,
        query: str,
        page_types: Optional[List[str]] = None,
        max_results: int = 10,
    ) -> List[Dict[str, Any]]:
        """
        Query the wiki for information.

        Args:
            query: Search query string
            page_types: Filter by page types
            max_results: Maximum results to return

        Returns:
            List of matching pages with relevance scores
        """
        return self.querier.search(query, page_types=page_types, max_results=max_results)

    def lint(self, fix: bool = False) -> Dict[str, Any]:
        """
        Run lint checks on wiki.

        Checks:
        - Frontmatter validity
        - Orphan pages
        - Missing concept definitions
        - Contradictions

        Args:
            fix: Whether to auto-fix issues

        Returns:
            Dict with lint results and issues
        """
        return self.linter.run(fix=fix)

    def sync_to_neo4j(self, page_id: Optional[str] = None) -> Dict[str, Any]:
        """
        Sync wiki pages to Neo4j.

        Args:
            page_id: Specific page to sync (all pages if None)

        Returns:
            Sync results
        """
        if not self._enable_neo4j_sync or self.neo4j_sync is None:
            return {
                "success": False,
                "error": "Neo4j sync not enabled or unavailable",
            }

        if page_id:
            page = self.get_page(page_id)
            if page is None:
                return {"success": False, "error": f"Page not found: {page_id}"}
            return self.neo4j_sync.sync_page(page)

        # Sync all pages
        pages = self.list_pages(status="published")
        return self.neo4j_sync.sync_all(pages)

    # === Utility Methods ===

    def _get_subdir_for_page(self, page: WikiPage) -> str:
        """Determine target subdirectory for a page."""
        page_type = page.frontmatter.type

        if page_type == "entity":
            category = page.frontmatter.category
            if category in self.ENTITY_CATEGORY_TO_SUBDIR:
                return self.ENTITY_CATEGORY_TO_SUBDIR[category]
            return "entities"  # Default for unknown category

        return self.TYPE_TO_SUBDIR.get(page_type, "")

    def _find_page_file(self, page_id: str) -> Optional[Path]:
        """Find the file path for a page ID."""
        for filepath in self._iter_page_files():
            if filepath.stem == page_id:
                return filepath
            # Also check by parsing frontmatter
            try:
                page = WikiPage.from_file(filepath)
                if page.frontmatter.id == page_id:
                    return filepath
            except Exception:
                continue
        return None

    def get_stats(self) -> Dict[str, Any]:
        """
        Get statistics about the wiki.

        Returns:
            Dict with counts by type, status, etc.
        """
        pages = self.list_pages()

        stats = {
            "total_pages": len(pages),
            "by_type": {},
            "by_status": {},
            "by_category": {},
            "synced_to_neo4j": 0,
            "needs_sync": 0,
        }

        for page in pages:
            # By type
            type_key = page.frontmatter.type
            stats["by_type"][type_key] = stats["by_type"].get(type_key, 0) + 1

            # By status
            status_key = page.frontmatter.status
            stats["by_status"][status_key] = stats["by_status"].get(status_key, 0) + 1

            # By category (for entities)
            if page.frontmatter.category:
                cat_key = page.frontmatter.category
                stats["by_category"][cat_key] = stats["by_category"].get(cat_key, 0) + 1

            # Sync status
            if page.frontmatter.neo4j_synced:
                stats["synced_to_neo4j"] += 1
            else:
                stats["needs_sync"] += 1

        return stats

    def append_to_log(self, message: str) -> None:
        """
        Append a message to the wiki log.

        Args:
            message: Log message to append
        """
        log_path = self.wiki_dir / "log.md"
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M")

        entry = f"\n{timestamp} - {message}\n"

        with open(log_path, "a", encoding="utf-8") as f:
            f.write(entry)

    def update_index(self) -> None:
        """
        Update the wiki index with current statistics.

        Regenerates the index.md file with current page counts.
        """
        stats = self.get_stats()
        index_path = self.wiki_dir / "index.md"

        # Read existing index content (preserve custom sections)
        if index_path.exists():
            existing = index_path.read_text(encoding="utf-8")
        else:
            existing = ""

        # Find the statistics section and update it
        # This is a simple implementation - can be enhanced

        content = existing.split("## Statistics")[0] if "## Statistics" in existing else existing

        # Add updated statistics section
        stats_section = f"""## Statistics

| Type | Count |
|------|-------|
| Concepts | {stats['by_type'].get('concept', 0)} |
| Algorithms | {stats['by_category'].get('algorithm', 0)} |
| Primitives | {stats['by_category'].get('primitive', 0)} |
| Papers | {stats['by_type'].get('source', 0)} |
| Comparisons | {stats['by_type'].get('comparison', 0)} |
| **Total** | {stats['total_pages']} |

| Status | Count |
|--------|-------|
| Draft | {stats['by_status'].get('draft', 0)} |
| Review | {stats['by_status'].get('review', 0)} |
| Published | {stats['by_status'].get('published', 0)} |
"""

        # Preserve the rest of the content after statistics
        rest = ""
        if "## Statistics" in existing:
            parts = existing.split("## Recent Activity")
            if len(parts) > 1:
                rest = f"\n## Recent Activity{parts[1]}"

        index_path.write_text(content + stats_section + rest, encoding="utf-8")
        logger.debug("Updated wiki index")


# Import datetime for log timestamps
from datetime import datetime
