"""Tests for QPandaCodeGenerator."""

import ast
import pytest
from qatlas.codegen.qpanda_generator import QPandaCodeGenerator
from qatlas.designer.quantum_circuit import QuantumCircuit


class TestQPandaCodeGenerator:
    """Test QPandaCodeGenerator class."""
    
    @pytest.fixture
    def generator(self):
        """Create a QPandaCodeGenerator instance."""
        return QPandaCodeGenerator()
    
    def test_empty_circuit(self, generator):
        """Test generating code for empty circuit."""
        circuit = QuantumCircuit(num_qubits=2, num_clbits=1)
        code = generator.generate(circuit, algorithm_id="test_empty")
        
        assert "from pyqpanda import" in code
        assert "CPUQVM" in code
        assert "qAlloc_many(2)" in code
        assert "cAlloc_many(1)" in code
    
    def test_single_qubit_gates(self, generator):
        """Test generating code for single qubit gates."""
        circuit = QuantumCircuit(num_qubits=2)
        circuit.h(0)
        circuit.x(1)
        circuit.y(0)
        circuit.z(1)
        circuit.s(0)
        circuit.t(1)
        
        code = generator.generate(circuit, algorithm_id="single_qubit")
        
        assert "circuit.H(qubits[0])" in code
        assert "circuit.X(qubits[1])" in code
        assert "circuit.Y(qubits[0])" in code
        assert "circuit.Z(qubits[1])" in code
        assert "circuit.S(qubits[0])" in code
        assert "circuit.T(qubits[1])" in code
    
    def test_rotation_gates(self, generator):
        """Test generating code for rotation gates."""
        circuit = QuantumCircuit(num_qubits=2)
        circuit.rx(0, 1.57)
        circuit.ry(1, 0.785)
        circuit.rz(0, 3.14)
        
        code = generator.generate(circuit, algorithm_id="rotation")
        
        assert "circuit.RX(qubits[0], 1.57)" in code
        assert "circuit.RY(qubits[1], 0.785)" in code
        assert "circuit.RZ(qubits[0], 3.14)" in code
    
    def test_cnot_gate(self, generator):
        """Test generating code for CNOT gate."""
        circuit = QuantumCircuit(num_qubits=2)
        circuit.cnot(0, 1)
        
        code = generator.generate(circuit, algorithm_id="cnot")
        
        assert "circuit.CNOT(qubits[0], qubits[1])" in code
    
    def test_cz_gate(self, generator):
        """Test generating code for CZ gate."""
        circuit = QuantumCircuit(num_qubits=2)
        circuit.cz(0, 1)
        
        code = generator.generate(circuit, algorithm_id="cz")
        
        assert "circuit.CZ(qubits[0], qubits[1])" in code
    
    def test_swap_gate(self, generator):
        """Test generating code for SWAP gate."""
        circuit = QuantumCircuit(num_qubits=2)
        circuit.swap(0, 1)
        
        code = generator.generate(circuit, algorithm_id="swap")
        
        assert "circuit.SWAP(qubits[0], qubits[1])" in code
    
    def test_bell_state(self, generator):
        """Test generating code for Bell state circuit."""
        circuit = QuantumCircuit(num_qubits=2, num_clbits=2)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        
        code = generator.generate(circuit, algorithm_id="bell_state")
        
        assert "circuit.H(qubits[0])" in code
        assert "circuit.CNOT(qubits[0], qubits[1])" in code
        # QPanda handles measurement separately
        assert "Algorithm: bell_state" in code
    
    def test_syntax_validity(self, generator):
        """Test that generated code has valid Python syntax."""
        circuit = QuantumCircuit(num_qubits=2)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.rx(1, 1.57)
        
        code = generator.generate(circuit, algorithm_id="syntax_test")
        
        # Should not raise SyntaxError
        ast.parse(code)
    
    def test_algorithm_id_in_code(self, generator):
        """Test that algorithm ID appears in generated code."""
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        code = generator.generate(
            circuit,
            algorithm_id="my_algorithm",
            description="Test description"
        )
        
        assert "my_algorithm" in code
        assert "Test description" in code
    
    def test_get_supported_gates(self, generator):
        """Test getting list of supported gates."""
        gates = generator.get_supported_gates()
        
        expected_gates = ["H", "X", "Y", "Z", "S", "T", "CNOT", "CZ", "SWAP", "RX", "RY", "RZ", "MEASURE"]
        for gate in expected_gates:
            assert gate in gates
    
    def test_unsupported_gate(self, generator):
        """Test handling of unsupported gates."""
        circuit = QuantumCircuit(num_qubits=1)
        # Add a custom/unsupported gate directly
        from qatlas.designer.quantum_circuit import Gate
        circuit.add_gate(Gate("CUSTOM_GATE", [0]))
        
        code = generator.generate(circuit, algorithm_id="unsupported")
        
        assert "Unsupported gate: CUSTOM_GATE" in code
    
    def test_multiple_gates_circuit(self, generator):
        """Test generating code for complex multi-gate circuit."""
        circuit = QuantumCircuit(num_qubits=3)
        circuit.h(0)
        circuit.h(1)
        circuit.cnot(0, 1)
        circuit.cnot(1, 2)
        circuit.rx(0, 0.5)
        circuit.ry(1, 0.3)
        circuit.rz(2, 0.7)
        circuit.x(2)
        
        code = generator.generate(circuit, algorithm_id="complex")
        
        # Verify all gates are in the code
        assert "circuit.H(qubits[0])" in code
        assert "circuit.H(qubits[1])" in code
        assert "circuit.CNOT(qubits[0], qubits[1])" in code
        assert "circuit.CNOT(qubits[1], qubits[2])" in code
        assert "circuit.RX(qubits[0], 0.5)" in code
        assert "circuit.RY(qubits[1], 0.3)" in code
        assert "circuit.RZ(qubits[2], 0.7)" in code
        assert "circuit.X(qubits[2])" in code
        
        # Verify syntax
        ast.parse(code)


class TestQPandaGeneratorTemplates:
    """Test template-based generation."""
    
    def test_template_rendering(self):
        """Test that template is properly rendered."""
        generator = QPandaCodeGenerator()
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        code = generator.generate(circuit, algorithm_id="template_test")
        
        # Check template elements
        assert "def create_circuit():" in code
        assert "def run_circuit(" in code
        assert 'if __name__ == "__main__":' in code
