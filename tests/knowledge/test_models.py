"""Tests for knowledge graph models."""

import pytest
from qatlas.knowledge.models import Primitive, Algorithm, Paper, Implementation


class TestPrimitive:
    """Test cases for Primitive model."""
    
    def test_primitive_creation(self):
        """Test creating a Primitive."""
        primitive = Primitive(
            id="primitive_qft",
            name="Quantum Fourier Transform",
            category="transformation",
            complexity={"gate_count": "O(n^2)", "depth": "O(n)"},
            references=["arxiv:9508027"],
            tags=["fourier", "fundamental"],
        )
        
        assert primitive.id == "primitive_qft"
        assert primitive.name == "Quantum Fourier Transform"
        assert primitive.category == "transformation"
        assert primitive.complexity["gate_count"] == "O(n^2)"
    
    def test_primitive_to_neo4j_dict(self):
        """Test converting Primitive to Neo4j dictionary."""
        primitive = Primitive(
            id="primitive_test",
            name="Test Primitive",
            category="test",
        )
        
        data = primitive.to_neo4j_dict()
        
        assert data["id"] == "primitive_test"
        assert data["name"] == "Test Primitive"
        assert data["category"] == "test"
        assert "created_at" in data


class TestAlgorithm:
    """Test cases for Algorithm model."""
    
    def test_algorithm_creation(self):
        """Test creating an Algorithm."""
        algorithm = Algorithm(
            id="alg_shor",
            name="Shor's Algorithm",
            problem_type="integer_factorization",
            year=1994,
            primitives_used=["primitive_qft", "primitive_qpe"],
        )
        
        assert algorithm.id == "alg_shor"
        assert algorithm.name == "Shor's Algorithm"
        assert "primitive_qft" in algorithm.primitives_used


class TestPaper:
    """Test cases for Paper model."""
    
    def test_paper_creation(self):
        """Test creating a Paper."""
        paper = Paper(
            id="paper_9508027",
            title="Polynomial-Time Algorithms for Prime Factorization",
            arxiv_id="9508027",
            authors=["Peter W. Shor"],
            year=1995,
        )
        
        assert paper.id == "paper_9508027"
        assert paper.arxiv_id == "9508027"
        assert paper.authors == ["Peter W. Shor"]


class TestImplementation:
    """Test cases for Implementation model."""
    
    def test_implementation_creation(self):
        """Test creating an Implementation."""
        impl = Implementation(
            id="impl_shor_qpanda",
            name="Shor's Algorithm in QPanda",
            algorithm_id="alg_shor",
            language="Python/QPanda",
            verified=False,
        )
        
        assert impl.id == "impl_shor_qpanda"
        assert impl.algorithm_id == "alg_shor"
        assert impl.verified is False


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
