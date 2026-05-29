"""
Resource Estimator CLI

Command-line interface for quantum circuit resource estimation.

Usage:
    python -m qatlas.estimator <circuit_file> [options]
    
Examples:
    python -m qatlas.estimator circuit.json
    python -m qatlas.estimator circuit.json --format markdown --output report
    python -m qatlas.estimator circuit.json --hardware-params '{"gate_time": 50}'
"""

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Dict, Optional

try:
    from qatlas.designer.quantum_circuit import QuantumCircuit
except ImportError:
    # Handle import when running from different contexts
    import sys
    sys.path.insert(0, str(Path(__file__).parent.parent.parent))
    from qatlas.designer.quantum_circuit import QuantumCircuit

from .estimator import ResourceEstimator


def load_circuit(file_path: str) -> QuantumCircuit:
    """
    Load a quantum circuit from a JSON file.
    
    Args:
        file_path: Path to the circuit JSON file
        
    Returns:
        QuantumCircuit instance
        
    Raises:
        FileNotFoundError: If file doesn't exist
        ValueError: If file format is invalid
    """
    path = Path(file_path)
    
    if not path.exists():
        raise FileNotFoundError(f"Circuit file not found: {file_path}")
    
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    
    # Check if it's a circuit dict or wrapped in metadata
    if "circuit" in data:
        circuit_data = data["circuit"]
        circuit_info = {k: v for k, v in data.items() if k != "circuit"}
    else:
        circuit_data = data
        circuit_info = {}
    
    circuit = QuantumCircuit.from_dict(circuit_data)
    return circuit, circuit_info


def parse_hardware_params(params_str: Optional[str]) -> Optional[Dict[str, Any]]:
    """
    Parse hardware parameters from JSON string.
    
    Args:
        params_str: JSON string with hardware parameters
        
    Returns:
        Dictionary of hardware parameters or None
    """
    if not params_str:
        return None
    
    try:
        params = json.loads(params_str)
        
        # Validate parameter types
        for key in ["gate_time", "two_qubit_gate_time", "measurement_time", "coherence_time"]:
            if key in params and not isinstance(params[key], (int, float)):
                raise ValueError(f"Parameter {key} must be a number")
        
        return params
        
    except json.JSONDecodeError as e:
        raise ValueError(f"Invalid JSON in hardware parameters: {e}")


def create_sample_circuit() -> QuantumCircuit:
    """Create a sample Bell state circuit for testing."""
    circuit = QuantumCircuit(2, 2, name="Bell State")
    circuit.h(0)
    circuit.cnot(0, 1)
    circuit.measure(0, 0)
    circuit.measure(1, 1)
    return circuit


def main():
    """Main CLI entry point."""
    parser = argparse.ArgumentParser(
        description="Quantum Circuit Resource Estimator",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s circuit.json
  %(prog)s circuit.json --format markdown --output report.md
  %(prog)s circuit.json --format json --output report.json
  %(prog)s circuit.json --hardware-params '{"gate_time": 50, "coherence_time": 100}'
  %(prog)s --demo
        """,
    )
    
    parser.add_argument(
        "circuit_file",
        nargs="?",
        help="Path to circuit JSON file",
    )
    
    parser.add_argument(
        "--format",
        choices=["markdown", "json", "both"],
        default="markdown",
        help="Output format (default: markdown)",
    )
    
    parser.add_argument(
        "--output",
        "-o",
        help="Output file path (without extension)",
    )
    
    parser.add_argument(
        "--hardware-params",
        help='Hardware parameters as JSON string, e.g., \'{"gate_time": 50}\'',
    )
    
    parser.add_argument(
        "--algorithm-name",
        "-n",
        default="Unknown",
        help="Name of the algorithm (default: Unknown)",
    )
    
    parser.add_argument(
        "--demo",
        action="store_true",
        help="Run with a demo Bell state circuit",
    )
    
    parser.add_argument(
        "--quiet",
        "-q",
        action="store_true",
        help="Suppress non-error output",
    )
    
    args = parser.parse_args()
    
    # Validate arguments
    if not args.circuit_file and not args.demo:
        parser.error("Must provide circuit_file or use --demo")
    
    try:
        # Load circuit
        if args.demo:
            circuit = create_sample_circuit()
            circuit_info = {"name": "Bell State", "description": "Demo circuit for testing"}
            algorithm_name = args.algorithm_name if args.algorithm_name != "Unknown" else "Bell State Demo"
        else:
            circuit, circuit_info = load_circuit(args.circuit_file)
            algorithm_name = args.algorithm_name
            if algorithm_name == "Unknown" and circuit.name:
                algorithm_name = circuit.name
        
        # Parse hardware parameters
        hardware_params = parse_hardware_params(args.hardware_params)
        
        # Run estimation
        estimator = ResourceEstimator()
        report = estimator.estimate(
            circuit=circuit,
            algorithm_name=algorithm_name,
            circuit_info=circuit_info,
            hardware_params=hardware_params,
        )
        
        # Output results
        if args.output:
            # Save to file
            output_path = Path(args.output)
            success = estimator.save_report_to_file(report, output_path, format=args.format)
            if not success:
                sys.exit(1)
        else:
            # Print to stdout
            if args.format == "json":
                print(json.dumps(report["json_report"], indent=2, default=str))
            elif args.format == "both":
                print("=== Markdown Report ===")
                print(report["markdown_report"])
                print("\n=== JSON Report ===")
                print(json.dumps(report["json_report"], indent=2, default=str))
            else:
                print(report["markdown_report"])
        
        # Print summary
        if not args.quiet and not args.output:
            stats = report["resource_stats"]
            print(f"\n{'='*50}", file=sys.stderr)
            print(f"Summary: {stats.total_gates} gates, {stats.depth} depth, {stats.num_qubits} qubits", file=sys.stderr)
            if stats.estimated_time_ms:
                print(f"Estimated time: {stats.estimated_time_ms:.3f} ms", file=sys.stderr)
            print(f"{'='*50}", file=sys.stderr)
        
        sys.exit(0)
        
    except FileNotFoundError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)
        
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)
        
    except Exception as e:
        print(f"Unexpected error: {e}", file=sys.stderr)
        if args.demo:
            import traceback
            traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()