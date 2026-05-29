"""
测试参考实现对比模块

包含10+测试用例：
- 参考实现注册和对比
- 指标比较
- 异常检测
"""

import pytest
import numpy as np

from qatlas.designer.quantum_circuit import QuantumCircuit
from qatlas.validator.reference_comparison import (
    ReferenceComparator, ReferenceComparisonResult,
    ComparisonStatus, MetricComparison
)


class TestReferenceComparatorBasics:
    """测试 ReferenceComparator 基础功能"""
    
    def test_initialization(self):
        """测试初始化"""
        comp = ReferenceComparator(tolerance=1e-8, matrix_size_limit=8)
        
        assert comp.tolerance == 1e-8
        assert comp.matrix_size_limit == 8
    
    def test_default_initialization(self):
        """测试默认初始化"""
        comp = ReferenceComparator()
        
        assert comp.tolerance == 1e-10
        assert comp.matrix_size_limit == 10


class TestReferenceRegistration:
    """测试参考实现注册"""
    
    def test_register_reference(self):
        """测试注册参考实现"""
        comp = ReferenceComparator()
        
        def ref_gen():
            return QuantumCircuit(1)
        
        comp.register_reference("test_ref", ref_gen)
        
        assert "test_ref" in comp._reference_implementations
    
    def test_register_multiple(self):
        """测试注册多个参考实现"""
        comp = ReferenceComparator()
        
        comp.register_reference("ref1", lambda: QuantumCircuit(1))
        comp.register_reference("ref2", lambda: QuantumCircuit(2))
        
        assert len(comp._reference_implementations) == 2


class TestCircuitComparison:
    """测试电路对比"""
    
    def test_identical_circuits(self):
        """测试相同电路"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        assert result.is_equivalent
        assert result.status == ComparisonStatus.MATCH
    
    def test_equivalent_circuits(self):
        """测试等价但形式不同的电路"""
        comp = ReferenceComparator()
        
        # H * Z * H = X (带全局相位)
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        qc1.z(0)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.x(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        assert result.is_equivalent
    
    def test_different_circuits(self):
        """测试不同电路"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.x(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        assert not result.is_equivalent
        assert result.status == ComparisonStatus.DIFFERENT
    
    def test_large_circuit_skipped(self):
        """测试大电路跳过等价性检查"""
        comp = ReferenceComparator(matrix_size_limit=5)
        
        qc1 = QuantumCircuit(6)  # 超过限制
        qc2 = QuantumCircuit(6)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        assert result.status == ComparisonStatus.SKIPPED
        assert len(result.warnings) > 0


class TestMetricComparison:
    """测试指标对比"""
    
    def test_qubit_count_comparison(self):
        """测试量子比特数对比"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(2)
        qc2 = QuantumCircuit(2)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        num_qubits_mc = None
        for mc in result.metric_comparisons:
            if mc.metric_name == "num_qubits":
                num_qubits_mc = mc
                break
        
        assert num_qubits_mc is not None
        assert num_qubits_mc.actual_value == 2
        assert num_qubits_mc.reference_value == 2
        assert num_qubits_mc.within_tolerance
    
    def test_gate_count_comparison(self):
        """测试门数量对比"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        qc1.x(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        gate_count_mc = None
        for mc in result.metric_comparisons:
            if mc.metric_name == "gate_count":
                gate_count_mc = mc
                break
        
        assert gate_count_mc is not None
        assert gate_count_mc.actual_value == 2
        assert gate_count_mc.reference_value == 1
        assert gate_count_mc.difference == 1
    
    def test_depth_comparison(self):
        """测试深度对比"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        qc1.x(0)
        qc1.z(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        qc2.x(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        
        depth_mc = None
        for mc in result.metric_comparisons:
            if mc.metric_name == "depth":
                depth_mc = mc
                break
        
        assert depth_mc is not None
        assert depth_mc.actual_value == 3
        assert depth_mc.reference_value == 2


class TestAnomalyDetection:
    """测试异常检测"""
    
    def test_gate_count_anomaly(self):
        """测试门数量异常检测"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        for _ in range(10):  # 很多门
            qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        anomalies = comp.detect_anomalies(result, gate_count_threshold=2.0)
        
        assert len(anomalies) > 0
        assert any("higher" in a for a in anomalies)
    
    def test_depth_anomaly(self):
        """测试深度异常检测"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        for _ in range(10):  # 深度 10
            qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        anomalies = comp.detect_anomalies(result, depth_threshold=2.0)
        
        assert len(anomalies) > 0
        assert any("deeper" in a for a in anomalies)
    
    def test_no_anomaly(self):
        """测试无异常情况"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        qc1.x(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        qc2.z(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        anomalies = comp.detect_anomalies(result)
        
        assert len(anomalies) == 0


class TestBuiltinReferences:
    """测试内置参考实现"""
    
    def test_create_builtin_references(self):
        """测试创建内置参考"""
        comp = ReferenceComparator()
        comp.create_builtin_references()
        
        assert "bell_state" in comp._reference_implementations
        assert "ghz_state" in comp._reference_implementations
        assert "qft" in comp._reference_implementations
    
    def test_bell_state_reference(self):
        """测试 Bell state 参考实现"""
        comp = ReferenceComparator()
        comp.create_builtin_references()
        
        # 创建用户电路
        user_circuit = QuantumCircuit(2, 2)
        user_circuit.h(0)
        user_circuit.cnot(0, 1)
        
        result = comp.compare_with_reference(user_circuit, "bell_state")
        
        assert result.is_equivalent
    
    def test_ghz_state_reference(self):
        """测试 GHZ state 参考实现"""
        comp = ReferenceComparator()
        comp.create_builtin_references()

        # 创建用户电路（与参考实现匹配的结构）
        user_circuit = QuantumCircuit(3)
        user_circuit.h(0)
        user_circuit.cnot(0, 1)
        user_circuit.cnot(1, 2)  # 参考实现使用这种连接方式

        result = comp.compare_with_reference(user_circuit, "ghz_state", {"n_qubits": 3})

        assert result.is_equivalent
    
    def test_unknown_reference(self):
        """测试未知参考实现"""
        comp = ReferenceComparator()
        
        qc = QuantumCircuit(1)
        result = comp.compare_with_reference(qc, "unknown_ref")
        
        assert result.status == ComparisonStatus.ERROR
        assert "Unknown reference" in result.error_message


class TestReportGeneration:
    """测试报告生成"""
    
    def test_generate_report_basic(self):
        """测试基本报告生成"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        report = comp.generate_report(result)
        
        assert "Reference Comparison Report" in report
        assert "test" in report
    
    def test_generate_report_verbose(self):
        """测试详细报告生成"""
        comp = ReferenceComparator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = comp.compare_circuits(qc1, qc2, "test")
        report = comp.generate_report(result, verbose=True)
        
        assert "Metric Comparison:" in report
        assert "num_qubits" in report
    
    def test_generate_report_with_error(self):
        """测试带错误的报告生成"""
        comp = ReferenceComparator()
        
        result = comp.compare_with_reference(QuantumCircuit(1), "unknown")
        report = comp.generate_report(result)
        
        assert "Error:" in report
        assert "Unknown reference" in report