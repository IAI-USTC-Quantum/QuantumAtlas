"""
Neo4j Wiki Sync

Synchronizes wiki pages to Neo4j knowledge graph.

Sync Rules:
- entity/algorithm → Algorithm node
- entity/primitive → Primitive node
- entity/people → Author node
- source/paper → Paper node
- Wiki links → CITES/RELATED relationships

The sync is one-way: Wiki is the source of truth for entity data,
Neo4j stores relationships and enables graph queries.
"""

import logging
import re
from typing import Any, Dict, List, Optional

logger = logging.getLogger(__name__)


class Neo4jWikiSync:
    """
    Synchronizes wiki pages to Neo4j knowledge graph.

    The wiki is the source of truth for entity data. Neo4j stores
    relationships and enables graph traversal queries.

    Sync Order:
    1. Primitives (no dependencies)
    2. Algorithms (depend on primitives)
    3. Papers (link to algorithms)

    Usage:
        sync = Neo4jWikiSync()
        result = sync.sync_page(wiki_page)
        # or
        results = sync.sync_all(wiki_pages)
    """

    def __init__(
        self,
        neo4j_uri: Optional[str] = None,
        neo4j_user: Optional[str] = None,
        neo4j_password: Optional[str] = None,
    ):
        """
        Initialize Neo4j sync.

        Args:
            neo4j_uri: Neo4j Bolt URI (default: from env NEO4J_URI)
            neo4j_user: Neo4j username (default: from env NEO4J_USER)
            neo4j_password: Neo4j password (default: from env NEO4J_PASSWORD)
        """
        self._client = None
        self._config = {
            "uri": neo4j_uri,
            "user": neo4j_user,
            "password": neo4j_password,
        }

    @property
    def client(self):
        """Lazy initialization of Neo4j client."""
        if self._client is None:
            try:
                from qatlas.knowledge.neo4j_client import Neo4jClient
                self._client = Neo4jClient(
                    uri=self._config.get("uri"),
                    username=self._config.get("user"),
                    password=self._config.get("password"),
                )
                self._client.connect()
                logger.info("Connected to Neo4j")
            except Exception as e:
                logger.error(f"Failed to connect to Neo4j: {e}")
                raise
        return self._client

    def sync_page(self, page: Any) -> Dict[str, Any]:
        """
        Sync a single wiki page to Neo4j.

        Args:
            page: WikiPage to sync

        Returns:
            Sync result with success status and details
        """
        result = {
            "page_id": page.frontmatter.id,
            "success": False,
            "neo4j_id": None,
            "node_type": None,
            "relationships_created": 0,
            "error": None,
        }

        try:
            page_type = page.frontmatter.type
            category = page.frontmatter.category

            if page_type == "entity":
                if category == "algorithm":
                    result = self._sync_algorithm(page)
                elif category == "primitive":
                    result = self._sync_primitive(page)
                elif category == "person":
                    result = self._sync_person(page)
                else:
                    result["error"] = f"Unknown entity category: {category}"

            elif page_type == "source":
                result = self._sync_paper(page)

            elif page_type == "concept":
                # Concepts don't sync to Neo4j (they're wiki-only)
                result["success"] = True
                result["error"] = "Concepts are wiki-only, not synced"

            else:
                result["error"] = f"Unknown page type: {page_type}"

            # Update page's sync status
            if result["success"]:
                page.frontmatter.neo4j_synced = True
                page.frontmatter.neo4j_id = result.get("neo4j_id")

        except Exception as e:
            result["error"] = str(e)
            logger.error(f"Sync failed for {page.frontmatter.id}: {e}")

        return result

    def sync_all(self, pages: List[Any]) -> Dict[str, Any]:
        """
        Sync all wiki pages to Neo4j.

        Syncs in dependency order: primitives, algorithms, papers.

        Args:
            pages: List of WikiPages to sync

        Returns:
            Aggregate sync results
        """
        results = {
            "total": len(pages),
            "synced": 0,
            "failed": 0,
            "skipped": 0,
            "details": [],
        }

        # Group pages by category
        primitives = []
        algorithms = []
        papers = []
        others = []

        for page in pages:
            page_type = page.frontmatter.type
            category = page.frontmatter.category

            if page_type == "entity" and category == "primitive":
                primitives.append(page)
            elif page_type == "entity" and category == "algorithm":
                algorithms.append(page)
            elif page_type == "source":
                papers.append(page)
            else:
                others.append(page)

        # Sync in order: primitives first (no dependencies)
        sync_order = [
            ("primitives", primitives),
            ("algorithms", algorithms),
            ("papers", papers),
        ]

        for group_name, group_pages in sync_order:
            logger.info(f"Syncing {group_name}: {len(group_pages)} pages")
            for page in group_pages:
                result = self.sync_page(page)
                results["details"].append(result)

                if result["success"]:
                    results["synced"] += 1
                elif result.get("error") and "wiki-only" in result.get("error", ""):
                    results["skipped"] += 1
                else:
                    results["failed"] += 1

        # Count others as skipped
        results["skipped"] += len(others)

        logger.info(
            f"Sync complete: {results['synced']} synced, "
            f"{results['failed']} failed, {results['skipped']} skipped"
        )

        return results

    def _sync_algorithm(self, page: Any) -> Dict[str, Any]:
        """Sync algorithm wiki page to Neo4j."""
        from qatlas.knowledge.models import Algorithm

        # Extract primitives from wiki links
        primitives = self._extract_primitive_links(page.content)

        # Extract other fields
        problem_type = self._extract_field(page.content, "Problem") or "unknown"
        description = page.get_summary()

        algorithm = Algorithm(
            id=self._to_neo4j_id(page.frontmatter.id, "algorithm"),
            name=page.frontmatter.title,
            description=description,
            problem_type=problem_type,
            primitives_used=[self._to_neo4j_id(p, "primitive") for p in primitives],
            tags=page.frontmatter.tags,
        )

        self.client.create_algorithm(algorithm)

        logger.info(f"Synced algorithm: {algorithm.id}")

        return {
            "page_id": page.frontmatter.id,
            "success": True,
            "neo4j_id": algorithm.id,
            "node_type": "Algorithm",
            "relationships_created": len(primitives),
        }

    def _sync_primitive(self, page: Any) -> Dict[str, Any]:
        """Sync primitive wiki page to Neo4j."""
        from qatlas.knowledge.models import Primitive

        description = page.get_summary()

        # Determine category from tags or default
        category = "transformation"
        for tag in page.frontmatter.tags:
            if tag in ("state_prep", "oracle", "transformation", "variational"):
                category = tag
                break

        primitive = Primitive(
            id=self._to_neo4j_id(page.frontmatter.id, "primitive"),
            name=page.frontmatter.title,
            description=description,
            category=category,
            tags=page.frontmatter.tags,
        )

        self.client.create_primitive(primitive)

        logger.info(f"Synced primitive: {primitive.id}")

        return {
            "page_id": page.frontmatter.id,
            "success": True,
            "neo4j_id": primitive.id,
            "node_type": "Primitive",
            "relationships_created": 0,
        }

    def _sync_paper(self, page: Any) -> Dict[str, Any]:
        """Sync paper wiki page to Neo4j."""
        from qatlas.knowledge.models import Paper

        # Extract arXiv ID from page ID
        page_id = page.frontmatter.id
        arxiv_id = page_id.replace("arxiv-", "").replace("paper-", "")

        # Extract authors from content
        authors = self._extract_authors(page.content)

        paper = Paper(
            id=page_id,
            title=page.frontmatter.title,
            arxiv_id=arxiv_id,
            authors=authors,
            abstract=self._extract_abstract(page.content),
        )

        self.client.create_paper(paper)

        # Link to algorithms
        algorithms = self._extract_algorithm_links(page.content)
        relationships = 0
        for algo_id in algorithms:
            try:
                neo4j_algo_id = self._to_neo4j_id(algo_id, "algorithm")
                self.client.link_paper_to_algorithm(page_id, neo4j_algo_id)
                relationships += 1
            except Exception as e:
                logger.warning(f"Failed to link paper to algorithm {algo_id}: {e}")

        logger.info(f"Synced paper: {paper.id}")

        return {
            "page_id": page.frontmatter.id,
            "success": True,
            "neo4j_id": paper.id,
            "node_type": "Paper",
            "relationships_created": relationships,
        }

    def _sync_person(self, page: Any) -> Dict[str, Any]:
        """Sync person wiki page to Neo4j.

        Note: Author nodes are not currently implemented in the Neo4j schema.
        This is a placeholder for future implementation.
        """
        # Authors are stored as strings on Paper nodes currently
        # Future: Create Author nodes and AUTHORED relationships
        logger.info(f"Person sync not implemented: {page.frontmatter.id}")

        return {
            "page_id": page.frontmatter.id,
            "success": True,
            "neo4j_id": None,
            "node_type": "Author",
            "relationships_created": 0,
            "error": "Author nodes not yet implemented in Neo4j schema",
        }

    def _extract_primitive_links(self, content: str) -> List[str]:
        """Extract primitive IDs from wiki links."""
        pattern = r'\[\[(prim-[^\]|]+)(?:\|[^\]]+)?\]\]'
        links = re.findall(pattern, content)
        return list(set(links))

    def _extract_algorithm_links(self, content: str) -> List[str]:
        """Extract algorithm IDs from wiki links."""
        pattern = r'\[\[(algo-[^\]|]+)(?:\|[^\]]+)?\]\]'
        links = re.findall(pattern, content)
        return list(set(links))

    def _extract_field(self, content: str, field_name: str) -> Optional[str]:
        """Extract a specific field from content."""
        pattern = rf'\*\*{re.escape(field_name)}\*\*:\s*(.+)'
        match = re.search(pattern, content)
        if match:
            return match.group(1).strip()
        return None

    def _extract_authors(self, content: str) -> List[str]:
        """Extract authors from paper content."""
        pattern = r'\*\*Authors\*\*:\s*(.+)'
        match = re.search(pattern, content)
        if match:
            authors_str = match.group(1).strip()
            return [a.strip() for a in authors_str.split(',')]
        return []

    def _extract_abstract(self, content: str) -> str:
        """Extract abstract section from content."""
        pattern = r'## Abstract\s*\n\n(.+?)(?=\n\n##|\Z)'
        match = re.search(pattern, content, re.DOTALL)
        if match:
            return match.group(1).strip()
        return ""

    def _to_neo4j_id(self, wiki_id: str, entity_type: str) -> str:
        """
        Convert wiki page ID to Neo4j node ID format.

        Wiki: prim-qft, algo-shors, paper-arxiv-9508027
        Neo4j: primitive_qft, shors_algorithm, paper_9508027

        Args:
            wiki_id: Wiki page ID
            entity_type: Type of entity (primitive, algorithm, paper)

        Returns:
            Neo4j-compatible ID
        """
        # Remove prefix
        id_part = wiki_id
        for prefix in ["prim-", "algo-", "arxiv-", "paper-"]:
            if id_part.startswith(prefix):
                id_part = id_part[len(prefix):]
                break

        # Convert format
        if entity_type == "primitive":
            return f"primitive_{id_part.replace('-', '_')}"
        elif entity_type == "algorithm":
            return f"{id_part.replace('-', '_')}_algorithm"
        elif entity_type == "paper":
            return f"paper_{id_part}"
        else:
            return wiki_id

    def check_sync_status(self, page_id: str) -> Dict[str, Any]:
        """
        Check if a wiki page is synced to Neo4j.

        Args:
            page_id: Wiki page ID to check

        Returns:
            Dict with sync status
        """
        # This would query Neo4j to check if node exists
        # Placeholder implementation
        return {
            "page_id": page_id,
            "synced": False,
            "neo4j_id": None,
        }
