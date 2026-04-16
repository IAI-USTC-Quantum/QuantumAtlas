"""
Resource Analyzer Module

Analyzes quantum circuits to extract resource requirements including:
- Gate counts by type
- Circuit depth
- Qubit and classical bit counts
- T-gate count (important for fault-tolerant computing)
- Parallelism metrics
- Execution time estimates
"""

from typing import Any, Dict, List, Optional, Set, Tuple
from dataclasses import dataclass, field
from collections import defaultdict

from atlas.designer.quantum_circuit import QuantumCircuit, Gate


@dataclass
class ResourceStats:
    """Container for resource analysis statistics."""
    # Basic counts
    total_gates: int = 0
    single_qubit_gates: int = 0
    two_qubit_gates: int = 0
    measurement_gates: int = 0
    
    # Qubit info
    num_qubits: int = 0
    num_clbits: int = 0
    qubits_used: int = 0
    
    # Depth
    depth: int = 0
    
    # Advanced metrics
    t_gates: int = 0
    parallelism: float = 0.0  # Ratio of parallelizable operations
    estimated_time_ms: Optional[float] = None
    
    # Gate breakdown by type
    gate_counts: Dict[str, int] = field(default_factory=dict)
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary."""
        return {
            "total_gates": self.total_gates,
            "single_qubit_gates": self.single_qubit_gates,
            "two_qubit_gates": self.two_qubit_gates,
            "measurement_gates": self.measurement_gates,
            "num_qubits": self.num_qubits,
            "num_clbits": self.num_clbits,
            "qubits_used": self.qubits_used,
            "depth": self.depth,
            "t_gates": self.t_gates,
            "parallelism": self.parallelism,
            "estimated_time_ms": self.estimated_time_ms,
            "gate_counts": self.gate_counts,
        }


class ResourceAnalyzer:
    """
    Analyzes quantum circuits to estimate resource requirements.
    
    Provides methods to:
    - Count gates by type
    - Calculate circuit depth
    - Analyze qubit usage
    - Estimate execution metrics
    """
    
    # Single qubit gates
    SINGLE_QUBIT_GATES = {"H", "X", "Y", "Z", "S", "T", "RX", "RY", "RZ"}
    
    # Two qubit gates
    TWO_QUBIT_GATES = {"CNOT", "CZ", "SWAP"}
    
    def __init__(self):
        """Initialize the resource analyzer."""
        pass
    
    def analyze(self, circuit: QuantumCircuit) -> ResourceStats:
        """
        Perform complete resource analysis on a circuit.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            ResourceStats containing all analysis results
        """
        stats = ResourceStats()
        
        # Basic counts
        stats.num_qubits = circuit.num_qubits
        stats.num_clbits = circuit.num_clbits
        stats.qubits_used = len(circuit.qubits_used())
        
        # Gate analysis
        gate_stats = self.analyze_gates(circuit)
        stats.total_gates = gate_stats["total"]
        stats.single_qubit_gates = gate_stats["single_qubit"]
        stats.two_qubit_gates = gate_stats["two_qubit"]
        stats.measurement_gates = gate_stats["measurement"]
        stats.gate_counts = gate_stats["by_type"]
        
        # Depth analysis
        stats.depth = self.analyze_depth(circuit)
        
        # Advanced analysis
        stats.t_gates = self.count_t_gates(circuit)
        stats.parallelism = self.calculate_parallelism(circuit)
        
        return stats
    
    def analyze_gates(self, circuit: QuantumCircuit) -> Dict[str, Any]:
        """
        Analyze gate counts by type.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Dictionary with gate statistics:
            - total: Total number of gates
            - single_qubit: Number of single-qubit gates
            - two_qubit: Number of two-qubit gates
            - measurement: Number of measurement operations
            - by_type: Breakdown by gate type
        """
        stats = {
            "total": 0,
            "single_qubit": 0,
            "two_qubit": 0,
            "measurement": 0,
            "by_type": defaultdict(int),
        }
        
        for gate in circuit.gates:
            # Skip barriers
            if gate.name == "BARRIER":
                continue
            
            stats["total"] += 1
            stats["by_type"][gate.name] += 1
            
            if gate.name in self.SINGLE_QUBIT_GATES:
                stats["single_qubit"] += 1
            elif gate.name in self.TWO_QUBIT_GATES:
                stats["two_qubit"] += 1
            elif gate.name == "MEASURE":
                stats["measurement"] += 1
        
        # Convert defaultdict to regular dict
        stats["by_type"] = dict(stats["by_type"])
        
        return stats
    
    def analyze_depth(self, circuit: QuantumCircuit) -> int:
        """
        Calculate circuit depth (critical path length).
        
        Depth is the number of time steps required to execute the circuit,
        assuming gates on different qubits can execute in parallel.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Circuit depth as integer
        """
        if not circuit.gates:
            return 0
        
        # Track the current depth for each qubit
        qubit_depths = [0] * circuit.num_qubits
        
        for gate in circuit.gates:
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
    
    def count_qubits(self, circuit: QuantumCircuit) -> int:
        """
        Get the number of quantum qubits in the circuit.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Number of quantum qubits
        """
        return circuit.num_qubits
    
    def count_clbits(self, circuit: QuantumCircuit) -> int:
        """
        Get the number of classical bits in the circuit.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Number of classical bits
        """
        return circuit.num_clbits
    
    def count_t_gates(self, circuit: QuantumCircuit) -> int:
        """
        Count T-gates in the circuit.
        
        T-gates are crucial for fault-tolerant quantum computing as they
        are typically the most expensive gates to implement fault-tolerantly.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Number of T-gates
        """
        count = 0
        for gate in circuit.gates:
            if gate.name == "T":
                count += 1
        return count
    
    def count_two_qubit_gates(self, circuit: QuantumCircuit) -> int:
        """
        Count two-qubit gates in the circuit.
        
        Two-qubit gates are typically the noisiest and slowest operations
        on near-term quantum hardware.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Number of two-qubit gates
        """
        count = 0
        for gate in circuit.gates:
            if gate.name in self.TWO_QUBIT_GATES:
                count += 1
        return count
    
    def calculate_parallelism(self, circuit: QuantumCircuit) -> float:
        """
        Calculate circuit parallelism ratio.
        
        Parallelism is defined as the ratio of total gates to circuit depth,
        representing the average number of gates that can be executed in parallel.
        A higher value indicates more potential for parallel execution.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            Parallelism ratio (total_gates / depth), or 0 if depth is 0
        """
        total_gates = sum(
            1 for g in circuit.gates 
            if g.name != "BARRIER"
        )
        depth = self.analyze_depth(circuit)
        
        if depth == 0:
            return 0.0
        
        return total_gates / depth
    
    def estimate_execution_time(
        self, 
        circuit: QuantumCircuit,
        gate_time: float = 50.0,  # ns
        two_qubit_gate_time: Optional[float] = None,
        measurement_time: float = 300.0,  # ns
        coherence_time: Optional[float] = None,  # us
    ) -> Dict[str, Any]:
        """
        Estimate circuit execution time on a quantum device.
        
        Args:
            circuit: The quantum circuit to analyze
            gate_time: Default gate execution time in nanoseconds
            two_qubit_gate_time: Two-qubit gate time (defaults to 2x gate_time)
            measurement_time: Measurement operation time in nanoseconds
            coherence_time: Qubit coherence time in microseconds (for fidelity warning)
            
        Returns:
            Dictionary with timing estimates:
            - total_time_ns: Total execution time in nanoseconds
            - total_time_ms: Total execution time in milliseconds
            - gate_time_ns: Time spent on single-qubit gates
            - two_qubit_time_ns: Time spent on two-qubit gates
            - measurement_time_ns: Time spent on measurements
            - coherence_limited: Whether execution time exceeds coherence time
        """
        if two_qubit_gate_time is None:
            two_qubit_gate_time = gate_time * 2
        
        single_qubit_count = 0
        two_qubit_count = 0
        measurement_count = 0
        
        for gate in circuit.gates:
            if gate.name == "BARRIER":
                continue
            elif gate.name in self.SINGLE_QUBIT_GATES:
                single_qubit_count += 1
            elif gate.name in self.TWO_QUBIT_GATES:
                two_qubit_count += 1
            elif gate.name == "MEASURE":
                measurement_count += 1
        
        gate_time_ns = single_qubit_count * gate_time
        two_qubit_time_ns = two_qubit_count * two_qubit_gate_time
        measurement_time_ns = measurement_count * measurement_time
        
        total_time_ns = gate_time_ns + two_qubit_time_ns + measurement_time_ns
        total_time_ms = total_time_ns / 1_000_000
        
        # Check coherence time limit
        coherence_limited = False
        if coherence_time is not None:
            coherence_time_ns = coherence_time * 1000  # Convert us to ns
            coherence_limited = total_time_ns > coherence_time_ns
        
        return {
            "total_time_ns": total_time_ns,
            "total_time_ms": total_time_ms,
            "gate_time_ns": gate_time_ns,
            "two_qubit_time_ns": two_qubit_time_ns,
            "measurement_time_ns": measurement_time_ns,
            "coherence_limited": coherence_limited,
            "parameters": {
                "gate_time_ns": gate_time,
                "two_qubit_gate_time_ns": two_qubit_gate_time,
                "measurement_time_ns": measurement_time,
                "coherence_time_us": coherence_time,
            },
        }