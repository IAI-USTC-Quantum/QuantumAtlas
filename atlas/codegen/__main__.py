"""
Code Generator CLI

Command-line interface for the code generator module.

Usage:
    python -m atlas.codegen <quantum_ir_file>
    python -m atlas.codegen <quantum_ir_file> --backend qpanda --output output.py
"""

import argparse
import sys
from pathlib import Path
from typing import Optional

from .generator import CodeGenerator
from ..designer.quantum_ir import QuantumIR


def create_parser() -> argparse.ArgumentParser:
    """Create the argument parser."""
    parser = argparse.ArgumentParser(
        prog="atlas.codegen",
        description="Generate executable quantum computing code from Quantum IR",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
    # Generate Qiskit code (default)
    python -m atlas.codegen circuit.json
    
    # Generate QPanda code
    python -m atlas.codegen circuit.json --backend qpanda
    
    # Save to specific file
    python -m atlas.codegen circuit.json --output my_circuit.py
    
    # Skip code formatting
    python -m atlas.codegen circuit.json --no-format
        """,
    )
    
    parser.add_argument(
        "input_file",
        type=str,
        help="Path to Quantum IR JSON file",
    )
    
    parser.add_argument(
        "--backend",
        type=str,
        choices=["qpanda", "qiskit"],
        default="qiskit",
        help="Target backend framework (default: qiskit)",
    )
    
    parser.add_argument(
        "--output",
        "-o",
        type=str,
        help="Output file path (default: print to stdout)",
    )
    
    parser.add_argument(
        "--no-format",
        action="store_true",
        help="Skip code formatting",
    )
    
    parser.add_argument(
        "--description",
        "-d",
        type=str,
        help="Circuit description to include in generated code",
    )
    
    parser.add_argument(
        "--line-length",
        type=int,
        default=100,
        help="Maximum line length for formatting (default: 100)",
    )
    
    return parser


def main(args: Optional[list] = None) -> int:
    """
    Main entry point for the CLI.
    
    Args:
        args: Command line arguments (defaults to sys.argv[1:])
        
    Returns:
        Exit code (0 for success, 1 for error)
    """
    parser = create_parser()
    parsed_args = parser.parse_args(args)
    
    # Validate input file
    input_path = Path(parsed_args.input_file)
    if not input_path.exists():
        print(f"Error: Input file not found: {input_path}", file=sys.stderr)
        return 1
    
    if not input_path.is_file():
        print(f"Error: Input path is not a file: {input_path}", file=sys.stderr)
        return 1
    
    # Load Quantum IR
    try:
        quantum_ir = QuantumIR.load(str(input_path))
    except Exception as e:
        print(f"Error loading Quantum IR: {e}", file=sys.stderr)
        return 1
    
    # Create generator
    try:
        generator = CodeGenerator(
            backend=parsed_args.backend,
            use_formatter=not parsed_args.no_format,
            line_length=parsed_args.line_length,
        )
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1
    
    # Generate code
    try:
        code = generator.generate(
            quantum_ir=quantum_ir,
            description=parsed_args.description,
            apply_formatting=not parsed_args.no_format,
        )
    except Exception as e:
        print(f"Error generating code: {e}", file=sys.stderr)
        return 1
    
    # Output
    if parsed_args.output:
        output_path = Path(parsed_args.output)
        try:
            output_path.parent.mkdir(parents=True, exist_ok=True)
            with open(output_path, 'w', encoding='utf-8') as f:
                f.write(code)
            print(f"Generated code saved to: {output_path}")
            
            # Print summary
            print(f"\nGeneration Summary:")
            print(f"  Backend: {parsed_args.backend}")
            print(f"  Algorithm: {quantum_ir.algorithm_id}")
            print(f"  Qubits: {quantum_ir.num_qubits}")
            print(f"  Gates: {quantum_ir.gate_count}")
            print(f"  Depth: {quantum_ir.depth}")
        except Exception as e:
            print(f"Error saving file: {e}", file=sys.stderr)
            return 1
    else:
        # Print to stdout
        print(code)
    
    return 0


if __name__ == "__main__":
    sys.exit(main())
