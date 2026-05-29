#!/usr/bin/env python3
"""
Demo script showing complete paper-to-code workflow.

This script demonstrates the QuantumAtlas pipeline without requiring
external LLM API calls. It uses predefined algorithm definitions to
show how the pipeline works end-to-end.

Usage:
    python examples/demo_pipeline.py --algorithm bell_state --backend qiskit
    python examples/demo_pipeline.py --algorithm qft --backend qpanda
    python examples/demo_pipeline.py --algorithm grover --backend qiskit --save-code

Options:
    --algorithm   Algorithm to demo: bell_state, qft, grover
    --backend     Code backend: qiskit (default) or qpanda
    --save-code   Save generated code to file
    --output-dir  Directory for output files (default: ./output)
"""

import argparse
import json
import sys
from pathlib import Path
from datetime import datetime
from typing import Dict, Any, List, Optional

# Add parent directory to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent))

from qatlas.designer.quantum_circuit import QuantumCircuit
from qatlas.designer.quantum_ir import QuantumIR
from qatlas.codegen.generator import CodeGenerator
from qatlas.validator.validator import Validator
from qatlas.estimator.estimator import ResourceEstimator


# === Predefined Algorithm Definitions ===

ALGORITHMS = {
    "bell_state": {
        "id": "algorithm_bell_state",
        "name": "Bell State Preparation",
        "description": "Create a Bell state (maximally entangled 2-qubit state)",
        "problem_type": "state_preparation",
        "n_qubits": 2,
        "gates": [
            {"name": "H", "targets": [0]},
            {"name": "CNOT", "controls": [0], "targets": [1]},
        ],
    },
    "qft": {
        "id": "algorithm_qft_3q",
        "name": "Quantum Fourier Transform (3-qubit)",
        "description": "3-qubit Quantum Fourier Transform",
        "problem_type": "transformation",
        "n_qubits": 3,
        "gates": [
            {"name": "H", "targets": [0]},
            {"name": "RZ", "targets": [0], "params": {"theta": 1.5708}},
            {"name": "CNOT", "controls": [1], "targets": [0]},
            {"name": "H", "targets": [1]},
            {"name": "CNOT", "controls": [2], "targets": [1]},
            {"name": "H", "targets": [2]},
        ],
    },
    "grover": {
        "id": "algorithm_grover_4item",
        "name": "Grover's Search (4-item)",
        "description": "Grover's search algorithm for 4-item database",
        "problem_type": "search",
        "n_qubits": 2,
        "gates": [
            # Oracle for marking |11>
            {"name": "H", "targets": [0]},
            {"name": "H", "targets": [1]},
            {"name": "X", "targets": [0]},
            {"name": "X", "targets": [1]},
            {"name": "H", "targets": [1]},
            {"name": "CNOT", "controls": [0], "targets": [1]},
            {"name": "H", "targets": [1]},
            {"name": "X", "targets": [0]},
            {"name": "X", "targets": [1]},
            # Diffusion operator
            {"name": "H", "targets": [0]},
            {"name": "H", "targets": [1]},
            {"name": "X", "targets": [0]},
            {"name": "X", "targets": [1]},
            {"name": "H", "targets": [1]},
            {"name": "CNOT", "controls": [0], "targets": [1]},
            {"name": "H", "targets": [1]},
            {"name": "X", "targets": [0]},
            {"name": "X", "targets": [1]},
            {"name": "H", "targets": [0]},
            {"name": "H", "targets": [1]},
        ],
    },
}


def create_circuit_from_gates(gates_data: List[Dict], n_qubits: int, name: str) -> QuantumCircuit:
    """Create a QuantumCircuit from gate data."""
    circuit = QuantumCircuit(n_qubits, n_qubits, name=name)

    for gate_data in gates_data:
        gate_name = gate_data["name"]
        targets = gate_data["targets"]
        controls = gate_data.get("controls", [])
        params = gate_data.get("params", {})

        if gate_name == "H":
            circuit.h(targets[0])
        elif gate_name == "X":
            circuit.x(targets[0])
        elif gate_name == "Y":
            circuit.y(targets[0])
        elif gate_name == "Z":
            circuit.z(targets[0])
        elif gate_name == "CNOT":
            circuit.cnot(controls[0], targets[0])
        elif gate_name == "CZ":
            circuit.cz(controls[0], targets[0])
        elif gate_name == "RZ":
            circuit.rz(params.get("theta", 0), targets[0])
        elif gate_name == "RY":
            circuit.ry(params.get("theta", 0), targets[0])
        elif gate_name == "RX":
            circuit.rx(params.get("theta", 0), targets[0])
        elif gate_name == "SWAP":
            circuit.swap(targets[0], targets[1])

    return circuit


def print_separator(title: str = ""):
    """Print a section separator."""
    if title:
        print(f"\n{'='*60}")
        print(f"  {title}")
        print(f"{'='*60}")
    else:
        print(f"\n{'-'*60}")


def run_demo(algorithm_name: str, backend: str, save_code: bool, output_dir: str):
    """Run the complete pipeline demo."""
    if algorithm_name not in ALGORITHMS:
        print(f"Error: Unknown algorithm '{algorithm_name}'")
        print(f"Available algorithms: {', '.join(ALGORITHMS.keys())}")
        return 1

    algo_data = ALGORITHMS[algorithm_name]

    print_separator("QuantumAtlas Pipeline Demo")
    print(f"Algorithm: {algo_data['name']}")
    print(f"Backend: {backend}")
    print(f"Time: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")

    # Step 1: Create Circuit
    print_separator("Step 1: Circuit Design")
    print(f"Creating circuit with {algo_data['n_qubits']} qubits...")

    circuit = create_circuit_from_gates(
        algo_data["gates"],
        algo_data["n_qubits"],
        algo_data["name"]
    )

    print(f"✓ Circuit created: {circuit.name}")
    print(f"  Qubits: {circuit.num_qubits}")
    print(f"  Gates: {circuit.gate_count}")
    print(f"  Depth: {circuit.depth}")

    # Step 2: Create Quantum IR
    print_separator("Step 2: Quantum IR Generation")

    quantum_ir = QuantumIR(
        circuit=circuit,
        algorithm_id=algo_data["id"],
        optimization_level=0,
        metadata={
            "name": algo_data["name"],
            "description": algo_data["description"],
        }
    )

    print(f"✓ Quantum IR created")
    print(f"  Algorithm ID: {quantum_ir.algorithm_id}")

    # Step 3: Generate Code
    print_separator("Step 3: Code Generation")

    try:
        generator = CodeGenerator(backend=backend, use_formatter=True)
        code = generator.generate(quantum_ir)
        print(f"✓ {backend.upper()} code generated")
        print(f"  Lines: {len(code.splitlines())}")
    except ValueError as e:
        print(f"Error: {e}")
        return 1

    # Step 4: Validate Circuit
    print_separator("Step 4: Circuit Validation")

    try:
        validator = Validator()
        report = validator.validate(circuit)
        status = "PASSED" if report.passed else "FAILED"
        print(f"✓ Validation complete: {status}")
    except Exception as e:
        print(f"⚠ Validation warning: {e}")

    # Step 5: Estimate Resources
    print_separator("Step 5: Resource Estimation")

    try:
        estimator = ResourceEstimator()
        est_report = estimator.estimate(
            circuit=circuit,
            algorithm_name=algo_data["name"]
        )
        stats = est_report["resource_stats"]
        print(f"✓ Resource estimation complete")
        print(f"  Total gates: {stats.total_gates}")
        print(f"  Circuit depth: {stats.depth}")
        print(f"  Qubits: {stats.num_qubits}")
    except Exception as e:
        print(f"⚠ Estimation warning: {e}")

    # Output
    print_separator("Generated Code")
    print(code)

    # Save to file if requested
    if save_code:
        output_path = Path(output_dir)
        output_path.mkdir(parents=True, exist_ok=True)

        code_file = output_path / f"{algorithm_name}_{backend}.py"
        with open(code_file, 'w') as f:
            f.write(code)
        print(f"\n✓ Code saved to: {code_file}")

        # Also save Quantum IR
        ir_file = output_path / f"{algorithm_name}_ir.json"
        quantum_ir.save(str(ir_file))
        print(f"✓ Quantum IR saved to: {ir_file}")

    print_separator("Demo Complete")
    return 0


def main():
    parser = argparse.ArgumentParser(
        description="QuantumAtlas Pipeline Demo",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python examples/demo_pipeline.py --algorithm bell_state --backend qiskit
  python examples/demo_pipeline.py --algorithm grover --backend qpanda --save-code
        """
    )

    parser.add_argument(
        "--algorithm",
        choices=list(ALGORITHMS.keys()),
        default="bell_state",
        help="Algorithm to demo (default: bell_state)"
    )

    parser.add_argument(
        "--backend",
        choices=["qiskit", "qpanda"],
        default="qiskit",
        help="Code generation backend (default: qiskit)"
    )

    parser.add_argument(
        "--save-code",
        action="store_true",
        help="Save generated code to file"
    )

    parser.add_argument(
        "--output-dir",
        default="./output",
        help="Output directory for generated files (default: ./output)"
    )

    args = parser.parse_args()

    return run_demo(
        algorithm_name=args.algorithm,
        backend=args.backend,
        save_code=args.save_code,
        output_dir=args.output_dir
    )


if __name__ == "__main__":
    sys.exit(main())
