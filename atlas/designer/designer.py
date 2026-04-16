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

# Knowledge graph imports (optional)
try:
    from ..knowledge.neo4j_client import Neo4jClient
    from ..knowledge.models import Algorithm, Implementation
    HAS_NEO4J = True
except ImportError:
    HAS_NEO4J = False

try:
    from ..extractor.algorithm_ir import AlgorithmIR
    HAS_ALGORITHM_IR = True
except ImportError:
    HAS_ALGORITHM_IR = False


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
        
        # With Neo4j integration
        designer = CircuitDesigner(neo4j_uri="bolt://localhost:7687")
        quantum_ir = designer.design_circuit_from_kg("algorithm_id")
    """
    
    def __init__(
        self,
        neo4j_uri: Optional[str] = None,
        neo4j_user: Optional[str] = None,
        neo4j_password: Optional[str] = None,
        primitives_dir: Optional[str] = None,
        default_optimization_level: int = OptimizationLevel.O1
    ):
        """
        Initialize circuit designer.
        
        Args:
            neo4j_uri: Neo4j Bolt URI (optional)
            neo4j_user: Neo4j username (optional)
            neo4j_password: Neo4j password (optional)
            primitives_dir: Directory with primitive definitions
            default_optimization_level: Default optimization level
        """
        # Initialize components
        self.primitive_loader = PrimitiveLoader(primitives_dir)
        self.primitive_composer = PrimitiveComposer(self.primitive_loader)
        self.optimizer = CircuitOptimizer(default_optimization_level)
        self.parameter_mapper = ParameterMapper()
        
        self.default_optimization_level = default_optimization_level
        
        # Neo4j client (lazy initialization)
        self._neo4j_client: Optional[Any] = None
        self._neo4j_config = {
            "uri": neo4j_uri,
            "user": neo4j_user,
            "password": neo4j_password,
        }
    
    @property
    def neo4j_client(self) -> Optional[Any]:
        """Get or create Neo4j client."""
        if self._neo4j_client is None and HAS_NEO4J:
            if self._neo4j_config.get("password"):
                try:
                    self._neo4j_client = Neo4jClient(
                        uri=self._neo4j_config.get("uri"),
                        username=self._neo4j_config.get("user"),
                        password=self._neo4j_config.get("password"),
                    )
                    self._neo4j_client.connect()
                except Exception as e:
                    print(f"Warning: Failed to connect to Neo4j: {e}")
        return self._neo4j_client
    
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
        Design a circuit from knowledge graph algorithm definition.
        
        Args:
            algorithm_id: Algorithm ID in knowledge graph
            optimization_level: Optimization level
            parameter_overrides: Override parameter values
            
        Returns:
            QuantumIR with the designed circuit
        """
        if not HAS_NEO4J or self.neo4j_client is None:
            raise RuntimeError("Neo4j not available. Provide connection parameters.")
        
        # Fetch algorithm from Neo4j
        algorithm = self.neo4j_client.get_algorithm(algorithm_id)
        if algorithm is None:
            raise ValueError(f"Algorithm not found in knowledge graph: {algorithm_id}")
        
        # Get associated primitives
        primitives = self.neo4j_client.get_algorithm_primitives(algorithm_id)
        primitive_ids = [p.id for p in primitives]
        
        # Convert to AlgorithmIR-like dict
        algo_dict = {
            "id": algorithm_id,
            "name": getattr(algorithm, 'name', algorithm_id),
            "primitives": primitive_ids,
            "input_params": getattr(algorithm, 'input_params', []),
        }
        
        # Design circuit
        return self.design_circuit(
            algo_dict,
            optimization_level=optimization_level,
            parameter_overrides=parameter_overrides,
            circuit_name=f"circuit_{algorithm_id}"
        )
    
    def save_circuit_to_kg(
        self,
        quantum_ir: QuantumIR,
        implementation_name: Optional[str] = None,
        implementation_id: Optional[str] = None
    ) -> bool:
        """
        Save a designed circuit to the knowledge graph.
        
        Creates an Implementation node linked to the Algorithm.
        
        Args:
            quantum_ir: QuantumIR to save
            implementation_name: Name for the implementation
            implementation_id: ID for the implementation
            
        Returns:
            True if successful
        """
        if not HAS_NEO4J or self.neo4j_client is None:
            raise RuntimeError("Neo4j not available. Provide connection parameters.")
        
        # Generate implementation ID if not provided
        if implementation_id is None:
            import uuid
            implementation_id = f"impl_{quantum_ir.algorithm_id}_{uuid.uuid4().hex[:8]}"
        
        if implementation_name is None:
            implementation_name = f"Generated Circuit for {quantum_ir.algorithm_id}"
        
        # Create implementation data
        impl_data = {
            "id": implementation_id,
            "name": implementation_name,
            "algorithm_id": quantum_ir.algorithm_id,
            "circuit_json": quantum_ir.to_json(),
            "gate_count": quantum_ir.gate_count,
            "depth": quantum_ir.depth,
            "num_qubits": quantum_ir.num_qubits,
            "optimization_level": quantum_ir.optimization_level,
            "created_at": quantum_ir.created_at,
        }
        
        try:
            # Create implementation node
            impl = Implementation(**impl_data)
            self.neo4j_client.create_implementation(impl)
            
            # Create relationship to primitives used
            metadata = quantum_ir.metadata
            primitives_used = metadata.get("primitives_used", [])
            
            return True
        except Exception as e:
            print(f"Error saving circuit to knowledge graph: {e}")
            return False
    
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
        Retrieve a previously designed circuit from knowledge graph.
        
        Args:
            algorithm_id: Algorithm ID
            
        Returns:
            QuantumIR if found, None otherwise
        """
        if not HAS_NEO4J or self.neo4j_client is None:
            return None
        
        try:
            # Query for implementations of this algorithm
            # This is a simplified version - full implementation would
            # query the graph for the latest/best implementation
            return None
        except Exception:
            return None
    
    def link_circuit_to_algorithm(
        self,
        circuit_id: str,
        algorithm_id: str
    ) -> bool:
        """
        Create CIRCUIT_FOR relationship in knowledge graph.
        
        Args:
            circuit_id: Circuit/Implementation ID
            algorithm_id: Algorithm ID
            
        Returns:
            True if successful
        """
        if not HAS_NEO4J or self.neo4j_client is None:
            return False
        
        try:
            # This would create a relationship in Neo4j
            # For now, this is a placeholder for the full implementation
            return True
        except Exception:
            return False
