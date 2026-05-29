"""
Relationship Type Definitions for Knowledge Graph

Defines the schema for all relationship types in the quantum algorithm knowledge graph.
"""

from enum import Enum
from typing import Optional, Dict, Any
from pydantic import BaseModel, ConfigDict, Field


class RelType(str, Enum):
    """Types of relationships in the knowledge graph."""
    DEPENDS_ON = "DEPENDS_ON"
    PUBLISHES = "PUBLISHES"
    IMPLEMENTED_AS = "IMPLEMENTED_AS"
    CITES = "CITES"
    USES_PRIMITIVE = "USES_PRIMITIVE"
    EXTENDS = "EXTENDS"


class BaseRelationship(BaseModel):
    """Base class for all knowledge graph relationships."""

    model_config = ConfigDict(extra="allow")

    rel_type: RelType
    properties: Dict[str, Any] = Field(default_factory=dict)


class DEPENDS_ON(BaseRelationship):
    """
    Algorithm depends on Primitive relationship.
    
    Indicates that an algorithm uses a particular primitive.
    """
    rel_type: RelType = RelType.DEPENDS_ON
    usage_context: Optional[str] = Field(None, description="How the primitive is used")
    complexity_contribution: Optional[str] = Field(None, description="Complexity contribution")


class PUBLISHES(BaseRelationship):
    """
    Paper publishes Algorithm relationship.
    
    Links a paper to the algorithms it introduces.
    """
    rel_type: RelType = RelType.PUBLISHES
    section: Optional[str] = Field(None, description="Section where algorithm is described")


class IMPLEMENTED_AS(BaseRelationship):
    """
    Algorithm implemented as Implementation relationship.
    
    Links an algorithm to its code implementation.
    """
    rel_type: RelType = RelType.IMPLEMENTED_AS
    fidelity: Optional[float] = Field(None, description="Implementation fidelity if applicable")
    notes: Optional[str] = Field(None, description="Implementation notes")


class CITES(BaseRelationship):
    """
    Paper cites Paper relationship.
    
    Represents citation relationships between papers.
    """
    rel_type: RelType = RelType.CITES
    context: Optional[str] = Field(None, description="Citation context")


class USES_PRIMITIVE(BaseRelationship):
    """
    Algorithm uses Primitive relationship (alternative to DEPENDS_ON).
    
    Indicates primitive usage with specific parameters.
    """
    rel_type: RelType = RelType.USES_PRIMITIVE
    parameters: Optional[Dict[str, Any]] = Field(None, description="Primitive parameters")
    count: Optional[int] = Field(None, description="Number of uses")


class EXTENDS(BaseRelationship):
    """
    Algorithm extends Algorithm relationship.
    
    Indicates algorithm improvements or variations.
    """
    rel_type: RelType = RelType.EXTENDS
    extension_type: Optional[str] = Field(None, description="Type of extension")
    improvement: Optional[str] = Field(None, description="What is improved")
