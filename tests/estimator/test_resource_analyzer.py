"""
Tests for resource_analyzer.py
"""

import pytest
from atlas.designer.quantum_circuit import QuantumCircuit
from atlas.estimator.resource_analyzer import ResourceAnalyzer, ResourceStats


class TestResourceAnalyzer:
    """Test cases for ResourceAnalyzer class."""
    
    @pytest.fixture
    def analyzer(self):
        """Create a ResourceAnalyzer instance."""
        return ResourceAnalyzer()
    
    def test_analyze_empty_circuit(self, analyzer):
        """Test analyzing an empty circuit."""
        circuit = QuantumCircuit(2)
        stats = analyzer.analyze(circuit)
        
        assert stats.total_gates == 0
        assert stats.depth == 0
        assert stats.num_qubits == 2
        assert stats.num_clbits == 0
        assert stats.single_qubit_gates == 0
        assert stats.two_qubit_gates == 0
        assert stats.t_gates == 0
    
    def test_analyze_bell_state(self, analyzer):
        """Test analyzing a Bell state circuit."""
        circuit = QuantumCircuit(2, 2)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        
        stats = analyzer.analyze(circuit)
        
        # Verify counts
        assert stats.total_gates == 4
        assert stats.single_qubit_gates == 1  # H gate
        assert stats.two_qubit_gates == 1  # CNOT gate
        assert stats.measurement_gates == 2
        assert stats.num_qubits == 2
        assert stats.num_clbits == 2
        
        # Verify depth
        # H(0) -> CNOT(0,1) -> MEASURE(0) + MEASURE(1)
        # Layer 1: H(0)
        # Layer 2: CNOT(0,1)
        # Layer 3: MEASURE(0), MEASURE(1)
        assert stats.depth == 3
    
    def test_analyze_ghz_state(self, analyzer):
        """Test analyzing a GHZ state circuit."""
        circuit = QuantumCircuit(3, 3)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.cnot(1, 2)
        
        stats = analyzer.analyze(circuit)
        
        assert stats.total_gates == 3
        assert stats.single_qubit_gates == 1
        assert stats.two_qubit_gates == 2
        assert stats.num_qubits == 3
        
        # Depth: H(0) -> CNOT(0,1) -> CNOT(1,2) = 3 layers
        assert stats.depth == 3
    
    def test_analyze_gates_by_type(self, analyzer):
        """Test gate type counting."""
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.x(0)
        circuit.y(1)
        circuit.z(0)
        circuit.t(1)
        circuit.cnot(0, 1)
        
        gate_stats = analyzer.analyze_gates(circuit)
        
        assert gate_stats["total"] == 6
        assert gate_stats["single_qubit"] == 5
        assert gate_stats["two_qubit"] == 1
        assert gate_stats["by_type"]["H"] == 1
        assert gate_stats["by_type"]["X"] == 1
        assert gate_stats["by_type"]["Y"] == 1
        assert gate_stats["by_type"]["Z"] == 1
        assert gate_stats["by_type"]["T"] == 1
        assert gate_stats["by_type"]["CNOT"] == 1
    
    def test_count_t_gates(self, analyzer):
        """Test T-gate counting."""
        circuit = QuantumCircuit(2)
        circuit.t(0)
        circuit.t(1)
        circuit.h(0)
        circuit.t(0)
        
        t_count = analyzer.count_t_gates(circuit)
        assert t_count == 3
    
    def test_count_two_qubit_gates(self, analyzer):
        """Test two-qubit gate counting."""
        circuit = QuantumCircuit(3)
        circuit.cnot(0, 1)
        circuit.cz(1, 2)
        circuit.swap(0, 2)
        circuit.h(0)
        
        two_qubit_count = analyzer.count_two_qubit_gates(circuit)
        assert two_qubit_count == 3
    
    def test_analyze_depth_parallel(self, analyzer):
        """Test depth calculation with parallel gates."""
        circuit = QuantumCircuit(2)
        # These can execute in parallel (different qubits)
        circuit.h(0)
        circuit.x(1)
        
        depth = analyzer.analyze_depth(circuit)
        # Both gates can execute in parallel, so depth = 1
        assert depth == 1
    
    def test_analyze_depth_sequential(self, analyzer):
        """Test depth calculation with sequential gates."""
        circuit = QuantumCircuit(1)
        circuit.h(0)
        circuit.x(0)
        circuit.z(0)
        
        depth = analyzer.analyze_depth(circuit)
        # All gates on same qubit, so depth = 3
        assert depth == 3
    
    def test_analyze_depth_controlled(self, analyzer):
        """Test depth calculation with controlled gates."""
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.h(1)
        
        depth = analyzer.analyze_depth(circuit)
        # Layer 1: H(0)
        # Layer 2: CNOT(0,1)
        # Layer 3: H(1)
        assert depth == 3
    
    def test_calculate_parallelism(self, analyzer):
        """Test parallelism calculation."""
        # High parallelism circuit
        circuit = QuantumCircuit(4)
        for i in range(4):
            circuit.h(i)
        
        parallelism = analyzer.calculate_parallelism(circuit)
        # 4 gates, depth 1, parallelism = 4.0
        assert parallelism == 4.0
        
        # Sequential circuit
        circuit2 = QuantumCircuit(1)
        circuit2.h(0)
        circuit2.x(0)
        circuit2.z(0)
        
        parallelism2 = analyzer.calculate_parallelism(circuit2)
        # 3 gates, depth 3, parallelism = 1.0
        assert parallelism2 == 1.0
    
    def test_estimate_execution_time(self, analyzer):
        """Test execution time estimation."""
        circuit = QuantumCircuit(2, 2)
        circuit.h(0)  # Single qubit
        circuit.cnot(0, 1)  # Two qubit
        circuit.measure(0, 0)  # Measurement
        
        time_est = analyzer.estimate_execution_time(
            circuit,
            gate_time=50.0,
            two_qubit_gate_time=100.0,
            measurement_time=300.0,
        )
        
        assert time_est["total_time_ns"] == 50.0 + 100.0 + 300.0  # = 450 ns
        assert time_est["gate_time_ns"] == 50.0
        assert time_est["two_qubit_time_ns"] == 100.0
        assert time_est["measurement_time_ns"] == 300.0
        assert time_est["total_time_ms"] == 0.00045
    
    def test_estimate_execution_time_coherence_limited(self, analyzer):
        """Test coherence time checking."""
        circuit = QuantumCircuit(2)
        # Add many gates to exceed coherence time
        for _ in range(100):
            circuit.h(0)
        
        # With 50ns gate time, 100 gates = 5000ns = 5us
        # Coherence time of 4us should be exceeded
        time_est = analyzer.estimate_execution_time(
            circuit,
            gate_time=50.0,
            coherence_time=4.0,  # 4 microseconds
        )
        
        assert time_est["coherence_limited"] is True
    
    def test_estimate_execution_time_within_coherence(self, analyzer):
        """Test coherence time within limits."""
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        time_est = analyzer.estimate_execution_time(
            circuit,
            gate_time=50.0,
            coherence_time=100.0,  # 100 microseconds
        )
        
        assert time_est["coherence_limited"] is False
    
    def test_count_qubits(self, analyzer):
        """Test qubit counting."""
        circuit = QuantumCircuit(5)
        assert analyzer.count_qubits(circuit) == 5
    
    def test_count_clbits(self, analyzer):
        """Test classical bit counting."""
        circuit = QuantumCircuit(3, 7)
        assert analyzer.count_clbits(circuit) == 7
    
    def test_resource_stats_to_dict(self):
        """Test ResourceStats conversion to dict."""
        stats = ResourceStats(
            total_gates=10,
            depth=5,
            num_qubits=3,
            gate_counts={"H": 3, "CNOT": 2},
        )
        
        d = stats.to_dict()
        assert d["total_gates"] == 10
        assert d["depth"] == 5
        assert d["num_qubits"] == 3
        assert d["gate_counts"]["H"] == 3
    
    def test_barrier_ignored(self, analyzer):
        """Test that barriers are ignored in analysis."""
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.barrier()
        circuit.x(1)
        
        stats = analyzer.analyze(circuit)
        
        # Barrier should not count as a gate
        assert stats.total_gates == 2
        # But it shouldn't affect depth calculation
        assert stats.depth == 1  # Both gates on different qubits