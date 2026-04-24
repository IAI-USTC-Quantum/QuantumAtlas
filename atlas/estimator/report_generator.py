"""
Report Generator Module

Generates formatted reports from resource analysis results.
Supports multiple output formats including Markdown and JSON.
"""

import json
from typing import Any, Dict, List, Optional
from dataclasses import dataclass
from datetime import datetime

from .resource_analyzer import ResourceStats


@dataclass
class OptimizationSuggestion:
    """Represents an optimization suggestion."""
    category: str
    description: str
    priority: str  # "high", "medium", "low"
    potential_impact: Optional[str] = None


class ReportGenerator:
    """
    Generates resource estimation reports in various formats.
    
    Supports:
    - Markdown: Human-readable formatted reports
    - JSON: Machine-readable structured data
    """
    
    # Classical comparison data (example benchmarks)
    CLASSICAL_COMPARISONS = {
        "grover": {
            "classical_complexity": "O(N)",
            "quantum_complexity": "O(√N)",
            "speedup": "quadratic",
            "description": "Quantum search provides quadratic speedup over classical linear search",
        },
        "shor": {
            "classical_complexity": "O(exp((log N)^(1/3) * (log log N)^(2/3)))",
            "quantum_complexity": "O((log N)^3)",
            "speedup": "exponential",
            "description": "Quantum factoring provides exponential speedup over classical algorithms",
        },
        "vqe": {
            "classical_complexity": "O(2^N) for exact diagonalization",
            "quantum_complexity": "O(poly(N)) per iteration",
            "speedup": "heuristic",
            "description": "VQE provides potential polynomial speedup for molecular simulation",
        },
        "qaoa": {
            "classical_complexity": "O(2^N) for exact optimization",
            "quantum_complexity": "O(poly(N) * 2^p) where p is layers",
            "speedup": "heuristic",
            "description": "QAOA provides potential speedup for combinatorial optimization",
        },
    }
    
    def __init__(self):
        """Initialize the report generator."""
        pass
    
    def generate_markdown(
        self, 
        algorithm_name: str,
        stats: ResourceStats,
        circuit_info: Optional[Dict[str, Any]] = None,
        hardware_params: Optional[Dict[str, Any]] = None,
        execution_time: Optional[Dict[str, Any]] = None,
    ) -> str:
        """
        Generate a Markdown formatted report.
        
        Args:
            algorithm_name: Name of the algorithm
            stats: Resource statistics from analysis
            circuit_info: Additional circuit information
            hardware_params: Hardware parameters used for estimation
            execution_time: Execution time estimation results
            
        Returns:
            Markdown formatted report string
        """
        lines = []
        
        # Header
        lines.append(f"# Resource Estimation Report: {algorithm_name}")
        lines.append("")
        lines.append(f"Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
        lines.append("")
        
        # Circuit Overview
        lines.append("## Circuit Overview")
        lines.append("")
        lines.append(f"- **Algorithm**: {algorithm_name}")
        if circuit_info:
            lines.append(f"- **Circuit Name**: {circuit_info.get('name', 'N/A')}")
            lines.append(f"- **Description**: {circuit_info.get('description', 'N/A')}")
        lines.append("")
        
        # Qubit Statistics
        lines.append("## Qubit Statistics")
        lines.append("")
        lines.append(f"| Metric | Value |")
        lines.append(f"|--------|-------|")
        lines.append(f"| Total Qubits | {stats.num_qubits} |")
        lines.append(f"| Qubits Used | {stats.qubits_used} |")
        lines.append(f"| Classical Bits | {stats.num_clbits} |")
        lines.append(f"| Utilization | {stats.qubits_used / stats.num_qubits * 100:.1f}% |" if stats.num_qubits > 0 else "| Utilization | N/A |")
        lines.append("")
        
        # Gate Statistics
        lines.append("## Gate Statistics")
        lines.append("")
        lines.append(f"| Metric | Value |")
        lines.append(f"|--------|-------|")
        lines.append(f"| Total Gates | {stats.total_gates} |")
        lines.append(f"| Single-Qubit Gates | {stats.single_qubit_gates} |")
        lines.append(f"| Two-Qubit Gates | {stats.two_qubit_gates} |")
        lines.append(f"| Measurement Operations | {stats.measurement_gates} |")
        lines.append(f"| T-Gates | {stats.t_gates} |")
        lines.append("")
        
        # Gate Breakdown
        if stats.gate_counts:
            lines.append("### Gate Type Breakdown")
            lines.append("")
            lines.append(f"| Gate Type | Count | Percentage |")
            lines.append(f"|-----------|-------|------------|")
            for gate_name, count in sorted(stats.gate_counts.items(), key=lambda x: -x[1]):
                pct = count / stats.total_gates * 100 if stats.total_gates > 0 else 0
                lines.append(f"| {gate_name} | {count} | {pct:.1f}% |")
            lines.append("")
        
        # Circuit Depth and Parallelism
        lines.append("## Circuit Depth Analysis")
        lines.append("")
        lines.append(f"| Metric | Value |")
        lines.append(f"|--------|-------|")
        lines.append(f"| Circuit Depth | {stats.depth} |")
        lines.append(f"| Parallelism Ratio | {stats.parallelism:.2f} |")
        lines.append("")
        
        # Execution Time Estimate
        if execution_time:
            lines.append("## Execution Time Estimate")
            lines.append("")
            lines.append(f"| Metric | Value |")
            lines.append(f"|--------|-------|")
            lines.append(f"| Total Time | {execution_time['total_time_ms']:.3f} ms |")
            lines.append(f"| Single-Qubit Time | {execution_time['gate_time_ns'] / 1_000_000:.3f} ms |")
            lines.append(f"| Two-Qubit Time | {execution_time['two_qubit_time_ns'] / 1_000_000:.3f} ms |")
            lines.append(f"| Measurement Time | {execution_time['measurement_time_ns'] / 1_000_000:.3f} ms |")
            if execution_time.get('coherence_limited'):
                lines.append(f"| ⚠️ Coherence Limit | EXCEEDED |")
            lines.append("")
            
            if hardware_params:
                lines.append("### Hardware Parameters")
                lines.append("")
                lines.append(f"- Gate Time: {hardware_params.get('gate_time', 'N/A')} ns")
                lines.append(f"- Two-Qubit Gate Time: {hardware_params.get('two_qubit_gate_time', 'N/A')} ns")
                lines.append(f"- Measurement Time: {hardware_params.get('measurement_time', 'N/A')} ns")
                if 'coherence_time' in hardware_params:
                    lines.append(f"- Coherence Time: {hardware_params['coherence_time']} μs")
                lines.append("")
        
        # Classical Comparison
        comparison = self._get_classical_comparison(algorithm_name)
        if comparison:
            lines.append("## Classical vs Quantum Comparison")
            lines.append("")
            lines.append(f"**{comparison['description']}**")
            lines.append("")
            lines.append(f"| Aspect | Value |")
            lines.append(f"|--------|-------|")
            lines.append(f"| Classical Complexity | {comparison['classical_complexity']} |")
            lines.append(f"| Quantum Complexity | {comparison['quantum_complexity']} |")
            lines.append(f"| Speedup | {comparison['speedup']} |")
            lines.append("")
        
        # Optimization Suggestions
        suggestions = self._generate_suggestions(stats, execution_time)
        if suggestions:
            lines.append("## Optimization Suggestions")
            lines.append("")
            for i, suggestion in enumerate(suggestions, 1):
                priority_emoji = {"high": "🔴", "medium": "🟡", "low": "🟢"}.get(suggestion.priority, "⚪")
                lines.append(f"### {i}. {priority_emoji} {suggestion.category}")
                lines.append("")
                lines.append(f"**{suggestion.description}**")
                lines.append("")
                if suggestion.potential_impact:
                    lines.append(f"*Potential Impact: {suggestion.potential_impact}*")
                    lines.append("")
        
        # Summary
        lines.append("## Summary")
        lines.append("")
        lines.append(f"This circuit uses **{stats.num_qubits} qubits** with a depth of **{stats.depth}**,")
        lines.append(f"requiring **{stats.total_gates} gates** including **{stats.t_gates} T-gates**.")
        if execution_time:
            lines.append(f"Estimated execution time is **{execution_time['total_time_ms']:.3f} ms**.")
        lines.append("")
        
        return "\n".join(lines)
    
    def generate_json(
        self,
        algorithm_name: str,
        stats: ResourceStats,
        circuit_info: Optional[Dict[str, Any]] = None,
        hardware_params: Optional[Dict[str, Any]] = None,
        execution_time: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """
        Generate a JSON-formatted report (machine-readable).
        
        Args:
            algorithm_name: Name of the algorithm
            stats: Resource statistics from analysis
            circuit_info: Additional circuit information
            hardware_params: Hardware parameters used for estimation
            execution_time: Execution time estimation results
            
        Returns:
            Dictionary with report data (can be serialized to JSON)
        """
        report = {
            "report_metadata": {
                "algorithm_name": algorithm_name,
                "generated_at": datetime.now().isoformat(),
                "version": "1.0",
            },
            "circuit_statistics": stats.to_dict(),
        }
        
        if circuit_info:
            report["circuit_info"] = circuit_info
        
        if hardware_params:
            report["hardware_parameters"] = hardware_params
        
        if execution_time:
            report["execution_time_estimate"] = {
                "total_ms": execution_time["total_time_ms"],
                "total_ns": execution_time["total_time_ns"],
                "breakdown": {
                    "single_qubit_ns": execution_time["gate_time_ns"],
                    "two_qubit_ns": execution_time["two_qubit_time_ns"],
                    "measurement_ns": execution_time["measurement_time_ns"],
                },
                "coherence_limited": execution_time.get("coherence_limited", False),
            }
        
        # Classical comparison
        comparison = self._get_classical_comparison(algorithm_name)
        if comparison:
            report["classical_comparison"] = comparison
        
        # Optimization suggestions
        suggestions = self._generate_suggestions(stats, execution_time)
        if suggestions:
            report["optimization_suggestions"] = [
                {
                    "category": s.category,
                    "description": s.description,
                    "priority": s.priority,
                    "potential_impact": s.potential_impact,
                }
                for s in suggestions
            ]
        
        return report
    
    def _get_classical_comparison(self, algorithm_name: str) -> Optional[Dict[str, str]]:
        """
        Get classical vs quantum comparison data for an algorithm.
        
        Args:
            algorithm_name: Name of the algorithm
            
        Returns:
            Comparison dictionary or None
        """
        name_lower = algorithm_name.lower()
        for key, data in self.CLASSICAL_COMPARISONS.items():
            if key in name_lower:
                return data
        return None
    
    def _generate_suggestions(
        self, 
        stats: ResourceStats,
        execution_time: Optional[Dict[str, Any]] = None,
    ) -> List[OptimizationSuggestion]:
        """
        Generate optimization suggestions based on analysis.
        
        Args:
            stats: Resource statistics
            execution_time: Execution time estimation
            
        Returns:
            List of optimization suggestions
        """
        suggestions = []
        
        # High T-gate count
        if stats.t_gates > 100:
            suggestions.append(OptimizationSuggestion(
                category="T-Gate Reduction",
                description=f"High T-gate count ({stats.t_gates}). Consider T-gate optimization techniques "
                           "or approximation methods to reduce fault-tolerant overhead.",
                priority="high",
                potential_impact="Significant reduction in fault-tolerant resource requirements",
            ))
        elif stats.t_gates > 10:
            suggestions.append(OptimizationSuggestion(
                category="T-Gate Optimization",
                description=f"Moderate T-gate count ({stats.t_gates}). Review if all T-gates are necessary.",
                priority="medium",
                potential_impact="Reduced T-gate factory requirements",
            ))
        
        # High depth
        if stats.depth > stats.total_gates * 0.8:
            suggestions.append(OptimizationSuggestion(
                category="Circuit Depth",
                description=f"Circuit is mostly sequential (depth {stats.depth} vs {stats.total_gates} gates). "
                           "Look for opportunities to parallelize operations.",
                priority="medium",
                potential_impact="Reduced execution time and decoherence effects",
            ))
        
        # Low parallelism
        if stats.parallelism < 1.2 and stats.total_gates > 5:
            suggestions.append(OptimizationSuggestion(
                category="Parallelization",
                description="Low parallelism detected. Consider reordering gates to maximize parallel execution.",
                priority="low",
                potential_impact="Improved circuit execution time",
            ))
        
        # Coherence time exceeded
        if execution_time and execution_time.get("coherence_limited"):
            suggestions.append(OptimizationSuggestion(
                category="Coherence Time",
                description="Estimated execution time exceeds qubit coherence time. "
                           "Circuit may suffer from decoherence errors.",
                priority="high",
                potential_impact="Prevention of decoherence-induced errors",
            ))
        
        # Unused qubits
        unused = stats.num_qubits - stats.qubits_used
        if unused > 0:
            suggestions.append(OptimizationSuggestion(
                category="Resource Utilization",
                description=f"{unused} qubit(s) are allocated but unused. Consider removing unused qubits.",
                priority="low",
                potential_impact="More efficient use of quantum hardware",
            ))
        
        # High two-qubit gate ratio
        if stats.total_gates > 0:
            two_qubit_ratio = stats.two_qubit_gates / stats.total_gates
            if two_qubit_ratio > 0.5:
                suggestions.append(OptimizationSuggestion(
                    category="Two-Qubit Gates",
                    description=f"High ratio of two-qubit gates ({two_qubit_ratio*100:.1f}%). "
                               "Two-qubit gates are typically noisier and slower.",
                    priority="medium",
                    potential_impact="Improved gate fidelity",
                ))
        
        return suggestions