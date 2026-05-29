"""
Code Generator Main Module

Provides the main CodeGenerator class that orchestrates code generation
for different quantum computing backends.
"""

from typing import Optional, Literal
from pathlib import Path

from ..designer.quantum_ir import QuantumIR
from ..designer.quantum_circuit import QuantumCircuit
from .qpanda_generator import QPandaCodeGenerator
from .qiskit_generator import QiskitCodeGenerator
from .formatter import CodeFormatter


BackendType = Literal["qpanda", "qiskit"]


class CodeGenerator:
    """
    Main code generator class for quantum circuit code generation.
    
    Supports multiple backends:
    - QPanda: China's quantum computing framework
    - Qiskit: IBM's quantum computing framework
    
    Attributes:
        backend: The selected backend type
        backend_generator: Backend-specific generator instance
        formatter: Code formatter instance
    """
    
    def __init__(
        self,
        backend: BackendType = "qiskit",
        use_formatter: bool = True,
        line_length: int = 100,
    ):
        """
        Initialize the code generator.
        
        Args:
            backend: Backend type ('qpanda' or 'qiskit')
            use_formatter: Whether to use code formatting
            line_length: Maximum line length for formatting
            
        Raises:
            ValueError: If backend is not supported
        """
        self.backend = backend
        self._use_formatter = use_formatter
        
        # Initialize backend-specific generator
        if backend == "qpanda":
            self.backend_generator = QPandaCodeGenerator()
        elif backend == "qiskit":
            self.backend_generator = QiskitCodeGenerator()
        else:
            raise ValueError(f"Unsupported backend: {backend}. "
                           "Supported backends: qpanda, qiskit")
        
        # Initialize formatter
        if use_formatter:
            self.formatter = CodeFormatter(line_length=line_length)
        else:
            self.formatter = None
    
    def generate(
        self,
        quantum_ir: QuantumIR,
        description: Optional[str] = None,
        apply_formatting: bool = True,
    ) -> str:
        """
        Generate Python code from QuantumIR.
        
        Args:
            quantum_ir: Quantum intermediate representation
            description: Optional circuit description
            apply_formatting: Whether to apply code formatting
            
        Returns:
            Generated Python code as string
        """
        # Generate description if not provided
        if description is None:
            description = f"Quantum circuit for {quantum_ir.algorithm_id}"
        
        # Generate code using backend generator
        code = self.backend_generator.generate(
            circuit=quantum_ir.circuit,
            algorithm_id=quantum_ir.algorithm_id,
            description=description,
        )
        
        # Apply formatting if enabled
        if apply_formatting and self.formatter:
            code = self.formatter.format(code, add_header=False)
        
        return code
    
    def generate_file(
        self,
        quantum_ir: QuantumIR,
        output_path: str,
        description: Optional[str] = None,
        apply_formatting: bool = True,
    ) -> str:
        """
        Generate Python code and save to file.
        
        Args:
            quantum_ir: Quantum intermediate representation
            output_path: Path to save the generated code
            description: Optional circuit description
            apply_formatting: Whether to apply code formatting
            
        Returns:
            Path to the saved file
        """
        code = self.generate(
            quantum_ir=quantum_ir,
            description=description,
            apply_formatting=apply_formatting,
        )
        
        # Ensure directory exists
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        
        # Write to file
        with open(output_path, 'w', encoding='utf-8') as f:
            f.write(code)
        
        return str(output_path)
    
    def generate_from_circuit(
        self,
        circuit: QuantumCircuit,
        algorithm_id: str = "quantum_circuit",
        description: Optional[str] = None,
        apply_formatting: bool = True,
    ) -> str:
        """
        Generate Python code directly from a QuantumCircuit.
        
        Args:
            circuit: Quantum circuit to convert
            algorithm_id: Identifier for the algorithm
            description: Optional circuit description
            apply_formatting: Whether to apply code formatting
            
        Returns:
            Generated Python code as string
        """
        if description is None:
            description = f"Quantum circuit: {algorithm_id}"
        
        # Generate code using backend generator
        code = self.backend_generator.generate(
            circuit=circuit,
            algorithm_id=algorithm_id,
            description=description,
        )
        
        # Apply formatting if enabled
        if apply_formatting and self.formatter:
            code = self.formatter.format(code, add_header=False)
        
        return code
    
    def get_supported_gates(self) -> list:
        """
        Get list of supported gate names for the current backend.
        
        Returns:
            List of supported gate names
        """
        return self.backend_generator.get_supported_gates()
    
    @staticmethod
    def list_backends() -> list:
        """
        List available backends.
        
        Returns:
            List of available backend names
        """
        return ["qpanda", "qiskit"]


# Convenience functions for direct usage

def generate_qpanda_code(
    quantum_ir: QuantumIR,
    description: Optional[str] = None,
) -> str:
    """
    Convenience function to generate QPanda code.
    
    Args:
        quantum_ir: Quantum intermediate representation
        description: Optional circuit description
        
    Returns:
        Generated QPanda Python code
    """
    generator = CodeGenerator(backend="qpanda")
    return generator.generate(quantum_ir, description=description)


def generate_qiskit_code(
    quantum_ir: QuantumIR,
    description: Optional[str] = None,
) -> str:
    """
    Convenience function to generate Qiskit code.
    
    Args:
        quantum_ir: Quantum intermediate representation
        description: Optional circuit description
        
    Returns:
        Generated Qiskit Python code
    """
    generator = CodeGenerator(backend="qiskit")
    return generator.generate(quantum_ir, description=description)
