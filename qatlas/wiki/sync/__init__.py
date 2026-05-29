"""
Wiki Sync Module

Wiki → knowledge-graph synchronization now runs server-side. The Go
``qatlas-server`` owns the Neo4j connection and derives the graph from the
canonical Wiki (the source of truth). The Python client no longer connects to
Neo4j directly, so this package intentionally exposes no client-side sync.
"""

__all__: list[str] = []

