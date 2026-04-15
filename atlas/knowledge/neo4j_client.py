"""
Neo4j Client Module

Provides connection management and CRUD operations for the knowledge graph.
"""

import os
from typing import List, Optional, Dict, Any, TypeVar, Type
from contextlib import contextmanager

try:
    from neo4j import GraphDatabase, Driver, Session, Result
    from neo4j.exceptions import Neo4jError
except ImportError:
    raise ImportError(
        "neo4j-python-driver is required. Install with: pip install neo4j-python-driver"
    )

from .models import Primitive, Algorithm, Paper, Implementation, NODE_TYPE_MAP

T = TypeVar("T", Primitive, Algorithm, Paper, Implementation)


class Neo4jClient:
    """
    Neo4j database client for knowledge graph operations.
    
    Usage:
        client = Neo4jClient()
        client.connect()
        primitive = client.create_primitive(Primitive(...))
        client.close()
    """
    
    def __init__(
        self,
        uri: Optional[str] = None,
        username: Optional[str] = None,
        password: Optional[str] = None,
    ):
        """
        Initialize Neo4j client.
        
        Args:
            uri: Neo4j Bolt URI (default: from NEO4J_URI env var or bolt://localhost:7687)
            username: Neo4j username (default: from NEO4J_USER env var or neo4j)
            password: Neo4j password (default: from NEO4J_PASSWORD env var)
        """
        self.uri = uri or os.getenv("NEO4J_URI", "bolt://localhost:7687")
        self.username = username or os.getenv("NEO4J_USER", "neo4j")
        self.password = password or os.getenv("NEO4J_PASSWORD", "quantum-atlas")
        self._driver: Optional[Driver] = None
    
    def connect(self) -> "Neo4jClient":
        """Establish connection to Neo4j."""
        self._driver = GraphDatabase.driver(
            self.uri, auth=(self.username, self.password)
        )
        # Verify connection
        self._driver.verify_connectivity()
        return self
    
    def close(self) -> None:
        """Close Neo4j connection."""
        if self._driver:
            self._driver.close()
            self._driver = None
    
    def is_connected(self) -> bool:
        """Check if connection is active."""
        return self._driver is not None
    
    @contextmanager
    def session(self):
        """Context manager for Neo4j sessions."""
        if not self._driver:
            raise RuntimeError("Not connected to Neo4j. Call connect() first.")
        session = self._driver.session()
        try:
            yield session
        finally:
            session.close()
    
    def test_connection(self) -> bool:
        """Test if Neo4j connection is working."""
        try:
            with self.session() as session:
                result = session.run("RETURN 1 as test")
                return result.single()["test"] == 1
        except Exception:
            return False
    
    # === Node Creation Methods ===
    
    def create_primitive(self, primitive: Primitive) -> Primitive:
        """Create a Primitive node in the graph."""
        with self.session() as session:
            query = """
            MERGE (p:Primitive {id: $id})
            SET p = $props
            RETURN p
            """
            props = primitive.to_neo4j_dict()
            result = session.run(query, id=primitive.id, props=props)
            result.single()
            return primitive
    
    def create_algorithm(self, algorithm: Algorithm) -> Algorithm:
        """Create an Algorithm node in the graph."""
        with self.session() as session:
            query = """
            MERGE (a:Algorithm {id: $id})
            SET a = $props
            RETURN a
            """
            props = algorithm.to_neo4j_dict()
            result = session.run(query, id=algorithm.id, props=props)
            result.single()
            
            # Create DEPENDS_ON relationships to primitives
            for primitive_id in algorithm.primitives_used:
                self._create_primitive_dependency(session, algorithm.id, primitive_id)
            
            return algorithm
    
    def _create_primitive_dependency(self, session: Session, algorithm_id: str, primitive_id: str) -> None:
        """Create DEPENDS_ON relationship from algorithm to primitive."""
        query = """
        MATCH (a:Algorithm {id: $algorithm_id})
        MATCH (p:Primitive {id: $primitive_id})
        MERGE (a)-[:DEPENDS_ON]->(p)
        """
        session.run(query, algorithm_id=algorithm_id, primitive_id=primitive_id)
    
    def create_paper(self, paper: Paper) -> Paper:
        """Create a Paper node in the graph."""
        with self.session() as session:
            query = """
            MERGE (p:Paper {id: $id})
            SET p = $props
            RETURN p
            """
            props = paper.to_neo4j_dict()
            result = session.run(query, id=paper.id, props=props)
            result.single()
            return paper
    
    def create_implementation(self, implementation: Implementation) -> Implementation:
        """Create an Implementation node in the graph."""
        with self.session() as session:
            query = """
            MERGE (i:Implementation {id: $id})
            SET i = $props
            RETURN i
            """
            props = implementation.to_neo4j_dict()
            result = session.run(query, id=implementation.id, props=props)
            result.single()
            
            # Create IMPLEMENTED_AS relationship to algorithm
            self._create_implementation_link(session, implementation.id, implementation.algorithm_id)
            
            return implementation
    
    def _create_implementation_link(self, session: Session, impl_id: str, algorithm_id: str) -> None:
        """Create IMPLEMENTED_AS relationship from implementation to algorithm."""
        query = """
        MATCH (i:Implementation {id: $impl_id})
        MATCH (a:Algorithm {id: $algorithm_id})
        MERGE (a)-[:IMPLEMENTED_AS]->(i)
        """
        session.run(query, impl_id=impl_id, algorithm_id=algorithm_id)
    
    # === Query Methods ===
    
    def get_primitive(self, primitive_id: str) -> Optional[Primitive]:
        """Get a primitive by ID."""
        with self.session() as session:
            query = "MATCH (p:Primitive {id: $id}) RETURN p"
            result = session.run(query, id=primitive_id)
            record = result.single()
            if record:
                return Primitive(**record["p"])
            return None
    
    def get_all_primitives(self) -> List[Primitive]:
        """Get all primitives."""
        with self.session() as session:
            query = "MATCH (p:Primitive) RETURN p"
            result = session.run(query)
            return [Primitive(**record["p"]) for record in result]
    
    def get_algorithm(self, algorithm_id: str) -> Optional[Algorithm]:
        """Get an algorithm by ID."""
        with self.session() as session:
            query = "MATCH (a:Algorithm {id: $id}) RETURN a"
            result = session.run(query, id=algorithm_id)
            record = result.single()
            if record:
                return Algorithm(**record["a"])
            return None
    
    def get_paper(self, paper_id: str) -> Optional[Paper]:
        """Get a paper by ID."""
        with self.session() as session:
            query = "MATCH (p:Paper {id: $id}) RETURN p"
            result = session.run(query, id=paper_id)
            record = result.single()
            if record:
                return Paper(**record["p"])
            return None
    
    # === Relationship Methods ===
    
    def link_paper_to_algorithm(self, paper_id: str, algorithm_id: str) -> None:
        """Create PUBLISHES relationship from paper to algorithm."""
        with self.session() as session:
            query = """
            MATCH (p:Paper {id: $paper_id})
            MATCH (a:Algorithm {id: $algorithm_id})
            MERGE (p)-[:PUBLISHES]->(a)
            """
            session.run(query, paper_id=paper_id, algorithm_id=algorithm_id)
    
    def link_paper_citation(self, from_paper_id: str, to_paper_id: str) -> None:
        """Create CITES relationship from one paper to another."""
        with self.session() as session:
            query = """
            MATCH (p1:Paper {id: $from_id})
            MATCH (p2:Paper {id: $to_id})
            MERGE (p1)-[:CITES]->(p2)
            """
            session.run(query, from_id=from_paper_id, to_id=to_paper_id)
    
    def get_algorithm_primitives(self, algorithm_id: str) -> List[Primitive]:
        """Get all primitives that an algorithm depends on."""
        with self.session() as session:
            query = """
            MATCH (a:Algorithm {id: $id})-[:DEPENDS_ON]->(p:Primitive)
            RETURN p
            """
            result = session.run(query, id=algorithm_id)
            return [Primitive(**record["p"]) for record in result]
    
    # === Utility Methods ===
    
    def clear_database(self) -> None:
        """WARNING: Delete all nodes and relationships. Use with caution."""
        with self.session() as session:
            session.run("MATCH (n) DETACH DELETE n")
    
    def get_stats(self) -> Dict[str, int]:
        """Get database statistics."""
        with self.session() as session:
            stats = {}
            for label in ["Primitive", "Algorithm", "Paper", "Implementation"]:
                result = session.run(f"MATCH (n:{label}) RETURN count(n) as count")
                stats[label] = result.single()["count"]
            return stats
