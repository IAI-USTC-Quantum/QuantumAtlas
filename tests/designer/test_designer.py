"""Tests for CircuitDesigner main class."""

import pytest
import tempfile
import os
from pathlib import Path

from qatlas.designer.designer import CircuitDesigner
from qatlas.designer.quantum_ir import QuantumIR
from qatlas.designer.quantum_circuit import QuantumCircuit
from qatlas.designer.optimizer import OptimizationLevel


class TestCircuitDesigner:
    """Test CircuitDesigner class."""

    def test_initialization(self):
        """Test designer initialization."""
        designer = CircuitDesigner()

        assert designer.primitive_loader is not None
        assert designer.primitive_composer is not None
        assert designer.optimizer is not None
        assert designer.parameter_mapper is not None

    def test_design_circuit_from_dict(self):
        """Test designing circuit from dict."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "test_algorithm",
            "name": "Test Algorithm",
            "primitives": [],  # Will be inferred
            "input_params": []
        }

        quantum_ir = designer.design_circuit(algo_dict)

        assert isinstance(quantum_ir, QuantumIR)
        assert quantum_ir.algorithm_id == "test_algorithm"
        assert quantum_ir.circuit is not None

    def test_design_circuit_with_params(self):
        """Test designing circuit with parameters."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "test_algo",
            "name": "Test",
            "primitives": [],
            "input_params": [{"name": "n", "value": 4}]
        }

        quantum_ir = designer.design_circuit(
            algo_dict,
            parameter_overrides={"n": 3}
        )

        assert isinstance(quantum_ir, QuantumIR)

    def test_design_circuit_with_optimization(self):
        """Test designing circuit with different optimization levels."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "test_algo",
            "name": "Test",
            "primitives": []
        }

        for level in [0, 1, 2, 3]:
            quantum_ir = designer.design_circuit(
                algo_dict,
                optimization_level=level
            )
            assert quantum_ir.optimization_level == level

    def test_get_available_primitives(self):
        """Test getting available primitives."""
        designer = CircuitDesigner()
        primitives = designer.get_available_primitives()

        assert isinstance(primitives, list)
        # Should have some primitives from YAML files
        assert len(primitives) > 0

    def test_save_and_load_design(self):
        """Test saving and loading design config."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "save_test",
            "name": "Save Test",
            "primitives": []
        }

        quantum_ir = designer.design_circuit(algo_dict)

        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            temp_path = f.name

        try:
            # Save
            designer.save_design_config(quantum_ir, temp_path)
            assert os.path.exists(temp_path)

            # Load
            loaded = designer.load_design_config(temp_path)
            assert isinstance(loaded, QuantumIR)
            assert loaded.algorithm_id == quantum_ir.algorithm_id
        finally:
            if os.path.exists(temp_path):
                os.unlink(temp_path)

    def test_quantum_ir_properties(self):
        """Test QuantumIR properties."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "prop_test",
            "name": "Property Test",
            "primitives": []
        }

        quantum_ir = designer.design_circuit(algo_dict)

        # Properties should be accessible
        _ = quantum_ir.gate_count
        _ = quantum_ir.depth
        _ = quantum_ir.num_qubits
        _ = quantum_ir.get_summary()

    def test_quantum_ir_serialization(self):
        """Test QuantumIR serialization."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "serial_test",
            "name": "Serialization Test",
            "primitives": []
        }

        quantum_ir = designer.design_circuit(algo_dict)

        # Test dict serialization
        data = quantum_ir.to_dict()
        assert isinstance(data, dict)
        assert data["algorithm_id"] == "serial_test"

        # Test JSON serialization
        json_str = quantum_ir.to_json()
        assert isinstance(json_str, str)
        assert "serial_test" in json_str

        # Test deserialization
        restored = QuantumIR.from_dict(data)
        assert restored.algorithm_id == quantum_ir.algorithm_id

    def test_quantum_ir_exports(self):
        """Test QuantumIR export formats."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "export_test",
            "name": "Export Test",
            "primitives": []
        }

        quantum_ir = designer.design_circuit(algo_dict)

        # Ensure circuit has at least 2 qubits for CNOT
        if quantum_ir.circuit.num_qubits < 2:
            from qatlas.designer.quantum_circuit import QuantumCircuit
            new_circuit = QuantumCircuit(num_qubits=2, name=quantum_ir.circuit.name)
            new_circuit.gates = quantum_ir.circuit.gates
            quantum_ir.circuit = new_circuit

        # Add some gates for meaningful export
        quantum_ir.circuit.h(0)
        quantum_ir.circuit.cnot(0, 1)

        # Test QASM export
        qasm = quantum_ir.to_qasm()
        assert isinstance(qasm, str)
        assert "OPENQASM" in qasm

        # Test Qiskit export
        qiskit = quantum_ir.to_qiskit_code()
        assert isinstance(qiskit, str)
        assert "QuantumCircuit" in qiskit

        # Test QPanda dict export
        qpanda = quantum_ir.to_qpanda_dict()
        assert isinstance(qpanda, dict)
        assert "gates" in qpanda


class TestCircuitDesignerPrimitives:
    """Test designer with specific primitives."""

    def test_infer_primitives_shor(self):
        """Test primitive inference for Shor's algorithm."""
        designer = CircuitDesigner()
        primitives = designer._infer_primitives("shor_1997", "Shor's Factoring Algorithm")

        # Should include QFT and QPE
        assert any("qft" in p.lower() for p in primitives)
        assert any("qpe" in p.lower() for p in primitives)

    def test_infer_primitives_grover(self):
        """Test primitive inference for Grover's algorithm."""
        designer = CircuitDesigner()
        primitives = designer._infer_primitives("grover_1996", "Grover's Search")

        # Should include amplitude amplification
        assert any("amplitude" in p.lower() for p in primitives)

    def test_infer_primitives_qpe(self):
        """Test primitive inference for QPE."""
        designer = CircuitDesigner()
        primitives = designer._infer_primitives("qpe_test", "Quantum Phase Estimation")

        # Should include QPE and QFT
        assert any("qpe" in p.lower() for p in primitives)

    def test_infer_primitives_hamiltonian(self):
        """Test primitive inference for Hamiltonian simulation."""
        designer = CircuitDesigner()
        primitives = designer._infer_primitives("ham_sim", "Hamiltonian Simulation")

        assert any("hamiltonian" in p.lower() for p in primitives)


class TestCircuitDesignerIntegration:
    """Integration tests for CircuitDesigner."""

    def test_bell_state_design(self):
        """Test designing a Bell state circuit."""
        designer = CircuitDesigner()

        # Bell state should be inferrable
        algo_dict = {
            "id": "bell_state_test",
            "name": "Bell State",
            "primitives": ["bell_state"] if "bell_state" in designer.get_available_primitives() else [],
            "input_params": [{"name": "n", "value": 2}]
        }

        quantum_ir = designer.design_circuit(algo_dict)

        assert quantum_ir.circuit.num_qubits >= 2

    def test_qft_design(self):
        """Test designing a QFT circuit."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "qft_test",
            "name": "QFT Test",
            "primitives": ["primitive_qft"] if "primitive_qft" in designer.get_available_primitives() else [],
            "input_params": [{"name": "n", "value": 4}]
        }

        quantum_ir = designer.design_circuit(algo_dict)

        # Should have at least 4 qubits
        assert quantum_ir.circuit.num_qubits >= 4

    def test_parameter_mapping(self):
        """Test parameter mapping in design."""
        designer = CircuitDesigner()

        algo_dict = {
            "id": "param_test",
            "name": "Parameter Test",
            "primitives": [],
            "input_params": [{"name": "precision", "value": 0.01}]
        }

        quantum_ir = designer.design_circuit(algo_dict)

        # Check metadata
        assert "circuit_params" in quantum_ir.metadata
