"""Tests for AlgorithmIR schema."""

import pytest
from atlas.extractor.algorithm_ir import AlgorithmIR, Complexity


class TestComplexity:
    """Test cases for Complexity model."""
    
    def test_complexity_creation(self):
        """Test creating Complexity instance."""
        complexity = Complexity(
            time="O((log N)^3)",
            space="O(log N)",
            gate_count="O(n^2)",
        )
        
        assert complexity.time == "O((log N)^3)"
        assert complexity.space == "O(log N)"
        assert complexity.gate_count == "O(n^2)"
    
    def test_complexity_defaults(self):
        """Test Complexity default values."""
        complexity = Complexity()
        
        assert complexity.time is None
        assert complexity.space is None
        assert complexity.gate_count is None


class TestAlgorithmIR:
    """Test cases for AlgorithmIR model."""
    
    def test_algorithm_ir_creation(self):
        """Test creating AlgorithmIR instance."""
        algorithm = AlgorithmIR(
            id="shor_factoring_1997",
            name="Shor's Factoring Algorithm",
            problem_type="integer_factorization",
            arxiv_id="9508027",
            authors=["Peter W. Shor"],
            year=1997,
        )
        
        assert algorithm.id == "shor_factoring_1997"
        assert algorithm.name == "Shor's Factoring Algorithm"
        assert algorithm.problem_type == "integer_factorization"
        assert algorithm.arxiv_id == "9508027"
        assert algorithm.authors == ["Peter W. Shor"]
        assert algorithm.year == 1997
    
    def test_algorithm_ir_with_complexity(self):
        """Test AlgorithmIR with complexity."""
        algorithm = AlgorithmIR(
            id="test_algorithm",
            name="Test Algorithm",
            problem_type="test",
            complexity=Complexity(
                time="O(n)",
                space="O(1)",
            ),
        )
        
        assert algorithm.complexity.time == "O(n)"
        assert algorithm.complexity.space == "O(1)"
    
    def test_algorithm_ir_year_validation(self):
        """Test year validation."""
        with pytest.raises(ValueError):
            AlgorithmIR(
                id="test",
                name="Test",
                problem_type="test",
                year=1800,  # Too old
            )
        
        with pytest.raises(ValueError):
            AlgorithmIR(
                id="test",
                name="Test",
                problem_type="test",
                year=2200,  # Too far in future
            )
    
    def test_to_from_yaml(self):
        """Test YAML serialization."""
        original = AlgorithmIR(
            id="grover_search_1996",
            name="Grover's Search Algorithm",
            problem_type="search",
            arxiv_id="9704012",
            authors=["Lov K. Grover"],
            year=1996,
            complexity=Complexity(
                time="O(√N)",
                space="O(log N)",
            ),
            primitives=["primitive_amplitude_amplification"],
        )
        
        yaml_str = original.to_yaml()
        restored = AlgorithmIR.from_yaml(yaml_str)
        
        assert restored.id == original.id
        assert restored.name == original.name
        assert restored.year == original.year
        assert restored.primitives == original.primitives
    
    def test_to_neo4j_dict(self):
        """Test Neo4j dictionary conversion."""
        algorithm = AlgorithmIR(
            id="test",
            name="Test Algorithm",
            problem_type="test",
            complexity=Complexity(
                time="O(n)",
                gate_count="O(n^2)",
            ),
        )
        
        neo4j_dict = algorithm.to_neo4j_dict()
        
        assert neo4j_dict["id"] == "test"
        assert neo4j_dict["name"] == "Test Algorithm"
        assert neo4j_dict["problem_type"] == "test"
        assert neo4j_dict["complexity_time"] == "O(n)"
        assert neo4j_dict["complexity_gate_count"] == "O(n^2)"
    
    def test_from_extraction_results(self):
        """Test factory method from extraction results."""
        metadata = {
            "title": "Test Algorithm",
            "authors": ["Author 1", "Author 2"],
            "year": 2024,
            "problem_type": "optimization",
        }
        
        pseudocode = {
            "pseudocode": "Step 1... Step 2...",
            "input_params": ["n"],
            "output_params": ["result"],
            "assumptions": ["n > 0"],
        }
        
        complexity = {
            "time": "O(n^2)",
            "space": "O(n)",
        }
        
        primitives = {
            "primitives": ["primitive_qft"],
        }
        
        algorithm = AlgorithmIR.from_extraction_results(
            arxiv_id="2401.12345",
            metadata=metadata,
            pseudocode=pseudocode,
            complexity=complexity,
            primitives=primitives,
        )
        
        assert algorithm.name == "Test Algorithm"
        assert algorithm.authors == ["Author 1", "Author 2"]
        assert algorithm.year == 2024
        assert algorithm.problem_type == "optimization"
        assert algorithm.pseudocode == "Step 1... Step 2..."
        assert algorithm.primitives == ["primitive_qft"]


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
