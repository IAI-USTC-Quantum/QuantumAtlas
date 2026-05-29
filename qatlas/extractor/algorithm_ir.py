"""
Algorithm Intermediate Representation (IR) Schema

Defines the structured representation of quantum algorithms extracted from papers.
"""

from typing import Any, Dict, List, Optional
from datetime import datetime

import yaml
from pydantic import BaseModel, Field, field_validator


class Complexity(BaseModel):
    """Complexity metrics for quantum algorithms."""
    time: Optional[str] = Field(None, description="Time complexity (e.g., O((log N)^3))")
    space: Optional[str] = Field(None, description="Space complexity (e.g., O(log N))")
    query_complexity: Optional[str] = Field(None, description="Query complexity")
    gate_count: Optional[str] = Field(None, description="Total gate count (e.g., O(n^2))")
    circuit_depth: Optional[str] = Field(None, description="Circuit depth (e.g., O(n log n))")
    qubit_count: Optional[str] = Field(None, description="Number of qubits (e.g., n + O(1))")
    classical_equivalent: Optional[str] = Field(None, description="Classical algorithm complexity")


class AlgorithmIR(BaseModel):
    """
    Algorithm Intermediate Representation.
    
    This is the structured output of the extraction process, representing
    a quantum algorithm in a machine-readable format suitable for
    circuit design and code generation.
    """
    
    # Identification
    id: str = Field(..., description="Unique algorithm identifier (e.g., shor_factoring_1997)")
    name: str = Field(..., description="Algorithm name")
    description: Optional[str] = Field(None, description="Brief algorithm description")
    
    # Source information
    arxiv_id: Optional[str] = Field(None, description="arXiv paper ID")
    authors: List[str] = Field(default_factory=list, description="Paper authors")
    year: Optional[int] = Field(None, description="Publication year")
    venue: Optional[str] = Field(None, description="Publication venue (conference/journal)")
    doi: Optional[str] = Field(None, description="DOI if available")
    
    # Algorithm characteristics
    problem_type: str = Field(..., description="Type of problem solved (e.g., integer_factorization)")
    primitives: List[str] = Field(default_factory=list, description="List of primitive IDs used")
    complexity: Complexity = Field(default_factory=Complexity, description="Complexity metrics")
    
    # Algorithm specification
    pseudocode: Optional[str] = Field(None, description="Algorithm pseudocode")
    input_params: List[str] = Field(default_factory=list, description="Input parameters")
    output_params: List[str] = Field(default_factory=list, description="Output parameters")
    assumptions: List[str] = Field(default_factory=list, description="Algorithm assumptions")
    
    # Metadata
    created_at: str = Field(default_factory=lambda: datetime.now().isoformat())
    updated_at: Optional[str] = None
    version: str = Field(default="1.0.0", description="IR schema version")
    
    @field_validator('year')
    @classmethod
    def validate_year(cls, v):
        if v is not None and (v < 1900 or v > 2100):
            raise ValueError('Year must be between 1900 and 2100')
        return v
    
    def to_yaml(self) -> str:
        """Serialize to YAML format."""
        return yaml.dump(
            self.model_dump(exclude_none=True),
            default_flow_style=False,
            sort_keys=False,
            allow_unicode=True
        )
    
    @classmethod
    def from_yaml(cls, yaml_str: str) -> "AlgorithmIR":
        """Deserialize from YAML format."""
        data = yaml.safe_load(yaml_str)
        return cls(**data)
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary."""
        return self.model_dump(exclude_none=True)
    
    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "AlgorithmIR":
        """Create from dictionary."""
        return cls(**data)
    
    def to_neo4j_dict(self) -> Dict[str, Any]:
        """
        Convert to Neo4j-compatible dictionary.
        
        This format is suitable for creating Algorithm nodes in Neo4j.
        Lists and nested objects are preserved as JSON-compatible structures.
        """
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "arxiv_id": self.arxiv_id,
            "authors": self.authors,
            "year": self.year,
            "venue": self.venue,
            "doi": self.doi,
            "problem_type": self.problem_type,
            "primitives": self.primitives,
            "complexity_time": self.complexity.time,
            "complexity_space": self.complexity.space,
            "complexity_query": self.complexity.query_complexity,
            "complexity_gate_count": self.complexity.gate_count,
            "complexity_depth": self.complexity.circuit_depth,
            "complexity_qubits": self.complexity.qubit_count,
            "complexity_classical": self.complexity.classical_equivalent,
            "pseudocode": self.pseudocode,
            "input_params": self.input_params,
            "output_params": self.output_params,
            "assumptions": self.assumptions,
            "created_at": self.created_at,
            "updated_at": self.updated_at or datetime.now().isoformat(),
            "version": self.version,
        }
    
    @classmethod
    def from_extraction_results(
        cls,
        arxiv_id: str,
        metadata: Dict[str, Any],
        pseudocode: Dict[str, Any],
        complexity: Dict[str, Any],
        primitives: Dict[str, Any],
    ) -> "AlgorithmIR":
        """
        Create AlgorithmIR from extraction results.
        
        This is a convenience factory method that assembles the IR
        from the various extraction results.
        """
        # Generate ID from name and year
        algorithm_name = metadata.get("title", "unknown").lower().replace(" ", "_").replace("-", "_")
        year = metadata.get("year", "unknown")
        algorithm_id = f"{algorithm_name}_{year}"
        
        # Clean up the ID
        algorithm_id = "".join(c for c in algorithm_id if c.isalnum() or c == "_")
        
        return cls(
            id=algorithm_id,
            name=metadata.get("title", "Unknown Algorithm"),
            description=metadata.get("description"),
            arxiv_id=arxiv_id,
            authors=metadata.get("authors", []),
            year=metadata.get("year"),
            venue=metadata.get("venue"),
            doi=metadata.get("doi"),
            problem_type=metadata.get("problem_type", "unknown"),
            primitives=primitives.get("primitives", []),
            complexity=Complexity(
                time=complexity.get("time"),
                space=complexity.get("space"),
                query_complexity=complexity.get("query_complexity"),
                gate_count=complexity.get("gate_count"),
                circuit_depth=complexity.get("circuit_depth"),
                qubit_count=complexity.get("qubit_count"),
                classical_equivalent=complexity.get("classical_equivalent"),
            ),
            pseudocode=pseudocode.get("pseudocode"),
            input_params=pseudocode.get("input_params", []),
            output_params=pseudocode.get("output_params", []),
            assumptions=pseudocode.get("assumptions", []),
        )
