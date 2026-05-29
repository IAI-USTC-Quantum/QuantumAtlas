"""Tests for CodeGenerator main class."""

import ast
import json
import os
import tempfile
import pytest
from pathlib import Path

from qatlas.codegen.generator import CodeGenerator, generate_qpanda_code, generate_qiskit_code
from qatlas.designer.quantum_ir import QuantumIR
from qatlas.designer.quantum_circuit import QuantumCircuit


class TestCodeGenerator:
    """Test CodeGenerator class."""
    
    def test_init_qiskit_backend(self):
        """Test initializing with Qiskit backend."""
        gen = CodeGenerator(backend="qiskit")
        assert gen.backend == "qiskit"
        assert gen.formatter is not None
    
    def test_init_qpanda_backend(self):
        """Test initializing with QPanda backend."""
        gen = CodeGenerator(backend="qpanda")
        assert gen.backend == "qpanda"
        assert gen.formatter is not None
    
    def test_init_invalid_backend(self):
        """Test initializing with invalid backend raises error."""
        with pytest.raises(ValueError, match="Unsupported backend"):
            CodeGenerator(backend="invalid")
    
    def test_init_no_formatter(self):
        """Test initializing without formatter."""
        gen = CodeGenerator(backend="qiskit", use_formatter=False)
        assert gen.formatter is None
    
    def test_generate_with_qiskit(self):
        """Test generating code with Qiskit backend."""
        gen = CodeGenerator(backend="qiskit")
        
        circuit = QuantumCircuit(num_qubits=2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="bell_state")
        code = gen.generate(ir)
        
        assert "from qiskit import" in code
        assert "circuit.h(0)" in code
        assert "circuit.cx(0, 1)" in code
    
    def test_generate_with_qpanda(self):
        """Test generating code with QPanda backend."""
        gen = CodeGenerator(backend="qpanda")
        
        circuit = QuantumCircuit(num_qubits=2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="bell_state")
        code = gen.generate(ir)
        
        assert "from pyqpanda import" in code
        assert "circuit.H(qubits[0])" in code
        assert "circuit.CNOT(qubits[0], qubits[1])" in code
    
    def test_generate_with_description(self):
        """Test generating code with custom description."""
        gen = CodeGenerator(backend="qiskit")
        
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="test")
        code = gen.generate(ir, description="My custom description")
        
        assert "My custom description" in code
    
    def test_generate_without_formatting(self):
        """Test generating code without formatting."""
        gen = CodeGenerator(backend="qiskit")
        
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="test")
        code = gen.generate(ir, apply_formatting=False)
        
        # Code should still be valid
        ast.parse(code)
    
    def test_generate_file(self):
        """Test generating code and saving to file."""
        gen = CodeGenerator(backend="qiskit")
        
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="test")
        
        with tempfile.TemporaryDirectory() as tmpdir:
            output_path = os.path.join(tmpdir, "output.py")
            result_path = gen.generate_file(ir, output_path)
            
            assert result_path == output_path
            assert os.path.exists(output_path)
            
            with open(output_path, 'r') as f:
                code = f.read()
            
            assert "circuit.h(0)" in code
    
    def test_generate_file_creates_directories(self):
        """Test that generate_file creates parent directories."""
        gen = CodeGenerator(backend="qiskit")
        
        circuit = QuantumCircuit(num_qubits=1)
        ir = QuantumIR(circuit=circuit, algorithm_id="test")
        
        with tempfile.TemporaryDirectory() as tmpdir:
            nested_path = os.path.join(tmpdir, "nested", "dir", "output.py")
            gen.generate_file(ir, nested_path)
            
            assert os.path.exists(nested_path)
    
    def test_generate_from_circuit(self):
        """Test generating code directly from circuit."""
        gen = CodeGenerator(backend="qiskit")
        
        circuit = QuantumCircuit(num_qubits=2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        code = gen.generate_from_circuit(
            circuit,
            algorithm_id="direct_circuit",
            description="Direct from circuit"
        )
        
        assert "circuit.h(0)" in code
        assert "circuit.cx(0, 1)" in code
        assert "Direct from circuit" in code
    
    def test_get_supported_gates(self):
        """Test getting supported gates."""
        gen = CodeGenerator(backend="qiskit")
        gates = gen.get_supported_gates()
        
        assert "H" in gates
        assert "CNOT" in gates
        assert "MEASURE" in gates
    
    def test_list_backends(self):
        """Test listing available backends."""
        backends = CodeGenerator.list_backends()
        
        assert "qiskit" in backends
        assert "qpanda" in backends


class TestBellStateGeneration:
    """Test Bell state code generation for both backends."""
    
    @pytest.fixture
    def bell_state_circuit(self):
        """Create a Bell state circuit."""
        circuit = QuantumCircuit(num_qubits=2, num_clbits=2)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        return circuit
    
    def test_bell_state_qiskit(self, bell_state_circuit):
        """Test generating Bell state code for Qiskit."""
        gen = CodeGenerator(backend="qiskit")
        ir = QuantumIR(circuit=bell_state_circuit, algorithm_id="bell_state")
        
        code = gen.generate(ir)
        
        # Verify syntax
        ast.parse(code)
        
        # Verify key components
        assert "circuit.h(0)" in code
        assert "circuit.cx(0, 1)" in code
        assert "circuit.measure(0, 0)" in code
        assert "circuit.measure(1, 1)" in code
    
    def test_bell_state_qpanda(self, bell_state_circuit):
        """Test generating Bell state code for QPanda."""
        gen = CodeGenerator(backend="qpanda")
        ir = QuantumIR(circuit=bell_state_circuit, algorithm_id="bell_state")
        
        code = gen.generate(ir)
        
        # Verify syntax
        ast.parse(code)
        
        # Verify key components
        assert "circuit.H(qubits[0])" in code
        assert "circuit.CNOT(qubits[0], qubits[1])" in code


class TestConvenienceFunctions:
    """Test convenience functions."""
    
    def test_generate_qpanda_code(self):
        """Test generate_qpanda_code function."""
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="test")
        code = generate_qpanda_code(ir)
        
        assert "from pyqpanda import" in code
        assert "circuit.H(qubits[0])" in code
    
    def test_generate_qiskit_code(self):
        """Test generate_qiskit_code function."""
        circuit = QuantumCircuit(num_qubits=1)
        circuit.h(0)
        
        ir = QuantumIR(circuit=circuit, algorithm_id="test")
        code = generate_qiskit_code(ir)
        
        assert "from qiskit import" in code
        assert "circuit.h(0)" in code


class TestCodeGenerationWithIR:
    """Test code generation using QuantumIR."""
    
    def test_generate_from_loaded_ir(self):
        """Test generating code from loaded IR."""
        circuit = QuantumCircuit(num_qubits=2, num_clbits=1, name="test_circ")
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.measure(1, 0)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="test_algo",
            optimization_level=2,
            metadata={"author": "test"}
        )
        
        with tempfile.TemporaryDirectory() as tmpdir:
            # Save IR
            ir_path = os.path.join(tmpdir, "ir.json")
            ir.save(ir_path)
            
            # Load IR
            loaded_ir = QuantumIR.load(ir_path)
            
            # Generate code
            gen = CodeGenerator(backend="qiskit")
            code = gen.generate(loaded_ir)
            
            # Verify
            assert "circuit.h(0)" in code
            assert "circuit.cx(0, 1)" in code
            assert "circuit.measure(1, 0)" in code


class TestMultiGateCircuits:
    """Test code generation for circuits with multiple gate types."""
    
    def test_comprehensive_circuit_qiskit(self):
        """Test comprehensive circuit with Qiskit."""
        circuit = QuantumCircuit(num_qubits=3, num_clbits=2)
        
        # Single qubit gates
        circuit.h(0)
        circuit.x(1)
        circuit.y(0)
        circuit.z(1)
        circuit.s(0)
        circuit.t(1)
        
        # Rotation gates
        circuit.rx(0, 1.57)
        circuit.ry(1, 0.785)
        circuit.rz(2, 3.14)
        
        # Two qubit gates
        circuit.cnot(0, 1)
        circuit.cz(1, 2)
        circuit.swap(0, 2)
        
        # Measurement
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        
        gen = CodeGenerator(backend="qiskit")
        ir = QuantumIR(circuit=circuit, algorithm_id="comprehensive")
        code = gen.generate(ir)
        
        # Verify syntax
        tree = ast.parse(code)
        assert isinstance(tree, ast.Module)
        
        # Verify all gates present
        assert "circuit.h(0)" in code
        assert "circuit.x(1)" in code
        assert "circuit.y(0)" in code
        assert "circuit.z(1)" in code
        assert "circuit.s(0)" in code
        assert "circuit.t(1)" in code
        assert "circuit.rx(1.57, 0)" in code
        assert "circuit.ry(0.785, 1)" in code
        assert "circuit.rz(3.14, 2)" in code
        assert "circuit.cx(0, 1)" in code
        assert "circuit.cz(1, 2)" in code
        assert "circuit.swap(0, 2)" in code
        assert "circuit.measure(0, 0)" in code
        assert "circuit.measure(1, 1)" in code
