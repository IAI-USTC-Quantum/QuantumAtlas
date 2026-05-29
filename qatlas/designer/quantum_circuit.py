"""
Quantum Circuit Data Model

Defines the QuantumCircuit class for representing quantum circuits with
standard quantum gates and operations.
"""

from typing import Any, Dict, List, Optional, Set, Tuple, Union
from dataclasses import dataclass, field
from copy import deepcopy
from enum import Enum


class GateType(Enum):
    """Standard quantum gate types."""
    H = "H"  # Hadamard
    X = "X"  # Pauli-X
    Y = "Y"  # Pauli-Y
    Z = "Z"  # Pauli-Z
    S = "S"  # Phase
    T = "T"  # T gate
    CNOT = "CNOT"  # Controlled-NOT
    CZ = "CZ"  # Controlled-Z
    RX = "RX"  # Rotation X
    RY = "RY"  # Rotation Y
    RZ = "RZ"  # Rotation Z
    SWAP = "SWAP"  # Swap
    MEASURE = "MEASURE"  # Measurement
    BARRIER = "BARRIER"  # Barrier (for visualization/optimization)


# Single qubit gates
SINGLE_QUBIT_GATES = {GateType.H, GateType.X, GateType.Y, GateType.Z, GateType.S, GateType.T, GateType.RX, GateType.RY, GateType.RZ}

# Two qubit gates
TWO_QUBIT_GATES = {GateType.CNOT, GateType.CZ, GateType.SWAP}


@dataclass
class Gate:
    """
    Represents a quantum gate operation.
    
    Attributes:
        name: Gate name (H, X, Y, Z, S, T, CNOT, CZ, RX, RY, RZ, SWAP, MEASURE)
        target_qubits: List of target qubit indices
        control_qubits: List of control qubit indices (for controlled gates)
        params: Gate parameters (e.g., rotation angles for RX, RY, RZ)
        classical_bits: Classical bit indices for measurement results
    """
    name: str
    target_qubits: List[int]
    control_qubits: List[int] = field(default_factory=list)
    params: Dict[str, float] = field(default_factory=dict)
    classical_bits: List[int] = field(default_factory=list)
    
    def __post_init__(self):
        """Validate gate after initialization."""
        # Ensure qubit indices are non-negative
        for q in self.target_qubits:
            if q < 0:
                raise ValueError(f"Qubit index must be non-negative, got {q}")
        for q in self.control_qubits:
            if q < 0:
                raise ValueError(f"Control qubit index must be non-negative, got {q}")
        
        # Validate gate-specific constraints
        if self.name in ["CNOT", "CZ"]:
            if len(self.control_qubits) != 1:
                raise ValueError(f"{self.name} requires exactly 1 control qubit")
            if len(self.target_qubits) != 1:
                raise ValueError(f"{self.name} requires exactly 1 target qubit")
        elif self.name == "SWAP":
            if len(self.target_qubits) != 2:
                raise ValueError("SWAP requires exactly 2 qubits")
        elif self.name in ["RX", "RY", "RZ"]:
            if "theta" not in self.params:
                raise ValueError(f"{self.name} requires 'theta' parameter")
        elif self.name == "MEASURE":
            if len(self.classical_bits) != len(self.target_qubits):
                raise ValueError("MEASURE requires same number of classical bits as target qubits")
    
    @property
    def is_single_qubit(self) -> bool:
        """Check if this is a single qubit gate."""
        return self.name in ["H", "X", "Y", "Z", "S", "T", "RX", "RY", "RZ"]
    
    @property
    def is_two_qubit(self) -> bool:
        """Check if this is a two qubit gate."""
        return self.name in ["CNOT", "CZ", "SWAP"]
    
    @property
    def is_controlled(self) -> bool:
        """Check if this is a controlled gate."""
        return len(self.control_qubits) > 0
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert gate to dictionary."""
        return {
            "name": self.name,
            "target_qubits": self.target_qubits,
            "control_qubits": self.control_qubits,
            "params": self.params,
            "classical_bits": self.classical_bits,
        }
    
    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "Gate":
        """Create gate from dictionary."""
        return cls(
            name=data["name"],
            target_qubits=data["target_qubits"],
            control_qubits=data.get("control_qubits", []),
            params=data.get("params", {}),
            classical_bits=data.get("classical_bits", []),
        )
    
    def __eq__(self, other) -> bool:
        """Check equality with another gate."""
        if not isinstance(other, Gate):
            return False
        return (
            self.name == other.name
            and self.target_qubits == other.target_qubits
            and self.control_qubits == other.control_qubits
            and self.params == other.params
        )
    
    def __repr__(self) -> str:
        """String representation of gate."""
        if self.params:
            param_str = f"({', '.join(f'{k}={v:.4f}' for k, v in self.params.items())})"
        else:
            param_str = ""
        
        if self.control_qubits:
            ctrl_str = f"ctrl={self.control_qubits}, "
        else:
            ctrl_str = ""
        
        return f"Gate({self.name}{param_str}, {ctrl_str}targets={self.target_qubits})"


class QuantumCircuit:
    """
    Quantum Circuit representation.
    
    Represents a quantum circuit with qubits, gates, and classical bits.
    Supports standard quantum gates and provides methods for circuit manipulation.
    
    Attributes:
        num_qubits: Number of quantum qubits
        num_clbits: Number of classical bits
        gates: List of gates in the circuit
        qubit_labels: Optional labels for qubits
        name: Circuit name
    """
    
    def __init__(
        self,
        num_qubits: int,
        num_clbits: int = 0,
        name: str = "",
        qubit_labels: Optional[List[str]] = None,
    ):
        """
        Initialize quantum circuit.
        
        Args:
            num_qubits: Number of quantum qubits
            num_clbits: Number of classical bits
            name: Circuit name
            qubit_labels: Optional labels for qubits
        """
        if num_qubits < 0:
            raise ValueError("Number of qubits must be non-negative")
        if num_clbits < 0:
            raise ValueError("Number of classical bits must be non-negative")
        
        self.num_qubits = num_qubits
        self.num_clbits = num_clbits
        self.name = name
        self.gates: List[Gate] = []
        self.qubit_labels = qubit_labels or [f"q{i}" for i in range(num_qubits)]
        
        # Qubit mapping for logical to physical translation
        self._logical_to_physical: Dict[int, int] = {i: i for i in range(num_qubits)}
    
    # === Gate Addition Methods ===
    
    def h(self, qubit: int) -> "QuantumCircuit":
        """Add Hadamard gate."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("H", [qubit]))
        return self
    
    def x(self, qubit: int) -> "QuantumCircuit":
        """Add Pauli-X gate."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("X", [qubit]))
        return self
    
    def y(self, qubit: int) -> "QuantumCircuit":
        """Add Pauli-Y gate."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("Y", [qubit]))
        return self
    
    def z(self, qubit: int) -> "QuantumCircuit":
        """Add Pauli-Z gate."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("Z", [qubit]))
        return self
    
    def s(self, qubit: int) -> "QuantumCircuit":
        """Add Phase (S) gate."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("S", [qubit]))
        return self
    
    def t(self, qubit: int) -> "QuantumCircuit":
        """Add T gate."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("T", [qubit]))
        return self
    
    def rx(self, qubit: int, theta: float) -> "QuantumCircuit":
        """Add rotation around X-axis."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("RX", [qubit], params={"theta": theta}))
        return self
    
    def ry(self, qubit: int, theta: float) -> "QuantumCircuit":
        """Add rotation around Y-axis."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("RY", [qubit], params={"theta": theta}))
        return self
    
    def rz(self, qubit: int, theta: float) -> "QuantumCircuit":
        """Add rotation around Z-axis."""
        self._validate_qubit(qubit)
        self.gates.append(Gate("RZ", [qubit], params={"theta": theta}))
        return self
    
    def cnot(self, control: int, target: int) -> "QuantumCircuit":
        """Add Controlled-NOT gate."""
        self._validate_qubit(control)
        self._validate_qubit(target)
        if control == target:
            raise ValueError("Control and target qubits must be different")
        self.gates.append(Gate("CNOT", [target], control_qubits=[control]))
        return self
    
    def cz(self, control: int, target: int) -> "QuantumCircuit":
        """Add Controlled-Z gate."""
        self._validate_qubit(control)
        self._validate_qubit(target)
        if control == target:
            raise ValueError("Control and target qubits must be different")
        self.gates.append(Gate("CZ", [target], control_qubits=[control]))
        return self
    
    def swap(self, qubit1: int, qubit2: int) -> "QuantumCircuit":
        """Add SWAP gate."""
        self._validate_qubit(qubit1)
        self._validate_qubit(qubit2)
        if qubit1 == qubit2:
            raise ValueError("SWAP qubits must be different")
        self.gates.append(Gate("SWAP", [qubit1, qubit2]))
        return self
    
    def measure(self, qubit: int, clbit: int) -> "QuantumCircuit":
        """Add measurement operation."""
        self._validate_qubit(qubit)
        self._validate_clbit(clbit)
        self.gates.append(Gate("MEASURE", [qubit], classical_bits=[clbit]))
        return self
    
    def barrier(self, qubits: Optional[List[int]] = None) -> "QuantumCircuit":
        """Add barrier (for optimization/visualization)."""
        if qubits is None:
            qubits = list(range(self.num_qubits))
        for q in qubits:
            self._validate_qubit(q)
        self.gates.append(Gate("BARRIER", qubits))
        return self
    
    def add_gate(self, gate: Gate) -> "QuantumCircuit":
        """Add a gate object directly."""
        # Validate qubits
        for q in gate.target_qubits:
            self._validate_qubit(q)
        for q in gate.control_qubits:
            self._validate_qubit(q)
        for c in gate.classical_bits:
            self._validate_clbit(c)
        self.gates.append(gate)
        return self
    
    # === Utility Methods ===
    
    def _validate_qubit(self, qubit: int) -> None:
        """Validate qubit index."""
        if qubit < 0 or qubit >= self.num_qubits:
            raise ValueError(f"Qubit index {qubit} out of range [0, {self.num_qubits})")
    
    def _validate_clbit(self, clbit: int) -> None:
        """Validate classical bit index."""
        if clbit < 0 or clbit >= self.num_clbits:
            raise ValueError(f"Classical bit index {clbit} out of range [0, {self.num_clbits})")
    
    def copy(self) -> "QuantumCircuit":
        """Create a deep copy of the circuit."""
        new_circuit = QuantumCircuit(
            num_qubits=self.num_qubits,
            num_clbits=self.num_clbits,
            name=self.name,
            qubit_labels=self.qubit_labels.copy(),
        )
        new_circuit.gates = [Gate.from_dict(g.to_dict()) for g in self.gates]
        new_circuit._logical_to_physical = self._logical_to_physical.copy()
        return new_circuit
    
    # === Circuit Properties ===
    
    @property
    def gate_count(self) -> int:
        """Total number of gates (excluding barriers)."""
        return len([g for g in self.gates if g.name != "BARRIER"])
    
    def gate_counts_by_type(self) -> Dict[str, int]:
        """Get gate counts by gate type."""
        counts = {}
        for gate in self.gates:
            if gate.name != "BARRIER":
                counts[gate.name] = counts.get(gate.name, 0) + 1
        return counts
    
    @property
    def depth(self) -> int:
        """
        Calculate circuit depth.
        
        Depth is the number of time steps required to execute the circuit,
        assuming gates on different qubits can execute in parallel.
        """
        if not self.gates:
            return 0
        
        # Track the current depth for each qubit
        qubit_depths = [0] * self.num_qubits
        
        for gate in self.gates:
            if gate.name == "BARRIER":
                continue
            
            # Get all involved qubits
            involved_qubits = set(gate.target_qubits + gate.control_qubits)
            
            # Find the maximum depth among involved qubits
            max_depth = max(qubit_depths[q] for q in involved_qubits)
            
            # Update depths for all involved qubits
            for q in involved_qubits:
                qubit_depths[q] = max_depth + 1
        
        return max(qubit_depths)
    
    def qubits_used(self) -> Set[int]:
        """Get set of qubits that have gates applied to them."""
        used = set()
        for gate in self.gates:
            used.update(gate.target_qubits)
            used.update(gate.control_qubits)
        return used
    
    def num_nonlocal_gates(self) -> int:
        """Count number of non-local (multi-qubit) gates."""
        return len([g for g in self.gates if g.is_two_qubit])
    
    # === Qubit Mapping ===
    
    def set_qubit_mapping(self, mapping: Dict[int, int]) -> None:
        """Set logical to physical qubit mapping."""
        if set(mapping.keys()) != set(range(self.num_qubits)):
            raise ValueError("Mapping must cover all qubits")
        if set(mapping.values()) != set(range(self.num_qubits)):
            raise ValueError("Mapping must be a permutation")
        self._logical_to_physical = mapping.copy()
    
    def get_physical_qubit(self, logical_qubit: int) -> int:
        """Get physical qubit index from logical qubit index."""
        return self._logical_to_physical.get(logical_qubit, logical_qubit)
    
    # === Serialization ===
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert circuit to dictionary."""
        return {
            "name": self.name,
            "num_qubits": self.num_qubits,
            "num_clbits": self.num_clbits,
            "qubit_labels": self.qubit_labels,
            "gates": [g.to_dict() for g in self.gates],
            "qubit_mapping": self._logical_to_physical,
        }
    
    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "QuantumCircuit":
        """Create circuit from dictionary."""
        circuit = cls(
            num_qubits=data["num_qubits"],
            num_clbits=data.get("num_clbits", 0),
            name=data.get("name", ""),
            qubit_labels=data.get("qubit_labels"),
        )
        circuit.gates = [Gate.from_dict(g) for g in data.get("gates", [])]
        circuit._logical_to_physical = data.get("qubit_mapping", {i: i for i in range(circuit.num_qubits)})
        return circuit
    
    # === Visualization ===
    
    def to_ascii(self, max_width: int = 80) -> str:
        """
        Generate ASCII representation of the circuit.
        
        Args:
            max_width: Maximum width of the output
            
        Returns:
            ASCII string representation
        """
        if not self.gates:
            lines = [f"q{i}: ─────" for i in range(self.num_qubits)]
            return "\n".join(lines)
        
        # Initialize qubit lines
        lines = [[f"q{i}: "] for i in range(self.num_qubits)]
        
        for gate in self.gates:
            if gate.name == "BARRIER":
                # Add barrier
                for i in range(self.num_qubits):
                    lines[i].append("│")
                continue
            
            # Determine gate symbol and width
            if gate.name == "CNOT":
                symbol = "⊕"
                ctrl_symbol = "●"
            elif gate.name == "CZ":
                symbol = "■"
                ctrl_symbol = "●"
            elif gate.name == "SWAP":
                symbol = "×"
            elif gate.name == "MEASURE":
                symbol = "M"
            else:
                symbol = gate.name
            
            # Get all involved qubits
            all_qubits = sorted(set(gate.target_qubits + gate.control_qubits))
            min_q = min(all_qubits)
            max_q = max(all_qubits)
            
            # Add gate to lines
            if gate.is_two_qubit and gate.name != "SWAP":
                # Controlled gate
                ctrl = gate.control_qubits[0]
                tgt = gate.target_qubits[0]
                
                for i in range(self.num_qubits):
                    if i == ctrl:
                        lines[i].append(ctrl_symbol)
                    elif i == tgt:
                        lines[i].append(symbol)
                    elif min_q < i < max_q:
                        lines[i].append("│")
                    else:
                        lines[i].append("─")
            elif gate.name == "SWAP":
                for i in range(self.num_qubits):
                    if i in gate.target_qubits:
                        lines[i].append(symbol)
                    elif min_q < i < max_q:
                        lines[i].append("│")
                    else:
                        lines[i].append("─")
            else:
                # Single qubit gate
                for i in range(self.num_qubits):
                    if i in gate.target_qubits:
                        if gate.params:
                            param_str = f"{symbol}({gate.params.get('theta', 0):.2f})"
                            lines[i].append(param_str)
                        else:
                            lines[i].append(f"─{symbol}─")
                    else:
                        lines[i].append("───")
        
        # Join each qubit's line
        result_lines = []
        for line in lines:
            line_str = "".join(line)
            # Truncate if too long
            if len(line_str) > max_width:
                line_str = line_str[:max_width - 3] + "..."
            result_lines.append(line_str)
        
        return "\n".join(result_lines)
    
    def __repr__(self) -> str:
        """String representation."""
        return f"QuantumCircuit({self.num_qubits}q, {self.num_clbits}c, {self.gate_count} gates, depth={self.depth})"
    
    def __len__(self) -> int:
        """Number of gates."""
        return len(self.gates)
