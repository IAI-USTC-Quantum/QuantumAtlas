"""
Tests for report_generator.py
"""

import json
import pytest
from atlas.designer.quantum_circuit import QuantumCircuit
from atlas.estimator.resource_analyzer import ResourceAnalyzer
from atlas.estimator.report_generator import ReportGenerator, OptimizationSuggestion


class TestReportGenerator:
    """Test cases for ReportGenerator class."""
    
    @pytest.fixture
    def generator(self):
        """Create a ReportGenerator instance."""
        return ReportGenerator()
    
    @pytest.fixture
    def sample_stats(self):
        """Create sample resource stats."""
        analyzer = ResourceAnalyzer()
        circuit = QuantumCircuit(3, 2)
        circuit.h(0)
        circuit.h(1)
        circuit.cnot(0, 1)
        circuit.t(2)
        circuit.measure(0, 0)
        circuit.measure(1, 1)
        
        return analyzer.analyze(circuit)
    
    def test_generate_markdown_contains_key_sections(self, generator, sample_stats):
        """Test that markdown report contains all key sections."""
        report = generator.generate_markdown(
            algorithm_name="Test Algorithm",
            stats=sample_stats,
        )
        
        # Check for key sections
        assert "# Resource Estimation Report: Test Algorithm" in report
        assert "## Circuit Overview" in report
        assert "## Qubit Statistics" in report
        assert "## Gate Statistics" in report
        assert "## Circuit Depth Analysis" in report
    
    def test_generate_markdown_with_execution_time(self, generator, sample_stats):
        """Test markdown report with execution time data."""
        execution_time = {
            "total_time_ms": 1.5,
            "total_time_ns": 1500000,
            "gate_time_ns": 500000,
            "two_qubit_time_ns": 400000,
            "measurement_time_ns": 600000,
            "coherence_limited": False,
        }
        
        hardware_params = {
            "gate_time": 50.0,
            "two_qubit_gate_time": 100.0,
            "measurement_time": 300.0,
        }
        
        report = generator.generate_markdown(
            algorithm_name="Test Algorithm",
            stats=sample_stats,
            execution_time=execution_time,
            hardware_params=hardware_params,
        )
        
        # Check execution time section
        assert "## Execution Time Estimate" in report
        assert "1.500 ms" in report or "1.5" in report
        assert "Gate Time" in report
    
    def test_generate_markdown_coherence_warning(self, generator, sample_stats):
        """Test coherence warning in markdown report."""
        execution_time = {
            "total_time_ms": 5.0,
            "total_time_ns": 5000000,
            "gate_time_ns": 1000000,
            "two_qubit_time_ns": 2000000,
            "measurement_time_ns": 2000000,
            "coherence_limited": True,
        }
        
        report = generator.generate_markdown(
            algorithm_name="Test Algorithm",
            stats=sample_stats,
            execution_time=execution_time,
        )
        
        # Check for coherence warning
        assert "Coherence Limit" in report or "EXCEEDED" in report
    
    def test_generate_markdown_with_classical_comparison(self, generator):
        """Test markdown report with classical comparison for known algorithms."""
        analyzer = ResourceAnalyzer()
        circuit = QuantumCircuit(5)
        
        # Create a simple Grover-like circuit
        for i in range(5):
            circuit.h(i)
        
        stats = analyzer.analyze(circuit)
        
        report = generator.generate_markdown(
            algorithm_name="Grover Search",
            stats=stats,
        )
        
        # Check for classical comparison section
        assert "## Classical vs Quantum Comparison" in report
        assert "quadratic" in report.lower()
    
    def test_generate_markdown_with_suggestions(self, generator):
        """Test markdown report with optimization suggestions."""
        analyzer = ResourceAnalyzer()
        
        # Create a circuit with high T-gate count
        circuit = QuantumCircuit(5, 2)
        for i in range(150):
            circuit.t(i % 5)
        
        stats = analyzer.analyze(circuit)
        
        report = generator.generate_markdown(
            algorithm_name="High T-gate Circuit",
            stats=stats,
        )
        
        # Check for suggestions section
        assert "## Optimization Suggestions" in report
        assert "T-Gate" in report or "T-gate" in report
    
    def test_generate_json_structure(self, generator, sample_stats):
        """Test JSON report structure."""
        report = generator.generate_json(
            algorithm_name="Test Algorithm",
            stats=sample_stats,
        )
        
        # Check top-level structure
        assert "report_metadata" in report
        assert "circuit_statistics" in report
        
        # Check metadata
        assert report["report_metadata"]["algorithm_name"] == "Test Algorithm"
        assert "generated_at" in report["report_metadata"]
        
        # Check statistics
        assert report["circuit_statistics"]["total_gates"] == sample_stats.total_gates
        assert report["circuit_statistics"]["depth"] == sample_stats.depth
    
    def test_generate_json_with_execution_time(self, generator, sample_stats):
        """Test JSON report with execution time."""
        execution_time = {
            "total_time_ms": 2.0,
            "total_time_ns": 2000000,
            "gate_time_ns": 1000000,
            "two_qubit_time_ns": 500000,
            "measurement_time_ns": 500000,
            "coherence_limited": False,
        }
        
        hardware_params = {
            "gate_time": 50.0,
            "coherence_time": 100.0,
        }
        
        report = generator.generate_json(
            algorithm_name="Test Algorithm",
            stats=sample_stats,
            execution_time=execution_time,
            hardware_params=hardware_params,
        )
        
        # Check execution time section
        assert "execution_time_estimate" in report
        assert report["execution_time_estimate"]["total_ms"] == 2.0
        assert report["execution_time_estimate"]["coherence_limited"] is False
        assert "hardware_parameters" in report
    
    def test_generate_json_with_suggestions(self, generator):
        """Test JSON report with optimization suggestions."""
        analyzer = ResourceAnalyzer()
        
        # Create a circuit with coherence time exceeded
        circuit = QuantumCircuit(2)
        for _ in range(100):
            circuit.h(0)
        
        stats = analyzer.analyze(circuit)
        
        execution_time = {
            "total_time_ms": 10.0,
            "total_time_ns": 10000000,
            "gate_time_ns": 10000000,
            "two_qubit_time_ns": 0,
            "measurement_time_ns": 0,
            "coherence_limited": True,
        }
        
        report = generator.generate_json(
            algorithm_name="Test Algorithm",
            stats=stats,
            execution_time=execution_time,
        )
        
        # Check for optimization suggestions
        assert "optimization_suggestions" in report
        suggestions = report["optimization_suggestions"]
        assert len(suggestions) > 0
        # Should have coherence time warning
        coherence_suggestions = [s for s in suggestions if "coherence" in s["category"].lower()]
        assert len(coherence_suggestions) > 0
    
    def test_classical_comparison_grover(self, generator):
        """Test classical comparison for Grover."""
        comparison = generator._get_classical_comparison("Grover Search")
        assert comparison is not None
        assert "quadratic" in comparison["speedup"]
    
    def test_classical_comparison_shor(self, generator):
        """Test classical comparison for Shor."""
        comparison = generator._get_classical_comparison("Shor Factoring")
        assert comparison is not None
        assert "exponential" in comparison["speedup"]
    
    def test_classical_comparison_vqe(self, generator):
        """Test classical comparison for VQE."""
        comparison = generator._get_classical_comparison("VQE Algorithm")
        assert comparison is not None
        assert "heuristic" in comparison["speedup"]
    
    def test_classical_comparison_qaoa(self, generator):
        """Test classical comparison for QAOA."""
        comparison = generator._get_classical_comparison("QAOA")
        assert comparison is not None
        assert "heuristic" in comparison["speedup"]
    
    def test_classical_comparison_unknown(self, generator):
        """Test classical comparison for unknown algorithm."""
        comparison = generator._get_classical_comparison("Unknown Algorithm")
        assert comparison is None
    
    def test_suggestions_high_t_gates(self, generator):
        """Test high T-gate suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 200,
            't_gates': 150,
            'two_qubit_gates': 20,
            'num_qubits': 5,
            'qubits_used': 5,
            'depth': 100,
            'parallelism': 2.0,
        })()
        
        suggestions = generator._generate_suggestions(stats)
        
        # Should have high priority T-gate suggestion
        t_suggestions = [s for s in suggestions if "T-Gate" in s.category]
        assert len(t_suggestions) > 0
        assert t_suggestions[0].priority == "high"
    
    def test_suggestions_medium_t_gates(self, generator):
        """Test medium T-gate suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 50,
            't_gates': 20,
            'two_qubit_gates': 10,
            'num_qubits': 5,
            'qubits_used': 5,
            'depth': 30,
            'parallelism': 1.5,
        })()
        
        suggestions = generator._generate_suggestions(stats)
        
        # Should have medium priority T-gate suggestion
        t_suggestions = [s for s in suggestions if "T-Gate" in s.category]
        assert len(t_suggestions) > 0
        assert t_suggestions[0].priority == "medium"
    
    def test_suggestions_high_depth(self, generator):
        """Test high depth suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 10,
            't_gates': 0,
            'two_qubit_gates': 5,
            'num_qubits': 3,
            'qubits_used': 3,
            'depth': 9,  # 90% sequential
            'parallelism': 1.1,
        })()
        
        suggestions = generator._generate_suggestions(stats)
        
        # Should have depth suggestion
        depth_suggestions = [s for s in suggestions if "Depth" in s.category]
        assert len(depth_suggestions) > 0
    
    def test_suggestions_low_parallelism(self, generator):
        """Test low parallelism suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 20,
            't_gates': 0,
            'two_qubit_gates': 5,
            'num_qubits': 5,
            'qubits_used': 5,
            'depth': 15,
            'parallelism': 1.1,  # Low parallelism
        })()
        
        suggestions = generator._generate_suggestions(stats)
        
        # Should have parallelization suggestion
        par_suggestions = [s for s in suggestions if "Parallel" in s.category]
        assert len(par_suggestions) > 0
    
    def test_suggestions_coherence_limited(self, generator):
        """Test coherence time exceeded suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 10,
            't_gates': 0,
            'two_qubit_gates': 5,
            'num_qubits': 3,
            'qubits_used': 3,
            'depth': 10,
            'parallelism': 1.0,
        })()
        
        execution_time = {"coherence_limited": True}
        
        suggestions = generator._generate_suggestions(stats, execution_time)
        
        # Should have coherence time suggestion
        coherence_suggestions = [s for s in suggestions if "Coherence" in s.category]
        assert len(coherence_suggestions) > 0
        assert coherence_suggestions[0].priority == "high"
    
    def test_suggestions_unused_qubits(self, generator):
        """Test unused qubits suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 5,
            't_gates': 0,
            'two_qubit_gates': 0,
            'num_qubits': 10,
            'qubits_used': 2,  # 8 unused
            'depth': 5,
            'parallelism': 1.0,
        })()
        
        suggestions = generator._generate_suggestions(stats)
        
        # Should have resource utilization suggestion
        util_suggestions = [s for s in suggestions if "Resource Utilization" in s.category]
        assert len(util_suggestions) > 0
    
    def test_suggestions_high_two_qubit_ratio(self, generator):
        """Test high two-qubit gate ratio suggestions."""
        stats = type('obj', (object,), {
            'total_gates': 10,
            't_gates': 0,
            'two_qubit_gates': 6,  # 60% two-qubit
            'num_qubits': 3,
            'qubits_used': 3,
            'depth': 8,
            'parallelism': 1.25,
        })()
        
        suggestions = generator._generate_suggestions(stats)
        
        # Should have two-qubit gate suggestion
        two_q_suggestions = [s for s in suggestions if "Two-Qubit" in s.category]
        assert len(two_q_suggestions) > 0
    
    def test_optimization_suggestion_dataclass(self):
        """Test OptimizationSuggestion dataclass."""
        suggestion = OptimizationSuggestion(
            category="Test",
            description="Test description",
            priority="high",
            potential_impact="High impact",
        )
        
        assert suggestion.category == "Test"
        assert suggestion.description == "Test description"
        assert suggestion.priority == "high"
        assert suggestion.potential_impact == "High impact"