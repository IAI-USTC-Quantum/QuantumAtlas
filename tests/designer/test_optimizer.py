"""Tests for circuit optimizer."""

import pytest
import math
from atlas.designer.quantum_circuit import QuantumCircuit
from atlas.designer.optimizer import CircuitOptimizer, OptimizationLevel


class TestCircuitOptimizer:
    """Test CircuitOptimizer class."""

    def test_no_optimization(self):
        """Test O0 optimization level."""
        qc = QuantumCircuit(num_qubits=2)
        qc.h(0)
        qc.x(1)

        optimizer = CircuitOptimizer(level=OptimizationLevel.O0)
        result = optimizer.optimize(qc)

        assert len(result.gates) == len(qc.gates)

    def test_hadamard_cancellation(self):
        """Test consecutive H gate cancellation."""
        qc = QuantumCircuit(num_qubits=1)
        qc.h(0)
        qc.h(0)  # Should cancel with previous H

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 0

    def test_pauli_cancellation(self):
        """Test consecutive Pauli gate cancellation."""
        qc = QuantumCircuit(num_qubits=1)
        qc.x(0)
        qc.x(0)  # Should cancel

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 0

    def test_rotation_cancellation(self):
        """Test rotation angle cancellation."""
        qc = QuantumCircuit(num_qubits=1)
        qc.rx(0, math.pi)
        qc.rx(0, math.pi)  # 2*pi = 0, should cancel

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 0

    def test_rotation_sum_to_2pi(self):
        """Test rotations summing to 2*pi."""
        qc = QuantumCircuit(num_qubits=1)
        qc.rx(0, math.pi)
        qc.rx(0, math.pi)  # Sum = 2*pi

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 0

    def test_no_false_cancellation(self):
        """Test that non-consecutive gates don't cancel."""
        qc = QuantumCircuit(num_qubits=2)
        qc.h(0)
        qc.h(1)  # Different qubit, shouldn't cancel with previous
        qc.h(0)  # Shouldn't cancel with first H

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 3

    def test_cnot_cancellation(self):
        """Test CNOT cancellation at O2."""
        qc = QuantumCircuit(num_qubits=2)
        qc.cnot(0, 1)
        qc.cnot(0, 1)  # Same control and target, should cancel

        optimizer = CircuitOptimizer(level=OptimizationLevel.O2)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 0

    def test_no_cnot_cancellation_different_qubits(self):
        """Test that different CNOTs don't cancel."""
        qc = QuantumCircuit(num_qubits=3)
        qc.cnot(0, 1)
        qc.cnot(1, 2)  # Different control/target

        optimizer = CircuitOptimizer(level=OptimizationLevel.O2)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 2

    def test_optimization_stats(self):
        """Test optimization statistics."""
        qc = QuantumCircuit(num_qubits=1)
        qc.h(0)
        qc.h(0)  # Both H gates will be cancelled (2 gates removed)
        qc.x(0)

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        stats = optimizer.get_optimization_stats()
        assert stats["gates_removed"] == 2  # Both H gates are removed (they cancel each other)

    def test_depth_reduction(self):
        """Test that optimization reduces depth."""
        qc = QuantumCircuit(num_qubits=1)
        qc.h(0)
        qc.h(0)  # These cancel, reducing depth
        qc.x(0)

        initial_depth = qc.depth

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        # Original: H-H-X, depth = 3
        # Optimized: X, depth = 1
        assert result.depth < initial_depth

    def test_barrier_preservation(self):
        """Test that barriers are preserved."""
        qc = QuantumCircuit(num_qubits=2)
        qc.h(0)
        qc.barrier()
        qc.x(0)

        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        result = optimizer.optimize(qc)

        # Barrier should still be there
        barrier_gates = [g for g in result.gates if g.name == "BARRIER"]
        assert len(barrier_gates) == 1

    def test_empty_circuit(self):
        """Test optimizing empty circuit."""
        qc = QuantumCircuit(num_qubits=2)

        optimizer = CircuitOptimizer(level=OptimizationLevel.O3)
        result = optimizer.optimize(qc)

        assert len(result.gates) == 0
        assert result.depth == 0

    def test_complex_circuit(self):
        """Test optimizing a more complex circuit."""
        qc = QuantumCircuit(num_qubits=3, name="bell_plus")
        qc.h(0)
        qc.h(0)  # Cancel
        qc.cnot(0, 1)
        qc.cnot(0, 1)  # Cancel at O2
        qc.h(2)
        qc.x(2)

        optimizer = CircuitOptimizer(level=OptimizationLevel.O2)
        result = optimizer.optimize(qc)

        # Should have H on q2, X on q2 (possibly more optimization)
        assert result.gate_count <= 4

    def test_set_level(self):
        """Test changing optimization level."""
        optimizer = CircuitOptimizer(level=OptimizationLevel.O1)
        assert optimizer.level == 1

        optimizer.set_level(OptimizationLevel.O3)
        assert optimizer.level == 3
