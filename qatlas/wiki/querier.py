"""
Wiki Querier

Handles query operations on wiki pages.

Supports:
- Keyword search
- Tag-based filtering
- Link traversal (forward and backward)
- Section extraction
"""

import logging
import re
from typing import Any, Dict, List, Optional, Set

logger = logging.getLogger(__name__)


class WikiQuerier:
    """
    Query engine for wiki pages.

    Provides search and navigation functionality for the wiki.

    Usage:
        engine = WikiEngine()
        results = engine.querier.search("quantum fourier")
        links = engine.querier.get_linked_pages("prim-qft")
        backlinks = engine.querier.get_backlinks("prim-qft")
    """

    def __init__(self, wiki_engine):
        """
        Initialize querier.

        Args:
            wiki_engine: Parent WikiEngine instance
        """
        self.engine = wiki_engine

    def search(
        self,
        query: str,
        page_types: Optional[List[str]] = None,
        tags: Optional[List[str]] = None,
        max_results: int = 10,
    ) -> List[Dict[str, Any]]:
        """
        Search wiki pages.

        Uses a relevance scoring system based on:
        - Title matches (highest weight)
        - Tag matches
        - Content frequency
        - ID matches

        Args:
            query: Search query string
            page_types: Filter by page types
            tags: Filter by tags
            max_results: Maximum results to return

        Returns:
            List of results with page info and relevance scores
        """
        results = []
        query_lower = query.lower()
        query_terms = set(query_lower.split())

        pages = self.engine.list_pages(page_type=page_types[0] if page_types and len(page_types) == 1 else None)

        for page in pages:
            # Apply tag filter
            if tags and not any(t in page.frontmatter.tags for t in tags):
                continue

            # Apply type filter for multiple types
            if page_types and len(page_types) > 1 and page.frontmatter.type not in page_types:
                continue

            # Calculate relevance score
            score = self._calculate_relevance(page, query_terms, query_lower)

            if score > 0:
                results.append({
                    "id": page.frontmatter.id,
                    "title": page.frontmatter.title,
                    "type": page.frontmatter.type,
                    "category": page.frontmatter.category,
                    "score": score,
                    "snippet": page.get_summary(200),
                    "tags": page.frontmatter.tags,
                    "status": page.frontmatter.status,
                })

        # Sort by score descending
        results.sort(key=lambda x: x["score"], reverse=True)

        return results[:max_results]

    def get_linked_pages(self, page_id: str) -> List[Dict[str, Any]]:
        """
        Get all pages linked from a given page.

        Wiki links are in format [[page-id]] or [[page-id|display text]].

        Args:
            page_id: Source page ID

        Returns:
            List of linked page info
        """
        page = self.engine.get_page(page_id)
        if page is None:
            return []

        # Extract [[links]] from content
        link_ids = page.extract_links()

        linked_pages = []
        for link_id in link_ids:
            linked = self.engine.get_page(link_id)
            if linked:
                linked_pages.append({
                    "id": linked.frontmatter.id,
                    "title": linked.frontmatter.title,
                    "type": linked.frontmatter.type,
                    "category": linked.frontmatter.category,
                })

        return linked_pages

    def get_backlinks(self, page_id: str) -> List[Dict[str, Any]]:
        """
        Get all pages that link to a given page.

        Args:
            page_id: Target page ID

        Returns:
            List of pages that link to this page
        """
        backlinks = []

        # Escape special regex characters in page_id
        escaped_id = re.escape(page_id)
        link_pattern = rf'\[\[{escaped_id}(?:\|[^\]]+)?\]\]'

        for page in self.engine.list_pages():
            if re.search(link_pattern, page.content):
                backlinks.append({
                    "id": page.frontmatter.id,
                    "title": page.frontmatter.title,
                    "type": page.frontmatter.type,
                    "category": page.frontmatter.category,
                })

        return backlinks

    def get_page_graph(self, page_id: str, depth: int = 2) -> Dict[str, Any]:
        """
        Get a subgraph centered on a page.

        Args:
            page_id: Center page ID
            depth: How many levels of links to follow

        Returns:
            Dict with 'nodes' and 'edges' for visualization
        """
        nodes = []
        edges = []
        visited: Set[str] = set()

        def collect_graph(pid: str, current_depth: int):
            if current_depth > depth or pid in visited:
                return

            visited.add(pid)
            page = self.engine.get_page(pid)
            if page is None:
                return

            # Add node
            nodes.append({
                "id": pid,
                "title": page.frontmatter.title,
                "type": page.frontmatter.type,
                "category": page.frontmatter.category,
            })

            # Get linked pages
            for linked in self.get_linked_pages(pid):
                edges.append({
                    "source": pid,
                    "target": linked["id"],
                })
                collect_graph(linked["id"], current_depth + 1)

        collect_graph(page_id, 1)

        return {
            "center": page_id,
            "nodes": nodes,
            "edges": edges,
        }

    def find_by_tag(self, tag: str) -> List[Dict[str, Any]]:
        """
        Find all pages with a specific tag.

        Args:
            tag: Tag to search for

        Returns:
            List of matching pages
        """
        pages = self.engine.list_pages(tags=[tag])
        return [
            {
                "id": p.frontmatter.id,
                "title": p.frontmatter.title,
                "type": p.frontmatter.type,
                "tags": p.frontmatter.tags,
            }
            for p in pages
        ]

    def find_by_type(self, page_type: str, category: Optional[str] = None) -> List[Dict[str, Any]]:
        """
        Find all pages of a specific type.

        Args:
            page_type: Page type to filter by
            category: Optional category filter

        Returns:
            List of matching pages
        """
        pages = self.engine.list_pages(page_type=page_type, category=category)
        return [
            {
                "id": p.frontmatter.id,
                "title": p.frontmatter.title,
                "category": p.frontmatter.category,
                "status": p.frontmatter.status,
            }
            for p in pages
        ]

    def _calculate_relevance(
        self,
        page: Any,
        query_terms: Set[str],
        query_lower: str,
    ) -> float:
        """
        Calculate relevance score for a page.

        Scoring:
        - Title exact match: +10.0
        - Title term match: +5.0 per term
        - ID match: +5.0
        - Tag match: +3.0 per tag
        - Content occurrence: +1.0 per occurrence

        Args:
            page: WikiPage to score
            query_terms: Set of query terms
            query_lower: Lowercase query string

        Returns:
            Relevance score
        """
        score = 0.0

        # Title match (highest weight)
        title_lower = page.frontmatter.title.lower()
        if query_lower in title_lower:
            score += 10.0
        else:
            title_terms = set(title_lower.split())
            score += 5.0 * len(query_terms & title_terms)

        # ID match
        if query_lower in page.frontmatter.id.lower():
            score += 5.0

        # Tag match
        tag_terms = set(t.lower() for t in page.frontmatter.tags)
        score += 3.0 * len(query_terms & tag_terms)

        # Content match (frequency)
        content_lower = page.content.lower()
        for term in query_terms:
            count = content_lower.count(term)
            score += min(count, 5) * 1.0  # Cap at 5 occurrences

        return score

    def get_recent_pages(self, limit: int = 10) -> List[Dict[str, Any]]:
        """
        Get recently created pages.

        Args:
            limit: Maximum number of pages

        Returns:
            List of recent pages sorted by creation date
        """
        pages = self.engine.list_pages()

        # Sort by creation date descending
        pages.sort(key=lambda p: p.frontmatter.created_at, reverse=True)

        return [
            {
                "id": p.frontmatter.id,
                "title": p.frontmatter.title,
                "type": p.frontmatter.type,
                "created_at": p.frontmatter.created_at.isoformat(),
            }
            for p in pages[:limit]
        ]

    def get_pages_needing_review(self) -> List[Dict[str, Any]]:
        """
        Get pages that are in draft or review status.

        Returns:
            List of pages needing review
        """
        pages = self.engine.list_pages(status="draft")
        pages.extend(self.engine.list_pages(status="review"))

        return [
            {
                "id": p.frontmatter.id,
                "title": p.frontmatter.title,
                "status": p.frontmatter.status,
                "created_at": p.frontmatter.created_at.isoformat(),
            }
            for p in pages
        ]
