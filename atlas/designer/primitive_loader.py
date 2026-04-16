"""
Primitive Loader Module

Loads primitive definitions from YAML files and provides caching.
"""

import os
import yaml
from typing import Any, Dict, List, Optional
from pathlib import Path
from dataclasses import dataclass, field


@dataclass
class PrimitiveDefinition:
    """
    Represents a quantum primitive definition.
    
    Attributes:
        id: Unique primitive identifier
        name: Human-readable name
        description: Description of the primitive
        category: Category (transformation, oracle, state_preparation, etc.)
        complexity: Complexity metrics
        gate_sequence: Sequence of gates implementing the primitive
        input_qubits: Number of input qubits
        output_qubits: Number of output qubits
        parameters: Parameter definitions for parameterized primitives
        references: Reference papers
        tags: Tags for categorization
    """
    id: str
    name: str
    description: str = ""
    category: str = ""
    complexity: Dict[str, Any] = field(default_factory=dict)
    gate_sequence: List[Dict[str, Any]] = field(default_factory=list)
    input_qubits: int = 0
    output_qubits: int = 0
    parameters: Dict[str, Any] = field(default_factory=dict)
    references: List[str] = field(default_factory=list)
    tags: List[str] = field(default_factory=list)
    
    @classmethod
    def from_yaml(cls, yaml_path: str) -> "PrimitiveDefinition":
        """Load primitive definition from YAML file."""
        with open(yaml_path, 'r', encoding='utf-8') as f:
            data = yaml.safe_load(f)
        
        # Extract gate sequence if present
        gate_sequence = data.get('gate_sequence', [])
        
        # Determine qubit counts
        input_qubits = data.get('input_qubits', 0)
        output_qubits = data.get('output_qubits', input_qubits)
        
        # If not specified, try to infer from complexity
        if input_qubits == 0 and 'complexity' in data:
            qubits_str = data['complexity'].get('qubits', '')
            if isinstance(qubits_str, str) and qubits_str.isdigit():
                input_qubits = int(qubits_str)
                output_qubits = input_qubits
        
        return cls(
            id=data.get('id', ''),
            name=data.get('name', ''),
            description=data.get('description', ''),
            category=data.get('category', ''),
            complexity=data.get('complexity', {}),
            gate_sequence=gate_sequence,
            input_qubits=input_qubits,
            output_qubits=output_qubits,
            parameters=data.get('parameters', {}),
            references=data.get('references', []),
            tags=data.get('tags', []),
        )
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary."""
        return {
            "id": self.id,
            "name": self.name,
            "description": self.description,
            "category": self.category,
            "complexity": self.complexity,
            "gate_sequence": self.gate_sequence,
            "input_qubits": self.input_qubits,
            "output_qubits": self.output_qubits,
            "parameters": self.parameters,
            "references": self.references,
            "tags": self.tags,
        }


class PrimitiveLoader:
    """
    Loads and caches primitive definitions from YAML files.
    
    Usage:
        loader = PrimitiveLoader("path/to/primitives/")
        qft = loader.get_primitive("primitive_qft")
        all_primitives = loader.get_all_primitives()
    """
    
    def __init__(self, primitives_dir: Optional[str] = None):
        """
        Initialize primitive loader.
        
        Args:
            primitives_dir: Directory containing primitive YAML files.
                          If None, uses default location.
        """
        if primitives_dir is None:
            # Default to package primitives directory
            current_dir = Path(__file__).parent.parent
            primitives_dir = current_dir / "knowledge_graph" / "primitives"
        
        self.primitives_dir = Path(primitives_dir)
        self._cache: Dict[str, PrimitiveDefinition] = {}
        self._loaded = False
    
    def _load_all(self) -> None:
        """Load all primitives from the directory."""
        if self._loaded:
            return
        
        if not self.primitives_dir.exists():
            raise FileNotFoundError(f"Primitives directory not found: {self.primitives_dir}")
        
        for yaml_file in self.primitives_dir.glob("*.yaml"):
            try:
                primitive = PrimitiveDefinition.from_yaml(str(yaml_file))
                if primitive.id:
                    self._cache[primitive.id] = primitive
            except Exception as e:
                print(f"Warning: Failed to load primitive from {yaml_file}: {e}")
        
        self._loaded = True
    
    def get_primitive(self, primitive_id: str) -> Optional[PrimitiveDefinition]:
        """
        Get a primitive definition by ID.
        
        Args:
            primitive_id: Primitive identifier
            
        Returns:
            PrimitiveDefinition if found, None otherwise
        """
        self._load_all()
        return self._cache.get(primitive_id)
    
    def get_primitives_by_category(self, category: str) -> List[PrimitiveDefinition]:
        """
        Get all primitives in a category.
        
        Args:
            category: Category name
            
        Returns:
            List of primitive definitions
        """
        self._load_all()
        return [p for p in self._cache.values() if p.category == category]
    
    def get_primitives_by_tag(self, tag: str) -> List[PrimitiveDefinition]:
        """
        Get all primitives with a specific tag.
        
        Args:
            tag: Tag name
            
        Returns:
            List of primitive definitions
        """
        self._load_all()
        return [p for p in self._cache.values() if tag in p.tags]
    
    def get_all_primitives(self) -> List[PrimitiveDefinition]:
        """
        Get all loaded primitives.
        
        Returns:
            List of all primitive definitions
        """
        self._load_all()
        return list(self._cache.values())
    
    def get_all_primitive_ids(self) -> List[str]:
        """
        Get IDs of all loaded primitives.
        
        Returns:
            List of primitive IDs
        """
        self._load_all()
        return list(self._cache.keys())
    
    def search_primitives(self, query: str) -> List[PrimitiveDefinition]:
        """
        Search primitives by name, description, or tags.
        
        Args:
            query: Search query string
            
        Returns:
            List of matching primitive definitions
        """
        self._load_all()
        query = query.lower()
        results = []
        
        for primitive in self._cache.values():
            if (query in primitive.id.lower() or
                query in primitive.name.lower() or
                query in primitive.description.lower() or
                any(query in tag.lower() for tag in primitive.tags)):
                results.append(primitive)
        
        return results
    
    def clear_cache(self) -> None:
        """Clear the primitive cache."""
        self._cache.clear()
        self._loaded = False
    
    def reload(self) -> None:
        """Reload all primitives from disk."""
        self.clear_cache()
        self._load_all()
    
    def is_parameterized(self, primitive_id: str) -> bool:
        """
        Check if a primitive is parameterized.
        
        Args:
            primitive_id: Primitive identifier
            
        Returns:
            True if primitive has parameters
        """
        primitive = self.get_primitive(primitive_id)
        if primitive is None:
            return False
        return len(primitive.parameters) > 0
    
    def get_primitive_signature(self, primitive_id: str) -> Optional[Dict[str, Any]]:
        """
        Get the signature (input/output qubits, parameters) of a primitive.
        
        Args:
            primitive_id: Primitive identifier
            
        Returns:
            Dictionary with signature information
        """
        primitive = self.get_primitive(primitive_id)
        if primitive is None:
            return None
        
        return {
            "id": primitive.id,
            "name": primitive.name,
            "input_qubits": primitive.input_qubits,
            "output_qubits": primitive.output_qubits,
            "parameters": list(primitive.parameters.keys()),
            "category": primitive.category,
        }
