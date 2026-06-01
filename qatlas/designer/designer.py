"""
Circuit Designer Module

Main class for designing quantum circuits from Algorithm IR.
Integrates primitive composition, optimization, and parameter mapping.
"""

import os
from typing import Any, Dict, List, Optional, Tuple
from pathlib import Path

from .primitive_loader import PrimitiveLoader
from .primitive_composer import PrimitiveComposer, CompositionResult
from .quantum_circuit import QuantumCircuit
from .quantum_ir import QuantumIR
from .optimizer import CircuitOptimizer, OptimizationLevel
from .parameter_mapper import ParameterMapper

# Knowledge-graph data models (optional). The graph itself is server-side:
# the Go qatlasd owns the Neo4j connection and exposes reads over
# /api/graph/*. The Python client does not connect to Neo4j directly.
try:
    from ..knowledge.models import Algorithm, Implementation
    HAS_KG_MODELS = True
except ImportError:
    HAS_KG_MODELS = False

try:
    from ..extractor.algorithm_ir import AlgorithmIR
    HAS_ALGORITHM_IR = True
except ImportError:
    HAS_ALGORITHM_IR = False


_KG_SERVER_ONLY = (
    "Knowledge-graph access is server-side only. The Go qatlasd owns the "
    "Neo4j connection and exposes reads over /api/graph/*; client-side graph "
    "read/write is not implemented."
)


class CircuitDesigner:
    """
    Main class for quantum circuit design.
    
    Workflow:
    1. Load algorithm definition (from IR or knowledge graph)
    2. Map algorithm parameters to circuit parameters
    3. Compose primitives into circuit
    4. Apply optimizations
    5. Output QuantumIR
    
    Usage:
        designer = CircuitDesigner()
        quantum_ir = designer.design_circuit(algorithm_ir)
    """
    
    def __init__(
        self,
        primitives_dir: Optional[str] = None,
        default_optimization_level: int = OptimizationLevel.O1
    ):
        """
        Initialize circuit designer.
        
        Args:
            primitives_dir: Directory with primitive definitions
            default_optimization_level: Default optimization level
        """
        # Initialize components
        self.primitive_loader = PrimitiveLoader(primitives_dir)
        self.primitive_composer = PrimitiveComposer(self.primitive_loader)
        self.optimizer = CircuitOptimizer(default_optimization_level)
        self.parameter_mapper = ParameterMapper()
        
        self.default_optimization_level = default_optimization_level
    
    def design_circuit(
        self,
        algorithm_ir: Any,
        optimization_level: Optional[int] = None,
        parameter_overrides: Optional[Dict[str, Any]] = None,
        circuit_name: Optional[str] = None
    ) -> QuantumIR:
        """
        Design a quantum circuit from Algorithm IR.
        
        Args:
            algorithm_ir: AlgorithmIR object or algorithm ID string
            optimization_level: Optimization level (0-3)
            parameter_overrides: Override parameter values
            circuit_name: Name for the circuit
            
        Returns:
            QuantumIR with the designed circuit
        """
        opt_level = optimization_level if optimization_level is not None else self.default_optimization_level
        
        # Handle string algorithm_id
        if isinstance(algorithm_ir, str):
            return self.design_circuit_from_kg(
                algorithm_ir,
                optimization_level=opt_level,
                parameter_overrides=parameter_overrides
            )
        
        # Extract algorithm info
        if HAS_ALGORITHM_IR and isinstance(algorithm_ir, AlgorithmIR):
            algo_id = algorithm_ir.id
            algo_name = algorithm_ir.name
            primitives = algorithm_ir.primitives
        else:
            # Handle dict
            algo_id = getattr(algorithm_ir, 'id', algorithm_ir.get('id', 'unknown'))
            algo_name = getattr(algorithm_ir, 'name', algorithm_ir.get('name', 'Unknown'))
            primitives = getattr(algorithm_ir, 'primitives', algorithm_ir.get('primitives', []))
        
        # Map parameters
        circuit_params = self.parameter_mapper.map_parameters(
            algorithm_ir,
            overrides=parameter_overrides or {}
        )
        
        # Determine primitives to use
        if not primitives:
            # Try to infer from algorithm type
            primitives = self._infer_primitives(algo_id, algo_name)
        
        # Prepare primitive parameters
        primitive_params = {}
        for pid in primitives:
            primitive_params[pid] = circuit_params.copy()
        
        # Compose primitives
        result = self.primitive_composer.compose(
            primitive_ids=primitives,
            params=primitive_params,
            circuit_name=circuit_name or f"circuit_{algo_id}"
        )
        
        if not result.success:
            raise ValueError(f"Failed to compose circuit: {result.error_message}")
        
        circuit = result.circuit
        
        # Apply optimization
        if opt_level > 0 and circuit:
            self.optimizer.set_level(opt_level)
            circuit = self.optimizer.optimize(circuit)
        
        # Create Quantum IR
        quantum_ir = QuantumIR(
            circuit=circuit,
            algorithm_id=algo_id,
            optimization_level=opt_level,
            metadata={
                "primitives_used": primitives,
                "qubit_mapping": result.qubit_mapping,
                "circuit_params": circuit_params,
                "validation_errors": self.parameter_mapper.get_validation_errors(),
            }
        )
        
        return quantum_ir
    
    def design_circuit_from_kg(
        self,
        algorithm_id: str,
        optimization_level: Optional[int] = None,
        parameter_overrides: Optional[Dict[str, Any]] = None
    ) -> QuantumIR:
        """
        Design a circuit from a knowledge graph algorithm definition.
        
        Placeholder: graph access is server-side only (see /api/graph/*).
        """
        raise NotImplementedError(_KG_SERVER_ONLY)

    def design_from_wiki(
        self,
        algorithm_id: str,
        optimization_level: Optional[int] = None,
        parameter_overrides: Optional[Dict[str, Any]] = None
    ) -> QuantumIR:
        """
        Design a circuit from wiki algorithm page.

        This method reads from wiki/ instead of Neo4j,
        providing a more detailed view of the algorithm.

        Args:
            algorithm_id: Algorithm page ID (e.g., 'algo-shors')
            optimization_level: Optimization level
            parameter_overrides: Override parameter values

        Returns:
            QuantumIR with designed circuit

        Raises:
            ValueError: If algorithm not found in wiki
        """
        from qatlas.wiki.engine import WikiEngine

        wiki = WikiEngine()
        page = wiki.get_page(algorithm_id)

        if page is None:
            raise ValueError(f"Algorithm not found in wiki: {algorithm_id}")

        # Parse algorithm info from wiki page
        algo_info = self._parse_algorithm_wiki(page)

        # Design circuit using existing logic
        return self.design_circuit(
            algo_info,
            optimization_level=optimization_level,
            parameter_overrides=parameter_overrides,
            circuit_name=f"circuit_{algorithm_id}"
        )

    def _parse_algorithm_wiki(self, page: Any) -> Dict[str, Any]:
        """
        Parse algorithm info from wiki page.

        Extracts:
        - ID, name
        - Primitives used (from wiki links)
        - Complexity info
        - Tags

        Args:
            page: WikiPage for algorithm

        Returns:
            Dict with algorithm info compatible with design_circuit()
        """
        import re

        content = page.content

        # Extract primitives from wiki links
        # Looking for [[prim-*]] links
        prim_pattern = r'\[\[(prim-[^\]|]+)(?:\|[^\]]+)?\]\]'
        wiki_primitives = list(set(re.findall(prim_pattern, content)))

        # Convert wiki primitive IDs to internal format
        # prim-qft -> primitive_qft
        primitives = []
        for wp in wiki_primitives:
            internal_id = wp.replace("prim-", "primitive_")
            primitives.append(internal_id)

        # Extract complexity info if available
        complexity = {}
        time_match = re.search(r'\*\*Time\*\*:\s*(.+)', content)
        if time_match:
            complexity["time"] = time_match.group(1).strip()
        gates_match = re.search(r'\*\*Gates\*\*:\s*(.+)', content)
        if gates_match:
            complexity["gates"] = gates_match.group(1).strip()

        return {
            "id": page.frontmatter.id,
            "name": page.frontmatter.title,
            "primitives": primitives,
            "tags": page.frontmatter.tags,
            "complexity": complexity,
        }

    def save_circuit_to_kg(
        self,
        quantum_ir: QuantumIR,
        implementation_name: Optional[str] = None,
        implementation_id: Optional[str] = None
    ) -> bool:
        """
        Save a designed circuit to the knowledge graph.
        
        Placeholder: graph writes are server-side only.
        """
        raise NotImplementedError(_KG_SERVER_ONLY)
    
    def save_design_config(
        self,
        quantum_ir: QuantumIR,
        filepath: str
    ) -> None:
        """
        Save circuit design configuration to file.
        
        Args:
            quantum_ir: QuantumIR to save
            filepath: Path to save file
        """
        quantum_ir.save(filepath)
    
    def load_design_config(self, filepath: str) -> QuantumIR:
        """
        Load circuit design configuration from file.
        
        Args:
            filepath: Path to load file
            
        Returns:
            QuantumIR instance
        """
        return QuantumIR.load(filepath)
    
    def get_available_primitives(self) -> List[str]:
        """Get list of available primitive IDs."""
        return self.primitive_loader.get_all_primitive_ids()
    
    def _infer_primitives(self, algo_id: str, algo_name: str) -> List[str]:
        """
        Infer primitives from algorithm name/id.
        
        Args:
            algo_id: Algorithm ID
            algo_name: Algorithm name
            
        Returns:
            List of primitive IDs
        """
        text = f"{algo_id} {algo_name}".lower()
        primitives = []
        
        # Pattern matching for common algorithms
        if 'shor' in text or 'factor' in text:
            primitives = ["primitive_qft", "primitive_qpe"]
        elif 'grover' in text or 'search' in text:
            primitives = ["primitive_amplitude_amplification"]
        elif 'qpe' in text or 'estimation' in text:
            primitives = ["primitive_qpe", "primitive_qft"]
        elif 'hamiltonian' in text or 'simulation' in text:
            primitives = ["primitive_hamiltonian_simulation"]
        elif 'variational' in text or 'vqe' in text or 'qaoa' in text:
            primitives = ["primitive_variational_circuit"]
        elif 'walk' in text:
            primitives = ["primitive_quantum_walk"]
        elif 'encode' in text or 'block' in text:
            primitives = ["primitive_block_encoding"]
        else:
            # Default: try to find any matching primitives
            available = self.get_available_primitives()
            primitives = available[:1] if available else []
        
        return primitives
    
    def get_algorithm_circuit_from_kg(self, algorithm_id: str) -> Optional[QuantumIR]:
        """
        Retrieve a previously designed circuit from the knowledge graph.
        
        Placeholder: graph reads are server-side only (see /api/graph/*).
        """
        return None
    
    def link_circuit_to_algorithm(
        self,
        circuit_id: str,
        algorithm_id: str
    ) -> bool:
        """
        Create a CIRCUIT_FOR relationship in the knowledge graph.
        
        Placeholder: graph writes are server-side only.
        """
        return False
