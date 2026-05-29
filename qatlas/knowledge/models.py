"""
Data Models for Knowledge Graph

Pydantic models for type-safe interaction with Neo4j.
"""

from typing import List, Optional, Dict, Any
from datetime import datetime
from pydantic import BaseModel, Field


class Primitive(BaseModel):
    """Quantum primitive model."""
    id: str
    name: str
    description: Optional[str] = None
    category: str
    complexity: Optional[Dict[str, Any]] = None
    references: List[str] = Field(default_factory=list)
    tags: List[str] = Field(default_factory=list)
    definition: Optional[str] = None
    prerequisites: List[str] = Field(default_factory=list)
    
    def to_neo4j_dict(self) -> Dict[str, Any]:
        """Convert to Neo4j-compatible dictionary."""
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "category": self.category,
            "complexity": self.complexity,
            "references": self.references,
            "tags": self.tags,
            "definition": self.definition,
            "prerequisites": self.prerequisites,
            "created_at": datetime.now().isoformat(),
        }


class Algorithm(BaseModel):
    """Quantum algorithm model."""
    id: str
    name: str
    description: Optional[str] = None
    problem_type: str
    complexity: Optional[Dict[str, Any]] = None
    primitives_used: List[str] = Field(default_factory=list)
    paper_id: Optional[str] = None
    year: Optional[int] = None
    tags: List[str] = Field(default_factory=list)
    
    def to_neo4j_dict(self) -> Dict[str, Any]:
        """Convert to Neo4j-compatible dictionary."""
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "problem_type": self.problem_type,
            "complexity": self.complexity,
            "primitives_used": self.primitives_used,
            "paper_id": self.paper_id,
            "year": self.year,
            "tags": self.tags,
            "created_at": datetime.now().isoformat(),
        }


class Paper(BaseModel):
    """Research paper model."""
    id: str
    title: str
    arxiv_id: Optional[str] = None
    doi: Optional[str] = None
    authors: List[str] = Field(default_factory=list)
    year: Optional[int] = None
    abstract: Optional[str] = None
    pdf_url: Optional[str] = None
    sections: Optional[Dict[str, str]] = None
    citations: List[str] = Field(default_factory=list)
    
    def to_neo4j_dict(self) -> Dict[str, Any]:
        """Convert to Neo4j-compatible dictionary."""
        return {
            "id": self.id,
            "title": self.title,
            "arxiv_id": self.arxiv_id,
            "doi": self.doi,
            "authors": self.authors,
            "year": self.year,
            "abstract": self.abstract,
            "pdf_url": self.pdf_url,
            "created_at": datetime.now().isoformat(),
        }


class Implementation(BaseModel):
    """Code implementation model."""
    id: str
    name: str
    description: Optional[str] = None
    algorithm_id: str
    language: str
    code: Optional[str] = None
    repository_url: Optional[str] = None
    verified: bool = False
    
    def to_neo4j_dict(self) -> Dict[str, Any]:
        """Convert to Neo4j-compatible dictionary."""
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "algorithm_id": self.algorithm_id,
            "language": self.language,
            "code": self.code,
            "repository_url": self.repository_url,
            "verified": self.verified,
            "created_at": datetime.now().isoformat(),
        }


# Type mapping for Neo4j node creation
NODE_TYPE_MAP = {
    "Primitive": Primitive,
    "Algorithm": Algorithm,
    "Paper": Paper,
    "Implementation": Implementation,
}
