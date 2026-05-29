"""
Primitive Composer Module

Composes primitive gate sequences and handles qubit allocation.
"""

import math
from typing import Any, Dict, List, Optional, Set, Tuple
from dataclasses import dataclass, field

from .primitive_loader import PrimitiveLoader, PrimitiveDefinition
from .quantum_circuit import QuantumCircuit, Gate


@dataclass
class CompositionResult:
    """
    Result of primitive composition.
    
    Attributes:
        circuit: The composed quantum circuit
        qubit_mapping: Mapping from primitive logical qubits to circuit physical qubits
        primitives_used: List of primitive IDs used in composition
        total_qubits: Total number of qubits used
        success: Whether composition was successful
        error_message: Error message if composition failed
    """
    circuit: Optional[QuantumCircuit] = None
    qubit_mapping: Dict[str, Dict[int, int]] = field(default_factory=dict)
    primitives_used: List[str] = field(default_factory=list)
    total_qubits: int = 0
    success: bool = False
    error_message: str = ""


class PrimitiveComposer:
    """
    Composes quantum primitives into a unified quantum circuit.
    
    Handles:
    - Loading primitives from YAML definitions
    - Composing multiple primitives in sequence
    - Parameterized primitives (e.g., n-qubit QFT)
    - Qubit allocation and connection between primitives
    
    Usage:
        composer = PrimitiveLoader()
        result = composer.compose(
            ["primitive_qft", "primitive_qpe"],
            params={"primitive_qft": {"n": 4}}
        )
    """
    
    def __init__(self, primitive_loader: Optional[PrimitiveLoader] = None):
        """
        Initialize primitive composer.
        
        Args:
            primitive_loader: PrimitiveLoader instance. If None, creates default.
        """
        self.loader = primitive_loader or PrimitiveLoader()
        self._qubit_counter = 0
    
    def compose(
        self,
        primitive_ids: List[str],
        params: Optional[Dict[str, Dict[str, Any]]] = None,
        initial_qubits: Optional[List[int]] = None,
        circuit_name: str = "composed_circuit"
    ) -> CompositionResult:
        """
        Compose multiple primitives into a unified circuit.
        
        Args:
            primitive_ids: List of primitive IDs to compose in order
            params: Parameters for each primitive {primitive_id: {param_name: value}}
            initial_qubits: Initial qubit indices to use. If None, allocates new qubits.
            circuit_name: Name for the resulting circuit
            
        Returns:
            CompositionResult with the composed circuit and metadata
        """
        result = CompositionResult()
        result.primitives_used = primitive_ids
        params = params or {}
        
        if not primitive_ids:
            result.error_message = "No primitives specified"
            return result
        
        try:
            # Load all primitives first
            primitives = []
            for pid in primitive_ids:
                primitive = self.loader.get_primitive(pid)
                if primitive is None:
                    result.error_message = f"Primitive not found: {pid}"
                    return result
                primitives.append(primitive)
            
            # Calculate total qubits needed
            max_qubits = self._calculate_total_qubits(primitives, params)
            
            # Create circuit
            if initial_qubits:
                # Use provided qubits
                num_qubits = max(max(initial_qubits) + 1, max_qubits)
                available_qubits = initial_qubits
            else:
                # Allocate new qubits
                num_qubits = max_qubits
                available_qubits = list(range(num_qubits))
            
            circuit = QuantumCircuit(num_qubits=num_qubits, name=circuit_name)
            
            # Compose primitives
            current_qubit_offset = 0
            for i, primitive in enumerate(primitives):
                primitive_params = params.get(primitive.id, {})
                
                # Generate gate sequence for this primitive
                gates = self._generate_primitive_gates(
                    primitive, 
                    primitive_params,
                    current_qubit_offset
                )
                
                # Add gates to circuit
                for gate in gates:
                    circuit.add_gate(gate)
                
                # Record qubit mapping for this primitive
                num_primitive_qubits = self._get_primitive_qubits(primitive, primitive_params)
                result.qubit_mapping[primitive.id] = {
                    j: current_qubit_offset + j 
                    for j in range(num_primitive_qubits)
                }
                
                current_qubit_offset += num_primitive_qubits
            
            result.circuit = circuit
            result.total_qubits = num_qubits
            result.success = True
            
        except Exception as e:
            result.error_message = str(e)
        
        return result
    
    def compose_single(
        self,
        primitive_id: str,
        params: Optional[Dict[str, Any]] = None,
        qubit_offset: int = 0,
        circuit_name: str = "single_primitive"
    ) -> CompositionResult:
        """
        Compose a single primitive into a circuit.
        
        Args:
            primitive_id: ID of the primitive to compose
            params: Parameters for the primitive
            qubit_offset: Starting qubit index
            circuit_name: Name for the resulting circuit
            
        Returns:
            CompositionResult with the composed circuit
        """
        return self.compose(
            primitive_ids=[primitive_id],
            params={primitive_id: params or {}},
            initial_qubits=list(range(qubit_offset, qubit_offset + 20)),  # Reserve enough qubits
            circuit_name=circuit_name
        )
    
    def _generate_primitive_gates(
        self,
        primitive: PrimitiveDefinition,
        params: Dict[str, Any],
        qubit_offset: int
    ) -> List[Gate]:
        """
        Generate gate sequence for a primitive.
        
        Args:
            primitive: Primitive definition
            params: Parameters for this primitive
            qubit_offset: Starting qubit index
            
        Returns:
            List of gates
        """
        gates = []
        
        # Check if this is a parameterized primitive with dynamic generation
        if self.loader.is_parameterized(primitive.id):
            # Use parameterized generation
            return self._generate_parameterized_primitive(primitive, params, qubit_offset)
        
        # Use predefined gate sequence from YAML
        for gate_def in primitive.gate_sequence:
            gate = self._parse_gate_definition(gate_def, qubit_offset, params)
            if gate:
                gates.append(gate)
        
        return gates
    
    def _generate_parameterized_primitive(
        self,
        primitive: PrimitiveDefinition,
        params: Dict[str, Any],
        qubit_offset: int
    ) -> List[Gate]:
        """
        Generate gates for a parameterized primitive.
        
        Handles common parameterized primitives like:
        - n-qubit QFT
        - n-qubit QPE
        - Trotterized Hamiltonian simulation
        
        Args:
            primitive: Primitive definition
            params: Parameters (e.g., {"n": 4} for 4-qubit QFT)
            qubit_offset: Starting qubit index
            
        Returns:
            List of gates
        """
        gates = []
        
        # Handle specific parameterized primitives
        if "qft" in primitive.id.lower():
            n = params.get("n", params.get("num_qubits", 3))
            gates = self._generate_qft(n, qubit_offset)
        elif "qpe" in primitive.id.lower():
            n = params.get("n", params.get("num_qubits", 4))
            gates = self._generate_qpe(n, qubit_offset, params.get("unitary_primitive"))
        elif "hadamard" in primitive.id.lower() or "h_all" in primitive.id.lower():
            n = params.get("n", params.get("num_qubits", 1))
            for i in range(n):
                gates.append(Gate("H", [qubit_offset + i]))
        elif "entangle" in primitive.id.lower() or "bell" in primitive.id.lower():
            # Bell state preparation
            gates = self._generate_bell_state(qubit_offset)
        else:
            # Fallback: use gate sequence from definition if available
            for gate_def in primitive.gate_sequence:
                gate = self._parse_gate_definition(gate_def, qubit_offset, params)
                if gate:
                    gates.append(gate)
        
        return gates
    
    def _generate_qft(self, n: int, offset: int) -> List[Gate]:
        """Generate n-qubit QFT circuit."""
        gates = []
        
        for i in range(n):
            # Hadamard on qubit i
            gates.append(Gate("H", [offset + i]))
            
            # Controlled rotations
            for j in range(i + 1, n):
                # Controlled R_k where k = j - i + 1
                k = j - i + 1
                theta = 2 * math.pi / (2 ** k)
                # CPHASE as CNOT + single qubit rotations (simplified)
                # In practice, this would use a controlled-phase gate
                # For now, use RZ with control
                gates.append(Gate("RZ", [offset + j], control_qubits=[offset + i], params={"theta": theta}))
        
        # Swap qubits to correct order (QFT reverses output)
        for i in range(n // 2):
            gates.append(Gate("SWAP", [offset + i, offset + n - 1 - i]))
        
        return gates
    
    def _generate_qpe(
        self,
        n_counting: int,
        offset: int,
        unitary_primitive: Optional[str] = None
    ) -> List[Gate]:
        """Generate Quantum Phase Estimation circuit."""
        gates = []
        
        # Hadamard on counting register
        for i in range(n_counting):
            gates.append(Gate("H", [offset + i]))
        
        # Controlled-U operations (simplified)
        # In full implementation, this would apply the unitary primitive
        for i in range(n_counting):
            # Controlled unitary operation
            # Simplified as CNOT for demonstration
            if unitary_primitive:
                # Would look up and apply the unitary
                pass
            # Placeholder: just add a CNOT to show structure
            gates.append(Gate("CNOT", [offset + n_counting], control_qubits=[offset + i]))
        
        # Inverse QFT on counting register
        qft_gates = self._generate_qft(n_counting, offset)
        # Apply inverse (reverse order and conjugate)
        for gate in reversed(qft_gates):
            if gate.name == "H":
                gates.append(gate)  # H is self-inverse
            elif gate.name == "RZ" and gate.params:
                # Negate the angle for inverse
                inv_params = {"theta": -gate.params["theta"]}
                gates.append(Gate("RZ", gate.target_qubits, gate.control_qubits, inv_params))
            elif gate.name == "SWAP":
                gates.append(gate)  # SWAP is self-inverse
        
        return gates
    
    def _generate_bell_state(self, offset: int) -> List[Gate]:
        """Generate Bell state preparation circuit."""
        gates = []
        gates.append(Gate("H", [offset]))
        gates.append(Gate("CNOT", [offset + 1], control_qubits=[offset]))
        return gates
    
    def _parse_gate_definition(
        self,
        gate_def: Dict[str, Any],
        qubit_offset: int,
        params: Dict[str, Any]
    ) -> Optional[Gate]:
        """
        Parse a gate definition from YAML.
        
        Args:
            gate_def: Gate definition dictionary
            qubit_offset: Qubit index offset
            params: Parameters for substitution
            
        Returns:
            Gate object or None if parsing fails
        """
        try:
            gate_name = gate_def.get("name", "")
            if not gate_name:
                return None
            
            # Parse target qubits (can be indices or expressions)
            target_qubits = self._parse_qubit_list(
                gate_def.get("targets", []), 
                qubit_offset, 
                params
            )
            
            # Parse control qubits
            control_qubits = self._parse_qubit_list(
                gate_def.get("controls", []),
                qubit_offset,
                params
            )
            
            # Parse parameters (for rotation gates)
            gate_params = {}
            for param_name, param_value in gate_def.get("params", {}).items():
                # Substitute parameter values
                if isinstance(param_value, str) and param_value in params:
                    gate_params[param_name] = float(params[param_value])
                else:
                    gate_params[param_name] = float(param_value)
            
            return Gate(gate_name, target_qubits, control_qubits, gate_params)
            
        except Exception as e:
            print(f"Warning: Failed to parse gate definition {gate_def}: {e}")
            return None
    
    def _parse_qubit_list(
        self,
        qubit_defs: List[Any],
        qubit_offset: int,
        params: Dict[str, Any]
    ) -> List[int]:
        """
        Parse qubit list with parameter substitution.
        
        Args:
            qubit_defs: List of qubit definitions (int or str)
            qubit_offset: Offset to add to indices
            params: Parameters for substitution
            
        Returns:
            List of physical qubit indices
        """
        result = []
        for q in qubit_defs:
            if isinstance(q, int):
                result.append(qubit_offset + q)
            elif isinstance(q, str):
                # Try to substitute parameter
                if q in params:
                    result.append(qubit_offset + int(params[q]))
                else:
                    # Try to evaluate as expression
                    try:
                        # Replace common parameter names
                        expr = q
                        for param_name, param_value in params.items():
                            expr = expr.replace(param_name, str(param_value))
                        result.append(qubit_offset + int(eval(expr)))
                    except:
                        result.append(qubit_offset + int(q))
        return result
    
    def _get_primitive_qubits(
        self,
        primitive: PrimitiveDefinition,
        params: Dict[str, Any]
    ) -> int:
        """
        Get the number of qubits needed for a primitive.
        
        Args:
            primitive: Primitive definition
            params: Parameters (for parameterized primitives)
            
        Returns:
            Number of qubits
        """
        # Check for parameterized primitive
        if "n" in params:
            return int(params["n"])
        if "num_qubits" in params:
            return int(params["num_qubits"])
        
        # Use definition values
        return max(primitive.input_qubits, primitive.output_qubits, 1)
    
    def _calculate_total_qubits(
        self,
        primitives: List[PrimitiveDefinition],
        params: Dict[str, Dict[str, Any]]
    ) -> int:
        """
        Calculate total qubits needed for all primitives.
        
        Args:
            primitives: List of primitives
            params: Parameters for each primitive
            
        Returns:
            Total number of qubits
        """
        total = 0
        for primitive in primitives:
            primitive_params = params.get(primitive.id, {})
            total += self._get_primitive_qubits(primitive, primitive_params)
        return total
    
    def get_composable_primitives(self) -> List[str]:
        """
        Get list of primitives that can be composed (have gate sequences).
        
        Returns:
            List of primitive IDs
        """
        composable = []
        for primitive in self.loader.get_all_primitives():
            if primitive.gate_sequence or self.loader.is_parameterized(primitive.id):
                composable.append(primitive.id)
        return composable
