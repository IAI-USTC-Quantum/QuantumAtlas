"""
代码生成模块 (Codegen)

职责：
- 将电路设计转化为可执行代码
- 支持多种量子计算框架（Qiskit、Cirq、QPanda 等）
- 生成 Python 量子代码
- 生成代码注释和文档

Usage:
    from atlas.codegen import CodeGenerator
    from atlas.designer.quantum_ir import QuantumIR
    
    # Load Quantum IR
    ir = QuantumIR.load("circuit.json")
    
    # Generate code
    generator = CodeGenerator(backend="qiskit")
    code = generator.generate(ir)
    
    # Save to file
    generator.generate_file(ir, "output.py")
"""

from .generator import CodeGenerator, generate_qpanda_code, generate_qiskit_code
from .qpanda_generator import QPandaCodeGenerator
from .qiskit_generator import QiskitCodeGenerator
from .formatter import CodeFormatter, format_code, validate_code_syntax
from .template_engine import TemplateEngine

__all__ = [
    # Main generator
    "CodeGenerator",
    "generate_qpanda_code",
    "generate_qiskit_code",
    # Backend generators
    "QPandaCodeGenerator",
    "QiskitCodeGenerator",
    # Formatting
    "CodeFormatter",
    "format_code",
    "validate_code_syntax",
    # Templates
    "TemplateEngine",
]
