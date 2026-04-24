"""
Circuit Designer CLI

Command-line interface for quantum circuit design.
"""

import argparse
import sys
from pathlib import Path

from .designer import CircuitDesigner
from .optimizer import OptimizationLevel
from .quantum_ir import QuantumIR


def create_parser() -> argparse.ArgumentParser:
    """Create argument parser."""
    parser = argparse.ArgumentParser(
        prog="atlas.designer",
        description="Quantum Circuit Designer - Generate quantum circuits from algorithms",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python -m atlas.designer algorithm_id
  python -m atlas.designer my_algorithm --optimization-level O2 --output circuit.json
  python -m atlas.designer shor_factoring --param n=4 --visualize
        """
    )
    
    parser.add_argument(
        "algorithm_id",
        help="Algorithm ID or AlgorithmIR YAML file path"
    )
    
    parser.add_argument(
        "--optimization-level", "-O",
        choices=["O0", "O1", "O2", "O3", "0", "1", "2", "3"],
        default="O1",
        help="Optimization level (default: O1)"
    )
    
    parser.add_argument(
        "--output", "-o",
        help="Output file path for Quantum IR JSON"
    )
    
    parser.add_argument(
        "--visualize", "-v",
        action="store_true",
        help="Generate ASCII circuit visualization"
    )
    
    parser.add_argument(
        "--param", "-p",
        action="append",
        default=[],
        help="Circuit parameters (e.g., -p n=4 -p precision=0.01)"
    )
    
    parser.add_argument(
        "--export-qasm",
        help="Export to QASM format (specify output file)"
    )
    
    parser.add_argument(
        "--export-qiskit",
        help="Export to Qiskit Python code (specify output file)"
    )
    
    parser.add_argument(
        "--neo4j-uri",
        default="bolt://localhost:7687",
        help="Neo4j Bolt URI"
    )
    
    parser.add_argument(
        "--neo4j-user",
        default="neo4j",
        help="Neo4j username"
    )
    
    parser.add_argument(
        "--neo4j-password",
        help="Neo4j password (or set NEO4J_PASSWORD env var)"
    )
    
    parser.add_argument(
        "--list-primitives",
        action="store_true",
        help="List available primitives and exit"
    )
    
    parser.add_argument(
        "--save-to-kg",
        action="store_true",
        help="Save designed circuit to knowledge graph"
    )
    
    return parser


def parse_optimization_level(level_str: str) -> int:
    """Parse optimization level string to int."""
    mapping = {
        "O0": 0, "0": 0,
        "O1": 1, "1": 1,
        "O2": 2, "2": 2,
        "O3": 3, "3": 3,
    }
    return mapping.get(level_str, 1)


def parse_params(param_list: list) -> dict:
    """Parse parameter list to dictionary."""
    params = {}
    for p in param_list:
        if "=" in p:
            key, value = p.split("=", 1)
            # Try to convert to number
            try:
                if "." in value:
                    value = float(value)
                else:
                    value = int(value)
            except ValueError:
                pass  # Keep as string
            params[key] = value
    return params


def visualize_circuit(quantum_ir: QuantumIR) -> str:
    """Generate visualization of the circuit."""
    lines = [
        "=" * 60,
        "QUANTUM CIRCUIT VISUALIZATION",
        "=" * 60,
        f"Algorithm: {quantum_ir.algorithm_id}",
        f"Qubits: {quantum_ir.num_qubits}",
        f"Classical bits: {quantum_ir.circuit.num_clbits}",
        f"Gate count: {quantum_ir.gate_count}",
        f"Depth: {quantum_ir.depth}",
        f"Optimization: O{quantum_ir.optimization_level}",
        "-" * 60,
        "CIRCUIT:",
        quantum_ir.circuit.to_ascii(),
        "-" * 60,
        "GATE COUNTS BY TYPE:",
    ]
    
    counts = quantum_ir.circuit.gate_counts_by_type()
    for gate_type, count in sorted(counts.items()):
        lines.append(f"  {gate_type}: {count}")
    
    lines.append("=" * 60)
    
    return "\n".join(lines)


def main():
    """Main entry point."""
    parser = create_parser()
    args = parser.parse_args()
    
    # Handle list primitives
    if args.list_primitives:
        designer = CircuitDesigner()
        primitives = designer.get_available_primitives()
        print("Available Primitives:")
        print("-" * 40)
        for pid in sorted(primitives):
            primitive = designer.primitive_loader.get_primitive(pid)
            if primitive:
                print(f"  {pid}")
                print(f"    Name: {primitive.name}")
                print(f"    Category: {primitive.category}")
                print(f"    Qubits: {primitive.input_qubits}")
                print()
        return 0
    
    # Parse optimization level
    opt_level = parse_optimization_level(args.optimization_level)
    
    # Parse parameters
    params = parse_params(args.param)
    
    # Get Neo4j password from env if not provided
    neo4j_password = args.neo4j_password
    if not neo4j_password:
        import os
        neo4j_password = os.getenv("NEO4J_PASSWORD")
    
    # Initialize designer
    try:
        designer = CircuitDesigner(
            neo4j_uri=args.neo4j_uri,
            neo4j_user=args.neo4j_user,
            neo4j_password=neo4j_password,
            default_optimization_level=opt_level
        )
    except Exception as e:
        print(f"Error initializing designer: {e}", file=sys.stderr)
        return 1
    
    # Design circuit
    try:
        algo_path = Path(args.algorithm_id)
        if algo_path.exists() and algo_path.suffix in (".yaml", ".yml", ".json"):
            # Load from file
            import yaml
            with open(algo_path, 'r') as f:
                if algo_path.suffix == ".json":
                    import json
                    algo_data = json.load(f)
                else:
                    algo_data = yaml.safe_load(f)
            quantum_ir = designer.design_circuit(
                algo_data,
                optimization_level=opt_level,
                parameter_overrides=params
            )
        else:
            # Use as algorithm ID
            quantum_ir = designer.design_circuit(
                args.algorithm_id,
                optimization_level=opt_level,
                parameter_overrides=params
            )
    except Exception as e:
        print(f"Error designing circuit: {e}", file=sys.stderr)
        return 1
    
    # Visualize
    if args.visualize:
        print(visualize_circuit(quantum_ir))
    else:
        # Print summary
        summary = quantum_ir.get_summary()
        print(f"Circuit designed successfully!")
        print(f"  Algorithm: {summary['algorithm_id']}")
        print(f"  Qubits: {summary['num_qubits']}")
        print(f"  Gates: {summary['gate_count']}")
        print(f"  Depth: {summary['depth']}")
        print(f"  Optimization: O{summary['optimization_level']}")
    
    # Save output
    if args.output:
        try:
            quantum_ir.save(args.output)
            print(f"\nCircuit saved to: {args.output}")
        except Exception as e:
            print(f"Error saving circuit: {e}", file=sys.stderr)
            return 1
    
    # Export QASM
    if args.export_qasm:
        try:
            qasm = quantum_ir.to_qasm()
            with open(args.export_qasm, 'w') as f:
                f.write(qasm)
            print(f"QASM exported to: {args.export_qasm}")
        except Exception as e:
            print(f"Error exporting QASM: {e}", file=sys.stderr)
            return 1
    
    # Export Qiskit
    if args.export_qiskit:
        try:
            qiskit_code = quantum_ir.to_qiskit_code()
            with open(args.export_qiskit, 'w') as f:
                f.write(qiskit_code)
            print(f"Qiskit code exported to: {args.export_qiskit}")
        except Exception as e:
            print(f"Error exporting Qiskit code: {e}", file=sys.stderr)
            return 1
    
    # Save to knowledge graph
    if args.save_to_kg:
        try:
            success = designer.save_circuit_to_kg(quantum_ir)
            if success:
                print("Circuit saved to knowledge graph")
            else:
                print("Failed to save circuit to knowledge graph", file=sys.stderr)
        except Exception as e:
            print(f"Error saving to knowledge graph: {e}", file=sys.stderr)
            return 1
    
    return 0


if __name__ == "__main__":
    sys.exit(main())
