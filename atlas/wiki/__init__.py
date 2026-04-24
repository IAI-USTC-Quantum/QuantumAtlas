"""
Wiki Module

Manages the layered knowledge base system with Wiki pages and Neo4j sync.

Architecture:
- Layer 1 (Raw): Immutable source documents
- Layer 2 (Wiki): Structured knowledge pages
- Layer 3 (Graph): Neo4j knowledge graph

Core Components:
- WikiEngine: Main orchestrator for wiki operations
- WikiPage: Pydantic model for wiki pages with frontmatter
- WikiIngester: Handles paper ingestion into wiki
- WikiQuerier: Search and query functionality
- WikiLinter: Health checks and validation
- Neo4jWikiSync: Wiki to Neo4j synchronization
"""

from .page import WikiPage, WikiFrontmatter
from .templates import PageTemplate

__all__ = [
    "WikiPage",
    "WikiFrontmatter",
    "PageTemplate",
]
