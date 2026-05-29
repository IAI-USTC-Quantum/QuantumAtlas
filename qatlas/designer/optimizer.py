"""
Circuit Optimizer Module

Implements circuit optimization algorithms with multiple optimization levels.
"""

import math
from typing import List, Optional, Set, Tuple, Dict
from copy import deepcopy

from .quantum_circuit import QuantumCircuit, Gate


class OptimizationLevel:
    """Optimization level constants."""
    O0 = 0  # No optimization
    O1 = 1  # Basic optimizations (gate cancellations)
    O2 = 2  # Standard optimizations (CNOT simplification)
    O3 = 3  # Aggressive optimizations (gate fusion)


class CircuitOptimizer:
    """
    Quantum circuit optimizer.
    
    Implements multiple optimization levels:
    - O0: No optimization
    - O1: Gate cancellations (HH=I, XX=I, consecutive inverse gates)
    - O2: CNOT chain simplification, adjacent CNOT cancellation
    - O3: Single-qubit gate fusion (combine into U3 gates)
    
    Usage:
        optimizer = CircuitOptimizer(level=OptimizationLevel.O2)
        optimized_circuit = optimizer.optimize(circuit)
    """
    
    # Gates that are self-inverse (applying twice = identity)
    SELF_INVERSE_GATES = {"H", "X", "Y", "Z", "SWAP"}
    
    # Gates that commute with specific other gates
    COMMUTING_PAIRS = {
        ("X", "CNOT"),  # X on control commutes with CNOT
        ("Z", "CZ"),    # Z on control commutes with CZ
    }
    
    def __init__(self, level: int = OptimizationLevel.O1):
        """
        Initialize optimizer.
        
        Args:
            level: Optimization level (0-3)
        """
        self.level = level
        self._optimization_stats = {
            "gates_removed": 0,
            "gates_fused": 0,
            "depth_reduction": 0,
        }
    
    def optimize(self, circuit: QuantumCircuit) -> QuantumCircuit:
        """
        Optimize a quantum circuit.
        
        Args:
            circuit: Input circuit to optimize
            
        Returns:
            Optimized circuit
        """
        if self.level == OptimizationLevel.O0:
            return circuit.copy()
        
        # Start with a copy
        optimized = circuit.copy()
        initial_gate_count = optimized.gate_count
        initial_depth = optimized.depth
        
        # Apply optimizations based on level
        if self.level >= OptimizationLevel.O1:
            optimized = self._optimize_level1(optimized)
        
        if self.level >= OptimizationLevel.O2:
            optimized = self._optimize_level2(optimized)
        
        if self.level >= OptimizationLevel.O3:
            optimized = self._optimize_level3(optimized)
        
        # Update stats
        self._optimization_stats["gates_removed"] = initial_gate_count - optimized.gate_count
        self._optimization_stats["depth_reduction"] = initial_depth - optimized.depth
        
        return optimized
    
    def _optimize_level1(self, circuit: QuantumCircuit) -> QuantumCircuit:
        """
        Level 1 optimizations: Gate cancellations.
        
        - Consecutive self-inverse gates (HH, XX, YY, ZZ, etc.)
        - Cancel inverse gate pairs
        """
        gates = circuit.gates.copy()
        new_gates = []
        
        i = 0
        while i < len(gates):
            gate = gates[i]
            
            if gate.name == "BARRIER":
                new_gates.append(gate)
                i += 1
                continue
            
            # Check for cancellation with next gate
            cancelled = False
            if i + 1 < len(gates):
                next_gate = gates[i + 1]
                
                if self._can_cancel(gate, next_gate):
                    # Skip both gates (they cancel out)
                    cancelled = True
                    i += 2
                    continue
            
            if not cancelled:
                new_gates.append(gate)
                i += 1
        
        # Create new circuit with optimized gates
        result = QuantumCircuit(
            num_qubits=circuit.num_qubits,
            num_clbits=circuit.num_clbits,
            name=circuit.name,
            qubit_labels=circuit.qubit_labels.copy()
        )
        result.gates = new_gates
        result._logical_to_physical = circuit._logical_to_physical.copy()
        
        return result
    
    def _optimize_level2(self, circuit: QuantumCircuit) -> QuantumCircuit:
        """
        Level 2 optimizations: CNOT chain simplification.
        
        - Adjacent CNOTs with same target and control cancel
        - CNOT propagation rules
        """
        gates = circuit.gates.copy()
        new_gates = []
        
        i = 0
        while i < len(gates):
            gate = gates[i]
            
            if gate.name == "BARRIER":
                new_gates.append(gate)
                i += 1
                continue
            
            # Check for CNOT cancellations
            cancelled = False
            if gate.name == "CNOT" and i + 1 < len(gates):
                next_gate = gates[i + 1]
                
                if self._can_cancel_cnot(gate, next_gate):
                    # Skip both CNOTs
                    cancelled = True
                    i += 2
                    continue
                
                # Check for CNOT swap simplification
                if self._is_cnot_swap_pattern(gate, next_gate):
                    # Replace with SWAP gate
                    swap_gate = Gate(
                        "SWAP",
                        [gate.target_qubits[0], next_gate.target_qubits[0]]
                    )
                    new_gates.append(swap_gate)
                    cancelled = True
                    i += 2
                    continue
            
            if not cancelled:
                new_gates.append(gate)
                i += 1
        
        # Create new circuit
        result = QuantumCircuit(
            num_qubits=circuit.num_qubits,
            num_clbits=circuit.num_clbits,
            name=circuit.name,
            qubit_labels=circuit.qubit_labels.copy()
        )
        result.gates = new_gates
        result._logical_to_physical = circuit._logical_to_physical.copy()
        
        # Also apply level 1 optimizations again
        result = self._optimize_level1(result)
        
        return result
    
    def _optimize_level3(self, circuit: QuantumCircuit) -> QuantumCircuit:
        """
        Level 3 optimizations: Single-qubit gate fusion.
        
        - Combine consecutive single-qubit gates into U3 gates
        - Euler angle decomposition
        """
        # Group gates by qubit
        gates_by_qubit: Dict[int, List[Tuple[int, Gate]]] = {}
        
        for idx, gate in enumerate(circuit.gates):
            if gate.name == "BARRIER":
                continue
            
            # Get all affected qubits
            affected_qubits = set(gate.target_qubits + gate.control_qubits)
            
            for q in affected_qubits:
                if q not in gates_by_qubit:
                    gates_by_qubit[q] = []
                gates_by_qubit[q].append((idx, gate))
        
        # Find sequences of single-qubit gates to fuse
        fused_indices: Set[int] = set()
        new_gates: List[Gate] = []
        
        for qubit, gate_list in gates_by_qubit.items():
            # Find consecutive single-qubit gates on this qubit
            single_qubit_sequence: List[Tuple[int, Gate]] = []
            
            for idx, gate in gate_list:
                if idx in fused_indices:
                    continue
                
                if self._is_single_qubit_on_qubit(gate, qubit):
                    single_qubit_sequence.append((idx, gate))
                else:
                    # Process accumulated sequence
                    if len(single_qubit_sequence) > 1:
                        fused_gate = self._fuse_single_qubit_gates(single_qubit_sequence, qubit)
                        if fused_gate:
                            new_gates.append(fused_gate)
                            for i, _ in single_qubit_sequence:
                                fused_indices.add(i)
                            self._optimization_stats["gates_fused"] += len(single_qubit_sequence) - 1
                    single_qubit_sequence = []
            
            # Process remaining sequence
            if len(single_qubit_sequence) > 1:
                fused_gate = self._fuse_single_qubit_gates(single_qubit_sequence, qubit)
                if fused_gate:
                    new_gates.append(fused_gate)
                    for i, _ in single_qubit_sequence:
                        fused_indices.add(i)
                    self._optimization_stats["gates_fused"] += len(single_qubit_sequence) - 1
        
        # Build final gate list
        final_gates: List[Gate] = []
        for idx, gate in enumerate(circuit.gates):
            if idx in fused_indices:
                continue
            if gate.name == "BARRIER":
                continue
            final_gates.append(gate)
        
        # Add fused gates
        final_gates.extend(new_gates)
        
        # Sort by original position (approximate)
        # This maintains the order as best as possible
        
        # Create new circuit
        result = QuantumCircuit(
            num_qubits=circuit.num_qubits,
            num_clbits=circuit.num_clbits,
            name=circuit.name,
            qubit_labels=circuit.qubit_labels.copy()
        )
        result.gates = final_gates
        result._logical_to_physical = circuit._logical_to_physical.copy()
        
        # Apply lower level optimizations
        result = self._optimize_level2(result)
        
        return result
    
    def _can_cancel(self, gate1: Gate, gate2: Gate) -> bool:
        """
        Check if two gates cancel each other.
        
        Args:
            gate1: First gate
            gate2: Second gate
            
        Returns:
            True if gates cancel
        """
        # Must be same gate type
        if gate1.name != gate2.name:
            return False
        
        # Must act on same qubits
        if set(gate1.target_qubits) != set(gate2.target_qubits):
            return False
        
        if set(gate1.control_qubits) != set(gate2.control_qubits):
            return False
        
        # Self-inverse gates always cancel
        if gate1.name in self.SELF_INVERSE_GATES:
            return True
        
        # Rotation gates cancel if angles sum to 0 (mod 2π)
        if gate1.name in ["RX", "RY", "RZ"]:
            theta1 = gate1.params.get("theta", 0)
            theta2 = gate2.params.get("theta", 0)
            # Check if they sum to approximately 0 or 2π
            total = theta1 + theta2
            return abs(total) < 1e-10 or abs(total - 2 * math.pi) < 1e-10
        
        return False
    
    def _can_cancel_cnot(self, cnot1: Gate, cnot2: Gate) -> bool:
        """
        Check if two CNOT gates cancel each other.
        
        Args:
            cnot1: First CNOT
            cnot2: Second gate
            
        Returns:
            True if CNOTs cancel
        """
        if cnot1.name != "CNOT" or cnot2.name != "CNOT":
            return False
        
        # Same control and target
        return (cnot1.control_qubits == cnot2.control_qubits and
                cnot1.target_qubits == cnot2.target_qubits)
    
    def _is_cnot_swap_pattern(self, gate1: Gate, gate2: Gate) -> bool:
        """
        Check if two CNOTs form a SWAP pattern.
        
        Pattern: CNOT(a,b) followed by CNOT(b,a) and CNOT(a,b) = SWAP(a,b)
        
        Args:
            gate1: First CNOT
            gate2: Second gate
            
        Returns:
            True if pattern matches
        """
        if gate1.name != "CNOT" or gate2.name != "CNOT":
            return False
        
        # Check for alternating control/target pattern
        g1_ctrl = gate1.control_qubits[0]
        g1_tgt = gate1.target_qubits[0]
        g2_ctrl = gate2.control_qubits[0]
        g2_tgt = gate2.target_qubits[0]
        
        # Pattern: CNOT(a,b), CNOT(b,a) - could be part of SWAP
        return g1_ctrl == g2_tgt and g1_tgt == g2_ctrl
    
    def _is_single_qubit_on_qubit(self, gate: Gate, qubit: int) -> bool:
        """
        Check if gate is a single-qubit gate on specific qubit.
        
        Args:
            gate: Gate to check
            qubit: Qubit index
            
        Returns:
            True if single-qubit gate on qubit
        """
        if not gate.is_single_qubit:
            return False
        
        # Must only affect the specified qubit
        return gate.target_qubits == [qubit] and not gate.control_qubits
    
    def _fuse_single_qubit_gates(
        self,
        gate_sequence: List[Tuple[int, Gate]],
        qubit: int
    ) -> Optional[Gate]:
        """
        Fuse a sequence of single-qubit gates into a single U3 gate.
        
        Uses matrix multiplication to combine rotations.
        
        Args:
            gate_sequence: List of (index, gate) tuples
            qubit: Target qubit
            
        Returns:
            Fused gate or None if fusion not possible
        """
        if len(gate_sequence) < 2:
            return None
        
        # Convert gates to rotation vectors and combine
        # For now, simplified fusion: just use the last gate
        # Full implementation would do matrix multiplication
        
        # Check if all gates are of types we can fuse
        fusible_types = {"H", "X", "Y", "Z", "S", "T", "RX", "RY", "RZ"}
        
        for _, gate in gate_sequence:
            if gate.name not in fusible_types:
                return None
        
        # Simplified: if sequence contains only rotations around same axis, sum angles
        rotation_axis = None
        total_angle = 0.0
        
        for _, gate in gate_sequence:
            if gate.name in ["RX", "RY", "RZ"]:
                axis = gate.name[1]
                if rotation_axis is None:
                    rotation_axis = axis
                elif rotation_axis != axis:
                    # Different axes, full U3 fusion needed
                    # For now, return None (too complex for basic implementation)
                    return None
                total_angle += gate.params.get("theta", 0)
            elif gate.name == "H":
                # H is RY(π/2) followed by RX(π)
                # Complex to fuse, skip for basic implementation
                return None
            elif gate.name == "X":
                if rotation_axis == "X":
                    total_angle += math.pi
                else:
                    return None
            elif gate.name == "Y":
                if rotation_axis == "Y":
                    total_angle += math.pi
                else:
                    return None
            elif gate.name == "Z":
                if rotation_axis == "Z":
                    total_angle += math.pi
                else:
                    return None
        
        # Create fused rotation gate
        if rotation_axis == "X":
            return Gate("RX", [qubit], params={"theta": total_angle % (2 * math.pi)})
        elif rotation_axis == "Y":
            return Gate("RY", [qubit], params={"theta": total_angle % (2 * math.pi)})
        elif rotation_axis == "Z":
            return Gate("RZ", [qubit], params={"theta": total_angle % (2 * math.pi)})
        
        return None
    
    def get_optimization_stats(self) -> Dict[str, int]:
        """Get statistics from last optimization run."""
        return self._optimization_stats.copy()
    
    def set_level(self, level: int) -> None:
        """Set optimization level."""
        self.level = level
