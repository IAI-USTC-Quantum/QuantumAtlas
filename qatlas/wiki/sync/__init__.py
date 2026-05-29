"""
Wiki Sync Module

Handles synchronization between Wiki pages and Neo4j knowledge graph.

Sync Rules:
- entity/algorithm → Algorithm node
- entity/primitive → Primitive node
- entity/people → Author node (future)
- source/paper → Paper node
- Wiki links → Relationships

The sync is one-way: Wiki is the source of truth for entity data,
Neo4j stores relationships and enables graph queries.
"""

from .neo4j_sync import Neo4jWikiSync

__all__ = ["Neo4jWikiSync"]
