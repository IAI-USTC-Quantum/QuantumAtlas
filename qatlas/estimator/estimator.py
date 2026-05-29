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
    - Saving results to knowledge graph
    
    Example:
        estimator = ResourceEstimator()
        circuit = QuantumCircuit(2)
        circuit.h(0).cnot(0, 1)
        
        # Generate report
        report = estimator.estimate(circuit, algorithm_name="Bell State")
        
        # Save to knowledge graph
        estimator.save_to_knowledge_graph(algorithm_id="bell_state", report=report)
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
    
    def save_to_knowledge_graph(
        self,
        algorithm_id: str,
        report: Dict[str, Any],
        neo4j_client: Optional[Any] = None,
        neo4j_uri: Optional[str] = None,
        neo4j_user: Optional[str] = None,
        neo4j_password: Optional[str] = None,
    ) -> bool:
        """
        Save resource estimation results to Neo4j knowledge graph.
        
        Updates the Algorithm node with resource properties including:
        - gate_count: Total number of gates
        - depth: Circuit depth
        - qubit_count: Number of qubits used
        - t_gate_count: Number of T-gates
        - estimated_time_ms: Estimated execution time
        
        Args:
            algorithm_id: ID of the algorithm in the knowledge graph
            report: Report dictionary from estimate()
            neo4j_client: Existing Neo4j client instance (optional)
            neo4j_uri: Neo4j connection URI (optional)
            neo4j_user: Neo4j username (optional)
            neo4j_password: Neo4j password (optional)
            
        Returns:
            True if successful, False otherwise
        """
        should_close = False
        
        try:
            # Get or create Neo4j client
            if neo4j_client is None:
                try:
                    from qatlas.knowledge.neo4j_client import Neo4jClient
                except ImportError:
                    print("Warning: Neo4jClient not available. Install with: pip install neo4j-python-driver")
                    return False
                
                neo4j_client = Neo4jClient(
                    uri=neo4j_uri,
                    username=neo4j_user,
                    password=neo4j_password,
                )
                neo4j_client.connect()
                should_close = True
            
            # Extract resource stats
            stats = report.get("resource_stats")
            if not isinstance(stats, ResourceStats):
                # Convert dict to ResourceStats if needed
                if isinstance(stats, dict):
                    stats = ResourceStats(**stats)
                else:
                    print("Warning: Invalid resource stats in report")
                    return False
            
            # Build update query
            query = """
            MATCH (a:Algorithm {id: $algorithm_id})
            SET a.gate_count = $gate_count,
                a.depth = $depth,
                a.qubit_count = $qubit_count,
                a.t_gate_count = $t_gate_count,
                a.single_qubit_gates = $single_qubit_gates,
                a.two_qubit_gates = $two_qubit_gates,
                a.measurement_gates = $measurement_gates,
                a.parallelism = $parallelism,
                a.resource_estimated_at = datetime()
            """
            
            # Add estimated time if available
            params = {
                "algorithm_id": algorithm_id,
                "gate_count": stats.total_gates,
                "depth": stats.depth,
                "qubit_count": stats.num_qubits,
                "t_gate_count": stats.t_gates,
                "single_qubit_gates": stats.single_qubit_gates,
                "two_qubit_gates": stats.two_qubit_gates,
                "measurement_gates": stats.measurement_gates,
                "parallelism": stats.parallelism,
            }
            
            if stats.estimated_time_ms is not None:
                query += ",\n                a.estimated_time_ms = $estimated_time_ms"
                params["estimated_time_ms"] = stats.estimated_time_ms
            
            query += "\nRETURN a"
            
            # Execute query
            with neo4j_client.session() as session:
                result = session.run(query, **params)
                record = result.single()
                
                if record is None:
                    print(f"Warning: Algorithm {algorithm_id} not found in knowledge graph")
                    return False
                
                print(f"Successfully updated Algorithm {algorithm_id} with resource estimates")
                return True
                
        except Exception as e:
            print(f"Error saving to knowledge graph: {e}")
            return False
        
        finally:
            if should_close and neo4j_client is not None:
                try:
                    neo4j_client.close()
                except:
                    pass
    
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