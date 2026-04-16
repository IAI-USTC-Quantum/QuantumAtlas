"""
参考实现对比模块

提供与已知正确实现的对比功能，包括：
- 与 Qiskit 等框架的内置算法对比
- 门数量、深度等指标差异分析
- 异常检测和报告生成
"""

import numpy as np
from typing import Dict, Any, List, Optional, Callable, Union
from dataclasses import dataclass, field
from enum import Enum
import logging

from atlas.designer.quantum_circuit import QuantumCircuit, Gate

logger = logging.getLogger(__name__)


class ComparisonStatus(Enum):
    """对比状态枚举"""
    MATCH = "match"           # 完全匹配
    EQUIVALENT = "equivalent" # 等价但有差异（如全局相位）
    DIFFERENT = "different"   # 不同
    ERROR = "error"           # 对比出错
    SKIPPED = "skipped"       # 跳过


@dataclass
class MetricComparison:
    """指标对比结果"""
    metric_name: str
    actual_value: Any
    reference_value: Any
    difference: Any = None
    ratio: Optional[float] = None
    within_tolerance: bool = True


@dataclass
class ReferenceComparisonResult:
    """参考实现对比结果"""
    circuit_name: str
    reference_name: str
    status: ComparisonStatus
    metric_comparisons: List[MetricComparison] = field(default_factory=list)
    equivalence_result: Optional[Any] = None
    error_message: str = ""
    warnings: List[str] = field(default_factory=list)
    
    @property
    def is_equivalent(self) -> bool:
        """检查是否等价"""
        return self.status in [ComparisonStatus.MATCH, ComparisonStatus.EQUIVALENT]


class ReferenceComparator:
    """
    参考实现对比器
    
    将待验证电路与已知正确的参考实现进行对比
    """
    
    def __init__(
        self,
        tolerance: float = 1e-10,
        matrix_size_limit: int = 10
    ):
        """
        初始化参考对比器
        
        Args:
            tolerance: 数值容差
            matrix_size_limit: 矩阵大小限制（用于等价性检查）
        """
        self.tolerance = tolerance
        self.matrix_size_limit = matrix_size_limit
        self._reference_implementations: Dict[str, Callable] = {}
    
    def register_reference(
        self, 
        name: str, 
        generator: Callable[..., QuantumCircuit]
    ) -> None:
        """
        注册参考实现
        
        Args:
            name: 参考实现名称
            generator: 生成参考电路的函数
        """
        self._reference_implementations[name] = generator
        logger.info(f"Registered reference implementation: {name}")
    
    def compare_with_reference(
        self,
        circuit: QuantumCircuit,
        reference_name: str,
        reference_params: Optional[Dict[str, Any]] = None
    ) -> ReferenceComparisonResult:
        """
        与参考实现进行对比
        
        Args:
            circuit: 待验证电路
            reference_name: 参考实现名称
            reference_params: 生成参考电路的参数
            
        Returns:
            ReferenceComparisonResult 对比结果
        """
        if reference_name not in self._reference_implementations:
            return ReferenceComparisonResult(
                circuit_name=circuit.name,
                reference_name=reference_name,
                status=ComparisonStatus.ERROR,
                error_message=f"Unknown reference implementation: {reference_name}"
            )
        
        try:
            # 生成参考电路
            generator = self._reference_implementations[reference_name]
            params = reference_params or {}
            reference_circuit = generator(**params)
            
            return self.compare_circuits(circuit, reference_circuit, reference_name)
            
        except Exception as e:
            logger.error(f"Error comparing with reference {reference_name}: {e}")
            return ReferenceComparisonResult(
                circuit_name=circuit.name,
                reference_name=reference_name,
                status=ComparisonStatus.ERROR,
                error_message=str(e)
            )
    
    def compare_circuits(
        self,
        circuit: QuantumCircuit,
        reference_circuit: QuantumCircuit,
        reference_name: str = "custom"
    ) -> ReferenceComparisonResult:
        """
        对比两个电路
        
        Args:
            circuit: 待验证电路
            reference_circuit: 参考电路
            reference_name: 参考实现名称
            
        Returns:
            ReferenceComparisonResult 对比结果
        """
        result = ReferenceComparisonResult(
            circuit_name=circuit.name,
            reference_name=reference_name,
            status=ComparisonStatus.SKIPPED  # 默认状态，后续会更新
        )
        
        # 1. 对比基本指标
        metric_comparisons = self._compare_metrics(circuit, reference_circuit)
        result.metric_comparisons = metric_comparisons
        
        # 2. 检查等价性（如果可能）
        if circuit.num_qubits <= self.matrix_size_limit:
            try:
                from atlas.validator.equivalence_checker import EquivalenceChecker
                checker = EquivalenceChecker(matrix_size_limit=self.matrix_size_limit)
                equiv_result = checker.check_equivalence(circuit, reference_circuit)
                result.equivalence_result = equiv_result
                
                if equiv_result.is_equivalent:
                    if equiv_result.phase_difference and abs(equiv_result.phase_difference) > self.tolerance:
                        result.status = ComparisonStatus.EQUIVALENT
                        result.warnings.append(
                            f"Circuits are equivalent up to global phase: {equiv_result.phase_difference:.6f}"
                        )
                    else:
                        result.status = ComparisonStatus.MATCH
                else:
                    result.status = ComparisonStatus.DIFFERENT
                    result.error_message = equiv_result.error_message
                    
            except Exception as e:
                logger.warning(f"Equivalence check failed: {e}")
                result.status = ComparisonStatus.SKIPPED
                result.warnings.append(f"Could not perform equivalence check: {e}")
        else:
            result.status = ComparisonStatus.SKIPPED
            result.warnings.append(
                f"Circuit too large ({circuit.num_qubits} qubits) for equivalence check"
            )
        
        return result
    
    def _compare_metrics(
        self,
        circuit: QuantumCircuit,
        reference_circuit: QuantumCircuit
    ) -> List[MetricComparison]:
        """对比电路指标"""
        comparisons = []
        
        # 量子比特数
        comparisons.append(MetricComparison(
            metric_name="num_qubits",
            actual_value=circuit.num_qubits,
            reference_value=reference_circuit.num_qubits,
            within_tolerance=circuit.num_qubits == reference_circuit.num_qubits
        ))
        
        # 经典比特数
        comparisons.append(MetricComparison(
            metric_name="num_clbits",
            actual_value=circuit.num_clbits,
            reference_value=reference_circuit.num_clbits,
            within_tolerance=circuit.num_clbits == reference_circuit.num_clbits
        ))
        
        # 门数量
        gate_count_diff = circuit.gate_count - reference_circuit.gate_count
        gate_count_ratio = (
            circuit.gate_count / reference_circuit.gate_count 
            if reference_circuit.gate_count > 0 else float('inf')
        )
        comparisons.append(MetricComparison(
            metric_name="gate_count",
            actual_value=circuit.gate_count,
            reference_value=reference_circuit.gate_count,
            difference=gate_count_diff,
            ratio=gate_count_ratio,
            within_tolerance=abs(gate_count_diff) <= 2  # 允许少量差异
        ))
        
        # 电路深度
        depth_diff = circuit.depth - reference_circuit.depth
        depth_ratio = (
            circuit.depth / reference_circuit.depth 
            if reference_circuit.depth > 0 else float('inf')
        )
        comparisons.append(MetricComparison(
            metric_name="depth",
            actual_value=circuit.depth,
            reference_value=reference_circuit.depth,
            difference=depth_diff,
            ratio=depth_ratio,
            within_tolerance=depth_diff <= 2  # 允许少量差异
        ))
        
        # 两量子比特门数量
        actual_two_qubit = circuit.num_nonlocal_gates()
        reference_two_qubit = reference_circuit.num_nonlocal_gates()
        two_qubit_diff = actual_two_qubit - reference_two_qubit
        comparisons.append(MetricComparison(
            metric_name="two_qubit_gates",
            actual_value=actual_two_qubit,
            reference_value=reference_two_qubit,
            difference=two_qubit_diff,
            within_tolerance=abs(two_qubit_diff) <= 1
        ))
        
        # 各类型门数量对比
        actual_counts = circuit.gate_counts_by_type()
        reference_counts = reference_circuit.gate_counts_by_type()
        all_gate_types = set(actual_counts.keys()) | set(reference_counts.keys())
        
        for gate_type in sorted(all_gate_types):
            actual = actual_counts.get(gate_type, 0)
            reference = reference_counts.get(gate_type, 0)
            diff = actual - reference
            
            comparisons.append(MetricComparison(
                metric_name=f"gate_{gate_type}",
                actual_value=actual,
                reference_value=reference,
                difference=diff,
                within_tolerance=abs(diff) <= 1
            ))
        
        return comparisons
    
    def detect_anomalies(
        self,
        result: ReferenceComparisonResult,
        gate_count_threshold: float = 2.0,
        depth_threshold: float = 2.0
    ) -> List[str]:
        """
        检测异常差异
        
        Args:
            result: 对比结果
            gate_count_threshold: 门数量差异阈值（倍数）
            depth_threshold: 深度差异阈值（倍数）
            
        Returns:
            异常警告列表
        """
        anomalies = []
        
        for mc in result.metric_comparisons:
            if mc.metric_name == "gate_count" and mc.ratio:
                if mc.ratio > gate_count_threshold:
                    anomalies.append(
                        f"Gate count is {mc.ratio:.1f}x higher than reference "
                        f"({mc.actual_value} vs {mc.reference_value})"
                    )
                elif mc.ratio < 1.0 / gate_count_threshold:
                    anomalies.append(
                        f"Gate count is {1.0/mc.ratio:.1f}x lower than reference "
                        f"({mc.actual_value} vs {mc.reference_value})"
                    )
            
            elif mc.metric_name == "depth" and mc.ratio:
                if mc.ratio > depth_threshold:
                    anomalies.append(
                        f"Circuit depth is {mc.ratio:.1f}x deeper than reference "
                        f"({mc.actual_value} vs {mc.reference_value})"
                    )
                elif mc.ratio < 1.0 / depth_threshold:
                    anomalies.append(
                        f"Circuit depth is {1.0/mc.ratio:.1f}x shallower than reference "
                        f"({mc.actual_value} vs {mc.reference_value})"
                    )
        
        return anomalies
    
    def generate_report(
        self,
        result: ReferenceComparisonResult,
        verbose: bool = False
    ) -> str:
        """
        生成对比报告
        
        Args:
            result: 对比结果
            verbose: 是否包含详细信息
            
        Returns:
            格式化报告字符串
        """
        lines = []
        lines.append("=" * 60)
        lines.append(f"Reference Comparison Report")
        lines.append("=" * 60)
        lines.append(f"Circuit: {result.circuit_name}")
        lines.append(f"Reference: {result.reference_name}")
        lines.append(f"Status: {result.status.value.upper()}")
        lines.append("")
        
        # 等价性结果
        if result.equivalence_result:
            lines.append("-" * 40)
            lines.append("Equivalence Check:")
            lines.append("-" * 40)
            equiv = result.equivalence_result
            lines.append(f"  Equivalent: {equiv.is_equivalent}")
            if equiv.phase_difference is not None:
                lines.append(f"  Phase Difference: {equiv.phase_difference:.6f}")
            if equiv.max_difference > 0:
                lines.append(f"  Max Difference: {equiv.max_difference:.2e}")
            lines.append("")
        
        # 指标对比
        lines.append("-" * 40)
        lines.append("Metric Comparison:")
        lines.append("-" * 40)
        
        for mc in result.metric_comparisons:
            status = "✓" if mc.within_tolerance else "✗"
            lines.append(f"  {status} {mc.metric_name}:")
            lines.append(f"    Actual:   {mc.actual_value}")
            lines.append(f"    Reference: {mc.reference_value}")
            if mc.difference is not None:
                lines.append(f"    Difference: {mc.difference:+d}")
            if mc.ratio is not None and mc.metric_name in ["gate_count", "depth"]:
                lines.append(f"    Ratio: {mc.ratio:.2f}x")
            lines.append("")
        
        # 警告
        if result.warnings:
            lines.append("-" * 40)
            lines.append("Warnings:")
            lines.append("-" * 40)
            for warning in result.warnings:
                lines.append(f"  ⚠ {warning}")
            lines.append("")
        
        # 错误信息
        if result.error_message:
            lines.append("-" * 40)
            lines.append("Error:")
            lines.append("-" * 40)
            lines.append(f"  {result.error_message}")
            lines.append("")
        
        lines.append("=" * 60)
        
        return "\n".join(lines)
    
    def create_builtin_references(self) -> None:
        """
        创建内置的参考实现
        
        包括常见的量子算法参考实现
        """
        # Bell State 参考实现
        def bell_state() -> QuantumCircuit:
            qc = QuantumCircuit(2, name="Bell State")
            qc.h(0)
            qc.cnot(0, 1)
            return qc

        self.register_reference("bell_state", bell_state)

        # GHZ State 参考实现
        def ghz_state(n_qubits: int = 3) -> QuantumCircuit:
            qc = QuantumCircuit(n_qubits, name=f"GHZ State ({n_qubits} qubits)")
            qc.h(0)
            for i in range(n_qubits - 1):
                qc.cnot(i, i + 1)
            return qc
        
        self.register_reference("ghz_state", ghz_state)
        
        # QFT 参考实现（简化版）
        def qft_reference(n_qubits: int = 3) -> QuantumCircuit:
            qc = QuantumCircuit(n_qubits, name=f"QFT ({n_qubits} qubits)")
            for i in range(n_qubits):
                qc.h(i)
                for j in range(i + 1, n_qubits):
                    # 简化的旋转门序列
                    qc.cz(i, j)
            return qc
        
        self.register_reference("qft", qft_reference)
        
        logger.info("Created builtin reference implementations")