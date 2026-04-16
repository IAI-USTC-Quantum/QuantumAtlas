"""
Quantum IR Module

Defines QuantumIR - the intermediate representation for quantum circuits.
Provides serialization and export to various formats.
"""

import json
from typing import Any, Dict, List, Optional
from dataclasses import dataclass, field, asdict
from datetime import datetime

from .quantum_circuit import QuantumCircuit


@dataclass
class QuantumIR:
    """
    Quantum Intermediate Representation.
    
    Represents a quantum circuit with metadata about its generation.
    
    Attributes:
        circuit: The quantum circuit
        algorithm_id: ID of the algorithm this circuit implements
        optimization_level: Optimization level applied (0-3)
        metadata: Additional metadata
        version: IR schema version
        created_at: Creation timestamp
    """
    circuit: QuantumCircuit
    algorithm_id: str = ""
    optimization_level: int = 0
    metadata: Dict[str, Any] = field(default_factory=dict)
    version: str = "1.0.0"
    created_at: str = field(default_factory=lambda: datetime.now().isoformat())
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary."""
        return {
            "circuit": self.circuit.to_dict(),
            "algorithm_id": self.algorithm_id,
            "optimization_level": self.optimization_level,
            "metadata": self.metadata,
            "version": self.version,
            "created_at": self.created_at,
        }
    
    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "QuantumIR":
        """Create from dictionary."""
        circuit = QuantumCircuit.from_dict(data["circuit"])
        return cls(
            circuit=circuit,
            algorithm_id=data.get("algorithm_id", ""),
            optimization_level=data.get("optimization_level", 0),
            metadata=data.get("metadata", {}),
            version=data.get("version", "1.0.0"),
            created_at=data.get("created_at", datetime.now().isoformat()),
        )
    
    def to_json(self, indent: int = 2) -> str:
        """
        Serialize to JSON string.
        
        Args:
            indent: Indentation level for pretty printing
            
        Returns:
            JSON string
        """
        return json.dumps(self.to_dict(), indent=indent, ensure_ascii=False)
    
    @classmethod
    def from_json(cls, json_str: str) -> "QuantumIR":
        """
        Deserialize from JSON string.
        
        Args:
            json_str: JSON string
            
        Returns:
            QuantumIR instance
        """
        data = json.loads(json_str)
        return cls.from_dict(data)
    
    def save(self, filepath: str) -> None:
        """
        Save to JSON file.
        
        Args:
            filepath: Path to save file
        """
        with open(filepath, 'w', encoding='utf-8') as f:
            f.write(self.to_json())
    
    @classmethod
    def load(cls, filepath: str) -> "QuantumIR":
        """
        Load from JSON file.
        
        Args:
            filepath: Path to load file
            
        Returns:
            QuantumIR instance
        """
        with open(filepath, 'r', encoding='utf-8') as f:
            return cls.from_json(f.read())
    
    # === Export Methods ===
    
    def to_qpanda_dict(self) -> Dict[str, Any]:
        """
        Export to QPanda-compatible dictionary format.
        
        Returns:
            Dictionary in QPanda format
        """
        qpanda_gates = []
        
        for gate in self.circuit.gates:
            qpanda_gate = self._convert_gate_to_qpanda(gate)
            if qpanda_gate:
                qpanda_gates.append(qpanda_gate)
        
        return {
            "qubit_num": self.circuit.num_qubits,
            "cbit_num": self.circuit.num_clbits,
            "gates": qpanda_gates,
            "algorithm_id": self.algorithm_id,
        }
    
    def to_qasm(self) -> str:
        """
        Export to OpenQASM 2.0 format.
        
        Returns:
            QASM string
        """
        lines = [
            "OPENQASM 2.0;",
            'include "qelib1.inc";',
            f"qreg q[{self.circuit.num_qubits}];",
        ]
        
        if self.circuit.num_clbits > 0:
            lines.append(f"creg c[{self.circuit.num_clbits}];")
        
        for gate in self.circuit.gates:
            qasm_line = self._convert_gate_to_qasm(gate)
            if qasm_line:
                lines.append(qasm_line)
        
        return "\n".join(lines)
    
    def to_qiskit_code(self, circuit_name: str = "qc") -> str:
        """
        Generate Qiskit Python code.
        
        Args:
            circuit_name: Variable name for the circuit
            
        Returns:
            Python code string
        """
        lines = [
            "from qiskit import QuantumCircuit",
            "",
            f"# {self.algorithm_id}",
            f"{circuit_name} = QuantumCircuit({self.circuit.num_qubits}, {self.circuit.num_clbits})",
            "",
        ]
        
        for gate in self.circuit.gates:
            code_line = self._convert_gate_to_qiskit(gate, circuit_name)
            if code_line:
                lines.append(code_line)
        
        return "\n".join(lines)
    
    # === Internal Conversion Methods ===
    
    def _convert_gate_to_qpanda(self, gate) -> Optional[Dict[str, Any]]:
        """Convert gate to QPanda format."""
        gate_map = {
            "H": "H",
            "X": "X",
            "Y": "Y",
            "Z": "Z",
            "S": "S",
            "T": "T",
            "RX": "RX",
            "RY": "RY",
            "RZ": "RZ",
            "CNOT": "CNOT",
            "CZ": "CZ",
            "SWAP": "SWAP",
        }
        
        if gate.name not in gate_map:
            return None
        
        result = {
            "name": gate_map[gate.name],
            "targets": gate.target_qubits,
        }
        
        if gate.control_qubits:
            result["controls"] = gate.control_qubits
        
        if gate.params:
            result["params"] = gate.params
        
        return result
    
    def _convert_gate_to_qasm(self, gate) -> Optional[str]:
        """Convert gate to QASM format."""
        if gate.name == "BARRIER":
            return None
        
        # Format qubit indices
        targets = ",".join(f"q[{q}]" for q in gate.target_qubits)
        
        if gate.name == "CNOT":
            ctrl = f"q[{gate.control_qubits[0]}]"
            tgt = f"q[{gate.target_qubits[0]}]"
            return f"cx {ctrl},{tgt};"
        
        elif gate.name == "CZ":
            ctrl = f"q[{gate.control_qubits[0]}]"
            tgt = f"q[{gate.target_qubits[0]}]"
            return f"cz {ctrl},{tgt};"
        
        elif gate.name == "SWAP":
            q1 = f"q[{gate.target_qubits[0]}]"
            q2 = f"q[{gate.target_qubits[1]}]"
            return f"swap {q1},{q2};"
        
        elif gate.name in ["RX", "RY", "RZ"]:
            theta = gate.params.get("theta", 0)
            return f"{gate.name.lower()}({theta}) {targets};"
        
        elif gate.name == "MEASURE":
            q = f"q[{gate.target_qubits[0]}]"
            c = f"c[{gate.classical_bits[0]}]"
            return f"measure {q} -> {c};"
        
        elif gate.name in ["H", "X", "Y", "Z", "S", "T"]:
            return f"{gate.name.lower()} {targets};"
        
        return None
    
    def _convert_gate_to_qiskit(self, gate, circuit_name: str) -> Optional[str]:
        """Convert gate to Qiskit code."""
        if gate.name == "BARRIER":
            return None
        
        targets = ",".join(str(q) for q in gate.target_qubits)
        
        if gate.name == "CNOT":
            ctrl = gate.control_qubits[0]
            tgt = gate.target_qubits[0]
            return f"{circuit_name}.cx({ctrl}, {tgt})"
        
        elif gate.name == "CZ":
            ctrl = gate.control_qubits[0]
            tgt = gate.target_qubits[0]
            return f"{circuit_name}.cz({ctrl}, {tgt})"
        
        elif gate.name == "SWAP":
            q1 = gate.target_qubits[0]
            q2 = gate.target_qubits[1]
            return f"{circuit_name}.swap({q1}, {q2})"
        
        elif gate.name in ["RX", "RY", "RZ"]:
            theta = gate.params.get("theta", 0)
            return f"{circuit_name}.{gate.name.lower()}({theta}, {targets})"
        
        elif gate.name == "MEASURE":
            q = gate.target_qubits[0]
            c = gate.classical_bits[0]
            return f"{circuit_name}.measure({q}, {c})"
        
        elif gate.name in ["H", "X", "Y", "Z", "S", "T"]:
            return f"{circuit_name}.{gate.name.lower()}({targets})"
        
        return None
    
    # === Property Accessors ===
    
    @property
    def gate_count(self) -> int:
        """Total number of gates."""
        return self.circuit.gate_count
    
    @property
    def depth(self) -> int:
        """Circuit depth."""
        return self.circuit.depth
    
    @property
    def num_qubits(self) -> int:
        """Number of qubits."""
        return self.circuit.num_qubits
    
    def get_summary(self) -> Dict[str, Any]:
        """Get summary of the Quantum IR."""
        return {
            "algorithm_id": self.algorithm_id,
            "version": self.version,
            "num_qubits": self.num_qubits,
            "num_clbits": self.circuit.num_clbits,
            "gate_count": self.gate_count,
            "depth": self.depth,
            "optimization_level": self.optimization_level,
            "created_at": self.created_at,
            "gate_counts_by_type": self.circuit.gate_counts_by_type(),
        }
