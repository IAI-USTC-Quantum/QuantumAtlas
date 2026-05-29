"""
Tests for estimator.py (main ResourceEstimator class)
"""

import json
import pytest
from pathlib import Path

from qatlas.designer.quantum_circuit import QuantumCircuit
from qatlas.estimator.estimator import ResourceEstimator
from qatlas.estimator.resource_analyzer import ResourceAnalyzer, ResourceStats
from qatlas.estimator.report_generator import ReportGenerator


class TestResourceEstimator:
    """Test cases for ResourceEstimator class."""
    
    @pytest.fixture
    def estimator(self):
        """Create a ResourceEstimator instance."""
        return ResourceEstimator()
    
    @pytest.fixture
    def bell_circuit(self):
        """Create a Bell state circuit."""
        circuit = QuantumCircuit(2, 2, name="Bell State")
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        return circuit
    
    def test_init_default(self):
        """Test initialization with default components."""
        estimator = ResourceEstimator()
        assert isinstance(estimator.analyzer, ResourceAnalyzer)
        assert isinstance(estimator.report_generator, ReportGenerator)
    
    def test_init_custom(self):
        """Test initialization with custom components."""
        analyzer = ResourceAnalyzer()
        generator = ReportGenerator()
        estimator = ResourceEstimator(analyzer=analyzer, report_generator=generator)
        
        assert estimator.analyzer is analyzer
        assert estimator.report_generator is generator
    
    def test_estimate_basic(self, estimator, bell_circuit):
        """Test basic estimation."""
        report = estimator.estimate(bell_circuit, algorithm_name="Bell State")
        
        # Check report structure
        assert "algorithm_name" in report
        assert report["algorithm_name"] == "Bell State"
        assert "resource_stats" in report
        assert "markdown_report" in report
        assert "json_report" in report
        
        # Check stats
        stats = report["resource_stats"]
        assert isinstance(stats, ResourceStats)
        assert stats.total_gates == 4
        assert stats.num_qubits == 2
    
    def test_estimate_with_hardware_params(self, estimator, bell_circuit):
        """Test estimation with hardware parameters."""
        hardware_params = {
            "gate_time": 50.0,
            "two_qubit_gate_time": 100.0,
            "measurement_time": 300.0,
            "coherence_time": 100.0,
        }
        
        report = estimator.estimate(
            bell_circuit,
            algorithm_name="Bell State",
            hardware_params=hardware_params,
        )
        
        # Should include execution time
        assert "execution_time" in report
        assert report["execution_time"] is not None
        assert report["execution_time"]["total_time_ms"] > 0
        
        # Stats should have estimated time
        assert report["resource_stats"].estimated_time_ms is not None
    
    def test_estimate_with_circuit_info(self, estimator, bell_circuit):
        """Test estimation with circuit info."""
        circuit_info = {
            "name": "Test Bell",
            "description": "A test Bell state circuit",
        }
        
        report = estimator.estimate(
            bell_circuit,
            algorithm_name="Bell State",
            circuit_info=circuit_info,
        )
        
        # Check that info appears in markdown report
        assert "Test Bell" in report["markdown_report"]
        assert "A test Bell state circuit" in report["markdown_report"]
    
    def test_estimate_json_report_structure(self, estimator, bell_circuit):
        """Test JSON report structure."""
        report = estimator.estimate(bell_circuit, algorithm_name="Bell State")
        
        json_report = report["json_report"]
        assert "report_metadata" in json_report
        assert "circuit_statistics" in json_report
        assert json_report["report_metadata"]["algorithm_name"] == "Bell State"
    
    def test_analyze_quick(self, estimator, bell_circuit):
        """Test quick analysis."""
        stats = estimator.analyze_quick(bell_circuit)
        
        assert isinstance(stats, ResourceStats)
        assert stats.total_gates == 4
        assert stats.num_qubits == 2
    
    def test_estimate_from_dict(self, estimator):
        """Test estimation from circuit dictionary."""
        circuit = QuantumCircuit(2, 2, name="Test")
        circuit.h(0)
        circuit.cnot(0, 1)
        
        circuit_dict = circuit.to_dict()
        
        report = estimator.estimate_from_dict(
            circuit_dict,
            algorithm_name="Test Circuit",
        )
        
        assert report["resource_stats"].total_gates == 2
        assert report["resource_stats"].num_qubits == 2
    
    def test_save_report_to_file_markdown(self, estimator, bell_circuit, tmp_path):
        """Test saving markdown report to file."""
        report = estimator.estimate(bell_circuit, algorithm_name="Bell State")
        
        output_path = tmp_path / "report"
        success = estimator.save_report_to_file(report, output_path, format="markdown")
        
        assert success is True
        assert (tmp_path / "report.md").exists()
        
        content = (tmp_path / "report.md").read_text()
        assert "# Resource Estimation Report: Bell State" in content
    
    def test_save_report_to_file_json(self, estimator, bell_circuit, tmp_path):
        """Test saving JSON report to file."""
        report = estimator.estimate(bell_circuit, algorithm_name="Bell State")
        
        output_path = tmp_path / "report"
        success = estimator.save_report_to_file(report, output_path, format="json")
        
        assert success is True
        assert (tmp_path / "report.json").exists()
        
        with open(tmp_path / "report.json") as f:
            data = json.load(f)
        
        assert data["report_metadata"]["algorithm_name"] == "Bell State"
    
    def test_save_report_to_file_both(self, estimator, bell_circuit, tmp_path):
        """Test saving both formats."""
        report = estimator.estimate(bell_circuit, algorithm_name="Bell State")
        
        output_path = tmp_path / "report"
        success = estimator.save_report_to_file(report, output_path, format="both")
        
        assert success is True
        assert (tmp_path / "report.md").exists()
        assert (tmp_path / "report.json").exists()
    
    def test_save_report_to_file_invalid_path(self, estimator, bell_circuit):
        """Test saving to invalid path."""
        report = estimator.estimate(bell_circuit, algorithm_name="Bell State")
        
        # Try to save to a non-existent directory without permissions
        success = estimator.save_report_to_file(
            report,
            "/nonexistent/path/report",
            format="markdown",
        )
        
        assert success is False
    
    def test_bell_state_accuracy(self, estimator):
        """Verify Bell state statistics are accurate."""
        circuit = QuantumCircuit(2, 2)
        circuit.h(0)
        circuit.cnot(0, 1)
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        
        stats = estimator.analyze_quick(circuit)
        
        # Verify counts match actual circuit
        assert stats.total_gates == 4
        assert stats.single_qubit_gates == 1  # H
        assert stats.two_qubit_gates == 1  # CNOT
        assert stats.measurement_gates == 2
        assert stats.num_qubits == 2
        assert stats.num_clbits == 2
        assert stats.qubits_used == 2
        assert stats.depth == 3
    
    def test_multi_gate_circuit(self, estimator):
        """Test multi-gate circuit accuracy."""
        circuit = QuantumCircuit(3, 3)
        
        # Add various gates
        circuit.h(0)  # Single qubit
        circuit.h(1)  # Single qubit
        circuit.cnot(0, 1)  # Two qubit
        circuit.t(2)  # Single qubit (T)
        circuit.x(2)  # Single qubit
        circuit.cz(1, 2)  # Two qubit
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        circuit.measure(2, 2)
        
        stats = estimator.analyze_quick(circuit)
        
        # Verify counts
        assert stats.total_gates == 9
        assert stats.single_qubit_gates == 4  # H, H, T, X
        assert stats.two_qubit_gates == 2  # CNOT, CZ
        assert stats.measurement_gates == 3
        assert stats.t_gates == 1
        
        # Check gate counts
        assert stats.gate_counts["H"] == 2
        assert stats.gate_counts["T"] == 1
        assert stats.gate_counts["X"] == 1
        assert stats.gate_counts["CNOT"] == 1
        assert stats.gate_counts["CZ"] == 1
        assert stats.gate_counts["MEASURE"] == 3
    
    def test_report_contains_all_data(self, estimator, bell_circuit):
        """Test that reports contain all expected data."""
        report = estimator.estimate(
            bell_circuit,
            algorithm_name="Bell State",
            hardware_params={"gate_time": 50.0},
        )
        
        # Markdown checks
        md = report["markdown_report"]
        assert "Bell State" in md
        assert "## Circuit Overview" in md
        assert "## Qubit Statistics" in md
        assert "## Gate Statistics" in md
        assert "Total Gates" in md or "total_gates" in md.lower()
        
        # JSON checks
        jr = report["json_report"]
        assert "circuit_statistics" in jr
        stats = jr["circuit_statistics"]
        assert stats["total_gates"] == 4
        assert stats["num_qubits"] == 2
        assert "execution_time_estimate" in jr
    
    def test_parallelism_calculation(self, estimator):
        """Test parallelism calculation in report."""
        # High parallelism circuit
        circuit = QuantumCircuit(4)
        for i in range(4):
            circuit.h(i)
        
        report = estimator.estimate(circuit, algorithm_name="Parallel Test")
        
        # Should have high parallelism
        assert report["resource_stats"].parallelism == 4.0
        
        # Should be in reports
        assert "4.00" in report["markdown_report"] or "4.0" in report["markdown_report"]
    
    def test_depth_dependency_analysis(self, estimator):
        """Test depth uses correct dependency analysis."""
        # Circuit with dependencies
        circuit = QuantumCircuit(3)
        circuit.h(0)  # Layer 1
        circuit.cnot(0, 1)  # Layer 2 (depends on H(0))
        circuit.cnot(1, 2)  # Layer 3 (depends on CNOT(0,1))
        circuit.x(2)  # Layer 4 (depends on CNOT(1,2))
        
        stats = estimator.analyze_quick(circuit)
        
        # Depth should be 4 due to dependencies
        assert stats.depth == 4