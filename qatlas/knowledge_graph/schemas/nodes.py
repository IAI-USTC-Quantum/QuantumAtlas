"""
Node Type Definitions for Knowledge Graph

Defines the schema for all node types in the quantum algorithm knowledge graph.
"""

from enum import Enum
from typing import List, Optional, Dict, Any
from pydantic import BaseModel, ConfigDict, Field


class NodeType(str, Enum):
    """Types of nodes in the knowledge graph."""
    PRIMITIVE = "Primitive"
    ALGORITHM = "Algorithm"
    PAPER = "Paper"
    IMPLEMENTATION = "Implementation"


class BaseNode(BaseModel):
    """Base class for all knowledge graph nodes."""

    model_config = ConfigDict(extra="allow")

    id: str = Field(..., description="Unique identifier")
    name: str = Field(..., description="Display name")
    description: Optional[str] = Field(None, description="Brief description")
    created_at: Optional[str] = Field(None, description="Creation timestamp")


class PrimitiveNode(BaseNode):
    """
    Primitive quantum building block node.
    
    Examples: QFT, QPE, Block Encoding, Amplitude Amplification, etc.
    """
    node_type: NodeType = NodeType.PRIMITIVE
    category: str = Field(..., description="Category: state_prep, oracle, transformation, etc.")
    complexity: Optional[Dict[str, Any]] = Field(None, description="Gate/depth/qubit complexity")
    references: List[str] = Field(default_factory=list, description="Reference paper IDs")
    tags: List[str] = Field(default_factory=list, description="Tags for classification")


class AlgorithmNode(BaseNode):
    """
    Quantum algorithm node.
    
    Represents a complete quantum algorithm like Shor's, Grover's, etc.
    """
    node_type: NodeType = NodeType.ALGORITHM
    problem_type: str = Field(..., description="Type of problem solved")
    complexity: Optional[Dict[str, Any]] = Field(None, description="Time/space complexity")
    primitives_used: List[str] = Field(default_factory=list, description="Primitive IDs used")
    paper_id: Optional[str] = Field(None, description="Source paper ID")
    year: Optional[int] = Field(None, description="Publication year")
    tags: List[str] = Field(default_factory=list, description="Tags for classification")


class PaperNode(BaseNode):
    """
    Research paper node.
    
    Represents an arXiv paper or other publication.
    """
    node_type: NodeType = NodeType.PAPER
    arxiv_id: Optional[str] = Field(None, description="arXiv identifier")
    doi: Optional[str] = Field(None, description="DOI if available")
    authors: List[str] = Field(default_factory=list, description="List of authors")
    year: Optional[int] = Field(None, description="Publication year")
    abstract: Optional[str] = Field(None, description="Paper abstract")
    pdf_url: Optional[str] = Field(None, description="URL to PDF")
    sections: Optional[Dict[str, str]] = Field(None, description="Parsed sections")
    citations: List[str] = Field(default_factory=list, description="Cited paper IDs")


class ImplementationNode(BaseNode):
    """
    Code implementation node.
    
    Links algorithms to executable code.
    """
    node_type: NodeType = NodeType.IMPLEMENTATION
    algorithm_id: str = Field(..., description="Implemented algorithm ID")
    language: str = Field(..., description="Programming language/framework")
    code: Optional[str] = Field(None, description="Source code or path")
    repository_url: Optional[str] = Field(None, description="Repository URL")
    verified: bool = Field(False, description="Whether implementation is verified")
