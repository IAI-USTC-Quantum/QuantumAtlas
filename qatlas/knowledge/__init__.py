"""
Knowledge Graph Module

Data models for the quantum-algorithm knowledge graph. The graph itself is
server-side: the Go ``qatlasd`` owns the Neo4j connection and exposes
reads over ``/api/graph/*``. The Python client does not connect to Neo4j
directly, so only the plain Pydantic data models are exported here.
"""

from .models import Algorithm, Implementation, Paper, Primitive

__all__ = [
    "Algorithm",
    "Implementation",
    "Paper",
    "Primitive",
]
