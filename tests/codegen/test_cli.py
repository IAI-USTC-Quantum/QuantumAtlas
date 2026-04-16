"""Tests for Code Generator CLI."""

import json
import sys
from io import StringIO
from pathlib import Path
from unittest.mock import patch

import pytest

from atlas.designer.quantum_ir import QuantumIR
from atlas.designer.quantum_circuit import QuantumCircuit


class TestCLI:
    """Test cases for CLI interface."""
    
    def test_cli_import(self):
        """Test that CLI module can be imported."""
        from atlas.codegen import __main__ as cli_module
        assert cli_module is not None
    
    def test_cli_help(self):
        """Test CLI help output."""
        from atlas.codegen.__main__ import main
        
        with pytest.raises(SystemExit) as exc_info:
            main(['--help'])
        
        assert exc_info.value.code == 0
    
    def test_cli_no_args_shows_error(self):
        """Test CLI with no arguments shows error."""
        from atlas.codegen.__main__ import main
        
        with pytest.raises(SystemExit) as exc_info:
            main([])
        assert exc_info.value.code == 2  # argparse exits with code 2 for missing arguments
    
    def test_cli_nonexistent_file(self):
        """Test CLI with non-existent file."""
        from atlas.codegen.__main__ import main
        
        result = main(['/nonexistent/file.json'])
        assert result == 1
    
    def test_cli_invalid_directory(self):
        """Test CLI with directory instead of file."""
        from atlas.codegen.__main__ import main
        
        result = main(['/tmp'])
        assert result == 1
    
    def test_cli_invalid_json(self, tmp_path):
        """Test CLI with invalid JSON file."""
        from atlas.codegen.__main__ import main
        
        ir_file = tmp_path / "invalid.json"
        ir_file.write_text("not valid json")
        
        result = main([str(ir_file)])
        assert result == 1
    
    def test_cli_qpanda_backend(self, tmp_path):
        """Test CLI with QPanda backend."""
        from atlas.codegen.__main__ import main
        
        # Create a valid IR file
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="bell_state",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        output_file = tmp_path / "output.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qpanda',
            '--output', str(output_file)
        ])
        
        assert result == 0
        assert output_file.exists()
        content = output_file.read_text()
        assert "pyqpanda" in content.lower() or "QPanda" in content
    
    def test_cli_qiskit_backend(self, tmp_path):
        """Test CLI with Qiskit backend."""
        from atlas.codegen.__main__ import main
        
        # Create a valid IR file
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="bell_state",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        output_file = tmp_path / "output.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qiskit',
            '--output', str(output_file)
        ])
        
        assert result == 0
        assert output_file.exists()
        content = output_file.read_text()
        assert "qiskit" in content.lower() or "Qiskit" in content
    
    def test_cli_no_format(self, tmp_path):
        """Test CLI with --no-format flag."""
        from atlas.codegen.__main__ import main
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="test",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        output_file = tmp_path / "output.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qpanda',
            '--output', str(output_file),
            '--no-format'
        ])
        
        assert result == 0
        assert output_file.exists()
    
    def test_cli_description(self, tmp_path):
        """Test CLI with description."""
        from atlas.codegen.__main__ import main
        
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="bell_state",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        output_file = tmp_path / "output.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qpanda',
            '--output', str(output_file),
            '--description', 'Test bell state'
        ])
        
        assert result == 0
        assert output_file.exists()
        content = output_file.read_text()
        # Description should appear in comments
        assert "bell" in content.lower() or "test" in content.lower()
    
    def test_cli_stdout_output(self, tmp_path, capsys):
        """Test CLI output to stdout."""
        from atlas.codegen.__main__ import main
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="test_algo",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        result = main([
            str(ir_file),
            '--backend', 'qpanda'
        ])
        
        assert result == 0
        captured = capsys.readouterr()
        assert "pyqpanda" in captured.out.lower() or "QPanda" in captured.out
    
    def test_cli_creates_directories(self, tmp_path):
        """Test CLI creates output directories if needed."""
        from atlas.codegen.__main__ import main
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="test",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        # Output to nested directory that doesn't exist
        output_file = tmp_path / "subdir1" / "subdir2" / "output.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qpanda',
            '--output', str(output_file)
        ])
        
        assert result == 0
        assert output_file.exists()
    
    def test_cli_line_length(self, tmp_path):
        """Test CLI with custom line length."""
        from atlas.codegen.__main__ import main
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="test",
            optimization_level=0
        )
        
        ir_file = tmp_path / "test_ir.json"
        ir.save(str(ir_file))
        
        output_file = tmp_path / "output.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qpanda',
            '--output', str(output_file),
            '--line-length', '80'
        ])
        
        assert result == 0
        assert output_file.exists()


class TestCLIArgumentParser:
    """Test CLI argument parsing."""
    
    def test_parser_creation(self):
        """Test that argument parser is created correctly."""
        from atlas.codegen.__main__ import create_parser
        
        parser = create_parser()
        assert parser is not None
    
    def test_backend_choices(self):
        """Test that backend choices are validated."""
        from atlas.codegen.__main__ import create_parser
        
        parser = create_parser()
        
        # Valid backends
        args = parser.parse_args(['file.json', '--backend', 'qpanda'])
        assert args.backend == 'qpanda'
        
        args = parser.parse_args(['file.json', '--backend', 'qiskit'])
        assert args.backend == 'qiskit'
    
    def test_default_backend(self):
        """Test default backend is qiskit."""
        from atlas.codegen.__main__ import create_parser
        
        parser = create_parser()
        args = parser.parse_args(['file.json'])
        
        assert args.backend == 'qiskit'
    
    def test_default_line_length(self):
        """Test default line length."""
        from atlas.codegen.__main__ import create_parser
        
        parser = create_parser()
        args = parser.parse_args(['file.json'])
        
        assert args.line_length == 100


class TestCLIMainFunction:
    """Test main function directly."""
    
    def test_main_with_valid_args(self, tmp_path):
        """Test main function with valid arguments."""
        from atlas.codegen.__main__ import main
        
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        ir = QuantumIR(
            circuit=circuit,
            algorithm_id="bell",
            optimization_level=0
        )
        
        ir_file = tmp_path / "ir.json"
        ir.save(str(ir_file))
        
        out_file = tmp_path / "out.py"
        
        result = main([
            str(ir_file),
            '--backend', 'qiskit',
            '--output', str(out_file)
        ])
        
        assert result == 0
        assert out_file.exists()


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
