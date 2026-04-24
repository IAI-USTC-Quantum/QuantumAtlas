"""Tests for QuantumCircuit data model."""

import pytest
from atlas.designer.quantum_circuit import QuantumCircuit, Gate


class TestQuantumCircuit:
    """Test QuantumCircuit class."""

    def test_empty_circuit(self):
        """Test creating an empty circuit."""
        qc = QuantumCircuit(num_qubits=2, num_clbits=1, name="test")
        assert qc.num_qubits == 2
        assert qc.num_clbits == 1
        assert qc.name == "test"
        assert len(qc.gates) == 0
        assert qc.gate_count == 0
        assert qc.depth == 0

    def test_single_qubit_gates(self):
        """Test adding single qubit gates."""
        qc = QuantumCircuit(num_qubits=2)

        # Add various gates
        qc.h(0)
        qc.x(1)
        qc.y(0)
        qc.z(1)
        qc.s(0)
        qc.t(1)

        assert len(qc.gates) == 6
        assert qc.gate_count == 6

        # Check gate types
        assert qc.gates[0].name == "H"
        assert qc.gates[0].target_qubits == [0]
        assert qc.gates[1].name == "X"
        assert qc.gates[1].target_qubits == [1]

    def test_rotation_gates(self):
        """Test rotation gates with parameters."""
        qc = QuantumCircuit(num_qubits=2)

        qc.rx(0, 1.57)
        qc.ry(1, 0.785)
        qc.rz(0, 3.14)

        assert qc.gates[0].name == "RX"
        assert qc.gates[0].params["theta"] == 1.57
        assert qc.gates[1].params["theta"] == 0.785

    def test_two_qubit_gates(self):
        """Test two qubit gates."""
        qc = QuantumCircuit(num_qubits=3)

        qc.cnot(0, 1)
        qc.cz(1, 2)
        qc.swap(0, 2)

        assert qc.gates[0].name == "CNOT"
        assert qc.gates[0].control_qubits == [0]
        assert qc.gates[0].target_qubits == [1]

        assert qc.gates[1].name == "CZ"
        assert qc.gates[1].control_qubits == [1]

        assert qc.gates[2].name == "SWAP"
        assert qc.gates[2].target_qubits == [0, 2]

    def test_measurement(self):
        """Test measurement operations."""
        qc = QuantumCircuit(num_qubits=2, num_clbits=2)

        qc.h(0)
        qc.measure(0, 0)
        qc.measure(1, 1)

        measure_gates = [g for g in qc.gates if g.name == "MEASURE"]
        assert len(measure_gates) == 2
        assert measure_gates[0].target_qubits == [0]
        assert measure_gates[0].classical_bits == [0]

    def test_gate_count(self):
        """Test gate counting."""
        qc = QuantumCircuit(num_qubits=2)

        qc.h(0)
        qc.x(1)
        qc.cnot(0, 1)
        qc.barrier()

        assert qc.gate_count == 3  # barrier not counted
        counts = qc.gate_counts_by_type()
        assert counts["H"] == 1
        assert counts["X"] == 1
        assert counts["CNOT"] == 1

    def test_depth_calculation(self):
        """Test circuit depth calculation."""
        # Parallel gates should have depth 1
        qc = QuantumCircuit(num_qubits=2)
        qc.h(0)
        qc.h(1)
        assert qc.depth == 1

        # Sequential gates on same qubit increase depth
        qc2 = QuantumCircuit(num_qubits=2)
        qc2.h(0)
        qc2.x(0)
        qc2.z(0)
        assert qc2.depth == 3

        # CNOT increases depth
        qc3 = QuantumCircuit(num_qubits=2)
        qc3.h(0)
        qc3.cnot(0, 1)
        qc3.h(1)
        # H on q0 (depth 1), CNOT (depth 2 for both), H on q1 (depth 3)
        # Result: q0 depth=2, q1 depth=3, total depth=3
        assert qc3.depth == 3

    def test_qubit_validation(self):
        """Test qubit index validation."""
        qc = QuantumCircuit(num_qubits=2)

        with pytest.raises(ValueError, match="out of range"):
            qc.h(2)

        with pytest.raises(ValueError, match="out of range"):
            qc.h(-1)

    def test_cnot_validation(self):
        """Test CNOT validation."""
        qc = QuantumCircuit(num_qubits=2)

        with pytest.raises(ValueError, match="must be different"):
            qc.cnot(0, 0)

    def test_circuit_copy(self):
        """Test circuit copying."""
        qc = QuantumCircuit(num_qubits=2, name="original")
        qc.h(0)
        qc.cnot(0, 1)

        qc_copy = qc.copy()
        assert qc_copy.num_qubits == qc.num_qubits
        assert qc_copy.name == qc.name
        assert len(qc_copy.gates) == len(qc.gates)

        # Modifying copy should not affect original
        qc_copy.x(0)
        assert len(qc.gates) == 2
        assert len(qc_copy.gates) == 3

    def test_serialization(self):
        """Test to_dict/from_dict."""
        qc = QuantumCircuit(num_qubits=2, num_clbits=1, name="test")
        qc.h(0)
        qc.cnot(0, 1)
        qc.measure(1, 0)

        data = qc.to_dict()
        qc2 = QuantumCircuit.from_dict(data)

        assert qc2.num_qubits == qc.num_qubits
        assert qc2.num_clbits == qc.num_clbits
        assert qc2.name == qc.name
        assert len(qc2.gates) == len(qc.gates)
        assert qc2.gates[0].name == "H"
        assert qc2.gates[1].name == "CNOT"

    def test_qubits_used(self):
        """Test getting used qubits."""
        qc = QuantumCircuit(num_qubits=3)
        qc.h(0)
        qc.cnot(0, 1)

        used = qc.qubits_used()
        assert used == {0, 1}

    def test_nonlocal_gates(self):
        """Test counting non-local gates."""
        qc = QuantumCircuit(num_qubits=2)
        qc.h(0)
        qc.cnot(0, 1)
        qc.swap(0, 1)

        assert qc.num_nonlocal_gates() == 2


class TestGate:
    """Test Gate class."""

    def test_gate_creation(self):
        """Test creating gates."""
        g = Gate("H", [0])
        assert g.name == "H"
        assert g.target_qubits == [0]
        assert g.control_qubits == []
        assert g.params == {}

    def test_controlled_gate(self):
        """Test controlled gate creation."""
        g = Gate("CNOT", [1], control_qubits=[0])
        assert g.name == "CNOT"
        assert g.target_qubits == [1]
        assert g.control_qubits == [0]
        assert g.is_controlled

    def test_parameterized_gate(self):
        """Test parameterized gate."""
        g = Gate("RX", [0], params={"theta": 1.57})
        assert g.name == "RX"
        assert g.params["theta"] == 1.57

    def test_gate_validation(self):
        """Test gate validation."""
        with pytest.raises(ValueError):
            Gate("CNOT", [0])  # Missing control

        with pytest.raises(ValueError):
            Gate("RX", [0])  # Missing theta param

    def test_gate_equality(self):
        """Test gate equality."""
        g1 = Gate("H", [0])
        g2 = Gate("H", [0])
        g3 = Gate("X", [0])

        assert g1 == g2
        assert g1 != g3
        assert g1 != "not a gate"

    def test_gate_serialization(self):
        """Test gate to_dict/from_dict."""
        g = Gate("CNOT", [1], control_qubits=[0])
        data = g.to_dict()
        g2 = Gate.from_dict(data)

        assert g == g2
