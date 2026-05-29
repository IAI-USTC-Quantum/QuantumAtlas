"""
Resource Estimator Module

Main entry point for quantum circuit resource estimation.
Provides high-level interface for analyzing circuits and generating reports.
Integrates with Neo4j knowledge graph for saving estimation results.
"""

from typing import Any, Dict, List, Optional, Union
from pathlib import Path
import json

try:
    from qatlas.designer.quantum_circuit import QuantumCircuit
except ImportError:
    # Fallback for standalone usage
    QuantumCircuit = Any

from .resource_analyzer import ResourceAnalyzer, ResourceStats
from .report_generator import ReportGenerator


class ResourceEstimator:
    """
    Main resource estimator class.
    
    Provides a unified interface for:
    - Analyzing quantum circuits
    - Generating resource reports

    Example:
        estimator = ResourceEstimator()
        circuit = QuantumCircuit(2)
        circuit.h(0).cnot(0, 1)
        
        # Generate report
        report = estimator.estimate(circuit, algorithm_name="Bell State")
    """
    
    def __init__(
        self,
        analyzer: Optional[ResourceAnalyzer] = None,
        report_generator: Optional[ReportGenerator] = None,
    ):
        """
        Initialize the resource estimator.
        
        Args:
            analyzer: Resource analyzer instance (creates default if None)
            report_generator: Report generator instance (creates default if None)
        """
        self.analyzer = analyzer or ResourceAnalyzer()
        self.report_generator = report_generator or ReportGenerator()
    
    def estimate(
        self,
        circuit: QuantumCircuit,
        algorithm_name: str = "Unknown",
        circuit_info: Optional[Dict[str, Any]] = None,
        hardware_params: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """
        Perform complete resource estimation on a quantum circuit.
        
        Args:
            circuit: The quantum circuit to analyze
            algorithm_name: Name of the algorithm
            circuit_info: Additional circuit information
            hardware_params: Hardware parameters for time estimation
                - gate_time: Single-qubit gate time in ns (default: 50)
                - two_qubit_gate_time: Two-qubit gate time in ns (default: 100)
                - measurement_time: Measurement time in ns (default: 300)
                - coherence_time: Coherence time in μs (optional)
                
        Returns:
            Complete report dictionary containing:
            - resource_stats: ResourceStats object
            - execution_time: Execution time estimates
            - markdown_report: Markdown formatted report
            - json_report: JSON structured data
        """
        # Analyze circuit
        stats = self.analyzer.analyze(circuit)
        
        # Estimate execution time
        execution_time = None
        if hardware_params:
            execution_time = self.analyzer.estimate_execution_time(
                circuit,
                gate_time=hardware_params.get("gate_time", 50.0),
                two_qubit_gate_time=hardware_params.get("two_qubit_gate_time"),
                measurement_time=hardware_params.get("measurement_time", 300.0),
                coherence_time=hardware_params.get("coherence_time"),
            )
            stats.estimated_time_ms = execution_time["total_time_ms"]
        
        # Generate reports
        markdown_report = self.report_generator.generate_markdown(
            algorithm_name=algorithm_name,
            stats=stats,
            circuit_info=circuit_info,
            hardware_params=hardware_params,
            execution_time=execution_time,
        )
        
        json_report = self.report_generator.generate_json(
            algorithm_name=algorithm_name,
            stats=stats,
            circuit_info=circuit_info,
            hardware_params=hardware_params,
            execution_time=execution_time,
        )
        
        return {
            "algorithm_name": algorithm_name,
            "resource_stats": stats,
            "execution_time": execution_time,
            "markdown_report": markdown_report,
            "json_report": json_report,
        }
    
    def estimate_from_dict(
        self,
        circuit_dict: Dict[str, Any],
        algorithm_name: str = "Unknown",
        **kwargs,
    ) -> Dict[str, Any]:
        """
        Estimate resources from a circuit dictionary.
        
        Args:
            circuit_dict: Dictionary representation of a quantum circuit
            algorithm_name: Name of the algorithm
            **kwargs: Additional parameters passed to estimate()
            
        Returns:
            Complete report dictionary
        """
        circuit = QuantumCircuit.from_dict(circuit_dict)
        return self.estimate(circuit, algorithm_name, **kwargs)
    
    def analyze_quick(self, circuit: QuantumCircuit) -> ResourceStats:
        """
        Quick analysis without generating full report.
        
        Args:
            circuit: The quantum circuit to analyze
            
        Returns:
            ResourceStats object with analysis results
        """
        return self.analyzer.analyze(circuit)
    
    def save_report_to_file(
        self,
        report: Dict[str, Any],
        output_path: Union[str, Path],
        format: str = "markdown",
    ) -> bool:
        """
        Save report to a file.
        
        Args:
            report: Report dictionary from estimate()
            output_path: Path to save the report
            format: Output format - "markdown", "json", or "both"
            
        Returns:
            True if successful, False otherwise
        """
        output_path = Path(output_path)
        
        try:
            if format in ("markdown", "both"):
                md_path = output_path.with_suffix(".md")
                with open(md_path, "w", encoding="utf-8") as f:
                    f.write(report.get("markdown_report", ""))
                print(f"Saved Markdown report to {md_path}")
            
            if format in ("json", "both"):
                json_path = output_path.with_suffix(".json")
                with open(json_path, "w", encoding="utf-8") as f:
                    json.dump(report.get("json_report", {}), f, indent=2, default=str)
                print(f"Saved JSON report to {json_path}")
            
            return True
            
        except Exception as e:
            print(f"Error saving report to file: {e}")
            return False