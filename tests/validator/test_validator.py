"""
测试 Validator 主类

包含15+测试用例：
- 完整验证流程
- 报告生成
- 各种验证模式
"""

import pytest
import numpy as np
import json
import tempfile
import os

from qatlas.designer.quantum_circuit import QuantumCircuit
from qatlas.designer.quantum_ir import QuantumIR
from qatlas.validator.__main__ import load_circuit
from qatlas.validator.validator import Validator, ValidationReport
from qatlas.validator.test_framework import TestCase, TestSuite


class TestValidatorInitialization:
    """测试 Validator 初始化"""
    
    def test_basic_initialization(self):
        """测试基本初始化"""
        validator = Validator(matrix_size_limit=8, tolerance=1e-8)
        
        assert validator.matrix_size_limit == 8
        assert validator.tolerance == 1e-8
        assert validator._equivalence_checker is not None
        assert validator._test_executor is not None
        assert validator._reference_comparator is not None
    
    def test_default_initialization(self):
        """测试默认初始化"""
        validator = Validator()
        
        assert validator.matrix_size_limit == 10
        assert validator.tolerance == 1e-10


class TestValidatorCliLoadCircuit:
    """测试 validator CLI 的 JSON 输入兼容性"""

    def test_loads_raw_quantum_circuit_json(self, tmp_path):
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        path = tmp_path / "circuit.json"
        path.write_text(json.dumps(circuit.to_dict()), encoding="utf-8")

        loaded = load_circuit(str(path))

        assert loaded.num_qubits == 2
        assert loaded.gate_count == 2

    def test_loads_quantum_ir_json_wrapper(self, tmp_path):
        circuit = QuantumCircuit(2)
        circuit.h(0)
        circuit.cnot(0, 1)
        quantum_ir = QuantumIR(circuit=circuit, algorithm_id="bell_state")
        path = tmp_path / "bell_state_ir.json"
        path.write_text(quantum_ir.to_json(), encoding="utf-8")

        loaded = load_circuit(str(path))

        assert loaded.num_qubits == 2
        assert loaded.gate_count == 2


class TestValidateEquivalence:
    """测试快速等价性验证"""
    
    def test_equivalent_circuits(self):
        """测试等价电路"""
        validator = Validator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.h(0)
        
        result = validator.validate_equivalence(qc1, qc2)
        
        assert result.is_equivalent
    
    def test_inequivalent_circuits(self):
        """测试不等价电路"""
        validator = Validator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.x(0)
        
        result = validator.validate_equivalence(qc1, qc2)
        
        assert not result.is_equivalent
    
    def test_bell_state_equivalence(self):
        """测试 Bell state 等价性 - 主要验收测试"""
        validator = Validator()
        
        # 标准 Bell state
        qc1 = QuantumCircuit(2, 2)
        qc1.h(0)
        qc1.cnot(0, 1)
        
        # 相同电路
        qc2 = QuantumCircuit(2, 2)
        qc2.h(0)
        qc2.cnot(0, 1)
        
        result = validator.validate_equivalence(qc1, qc2)
        
        assert result.is_equivalent
        assert result.phase_difference is None or abs(result.phase_difference) < 1e-10


class TestValidateWithTests:
    """测试带测试套件的验证"""
    
    def test_single_passing_test(self):
        """测试单个通过测试"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        suite = TestSuite(name="H Test")
        suite.add_test(TestCase(
            name="h_gate",
            input_state=np.array([1, 0], dtype=complex),
            expected_output=np.array([1, 1], dtype=complex) / np.sqrt(2)
        ))
        
        result = validator.validate_with_tests(circuit, suite)
        
        assert result.passed_tests == 1
        assert result.failed_tests == 0
    
    def test_bell_state_tests(self):
        """测试 Bell state 测试套件 - 主要验收测试"""
        validator = Validator()
        
        # Bell state 电路
        circuit = QuantumCircuit(2, 2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        suite = TestSuite(name="Bell State Tests")
        suite.add_test(TestCase(
            name="bell_from_00",
            description="Bell state from |00>",
            input_basis_state=0,
            expected_distribution={"00": 0.5, "11": 0.5}
        ))
        suite.add_test(TestCase(
            name="bell_normalized",
            description="Output should be normalized",
            custom_validator=lambda s: abs(np.linalg.norm(s) - 1.0) < 1e-10
        ))
        
        result = validator.validate_with_tests(circuit, suite)
        
        assert result.passed_tests == 2
        assert result.pass_rate == 1.0


class TestCompareWithReference:
    """测试与参考实现对比"""
    
    def test_compare_with_bell_state(self):
        """测试与 Bell state 参考对比"""
        validator = Validator()
        
        user_circuit = QuantumCircuit(2, 2)
        user_circuit.h(0)
        user_circuit.cnot(0, 1)
        
        result = validator.compare_with_reference(user_circuit, "bell_state")
        
        assert result.is_equivalent
    
    def test_compare_with_ghz_state(self):
        """测试与 GHZ state 参考对比"""
        validator = Validator()

        user_circuit = QuantumCircuit(3)
        user_circuit.h(0)
        user_circuit.cnot(0, 1)
        user_circuit.cnot(1, 2)  # 与参考实现匹配

        result = validator.compare_with_reference(user_circuit, "ghz_state")

        assert result.is_equivalent
    
    def test_register_custom_reference(self):
        """测试注册自定义参考实现"""
        validator = Validator()
        
        def custom_ref():
            qc = QuantumCircuit(1)
            qc.h(0)
            return qc
        
        validator.register_reference("custom_h", custom_ref)
        
        user_circuit = QuantumCircuit(1)
        user_circuit.h(0)
        
        result = validator.compare_with_reference(user_circuit, "custom_h")
        
        assert result.is_equivalent


class TestFullValidation:
    """测试完整验证流程"""
    
    def test_full_validation_with_all_checks(self):
        """测试包含所有检查的完整验证"""
        validator = Validator()
        
        # 电路
        circuit = QuantumCircuit(2, 2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        # 参考电路
        ref_circuit = QuantumCircuit(2, 2)
        ref_circuit.h(0)
        ref_circuit.cnot(0, 1)
        
        # 测试套件
        suite = TestSuite(name="Tests")
        suite.add_test(TestCase(
            name="test1",
            input_basis_state=0,
            expected_distribution={"00": 0.5, "11": 0.5}
        ))
        
        report = validator.validate(
            circuit=circuit,
            reference_circuit=ref_circuit,
            test_suite=suite,
            reference_names=["bell_state"]
        )
        
        assert report.passed
        assert report.equivalence_result is not None
        assert report.test_results is not None
        assert len(report.reference_results) == 1
    
    def test_full_validation_skip_equivalence(self):
        """测试跳过等价性检查"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        report = validator.validate(
            circuit=circuit,
            skip_equivalence=True
        )
        
        assert report.passed
        assert report.equivalence_result is None
    
    def test_full_validation_skip_tests(self):
        """测试跳过测试"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        suite = TestSuite(name="Tests")
        suite.add_test(TestCase(name="test1"))
        
        report = validator.validate(
            circuit=circuit,
            test_suite=suite,
            skip_tests=True
        )
        
        assert report.test_results is None
    
    def test_failed_equivalence_check(self):
        """测试失败的等价性检查"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        ref_circuit = QuantumCircuit(1)
        ref_circuit.x(0)
        
        report = validator.validate(
            circuit=circuit,
            reference_circuit=ref_circuit
        )
        
        assert not report.passed
        assert len(report.errors) > 0
    
    def test_failed_test(self):
        """测试失败的测试"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.x(0)
        
        suite = TestSuite(name="Failing")
        suite.add_test(TestCase(
            name="fail",
            input_state=np.array([1, 0], dtype=complex),
            expected_output=np.array([1, 0], dtype=complex)  # 错误期望
        ))
        
        report = validator.validate(
            circuit=circuit,
            test_suite=suite
        )
        
        assert not report.passed
        assert report.test_results.failed_tests == 1


class TestReportGeneration:
    """测试报告生成"""
    
    def test_generate_text_report(self):
        """测试文本报告生成"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        report = validator.validate(circuit=circuit)
        text_report = validator.generate_report(report, format="text")
        
        assert "VALIDATION REPORT" in text_report
        assert circuit.name in text_report
    
    def test_generate_json_report(self):
        """测试 JSON 报告生成"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        report = validator.validate(circuit=circuit)
        json_report = validator.generate_report(report, format="json")
        
        data = json.loads(json_report)
        assert "circuit_name" in data
        assert "passed" in data
    
    def test_generate_markdown_report(self):
        """测试 Markdown 报告生成"""
        validator = Validator()

        # 创建一个带测试套件的电路，确保报告有更多内容
        circuit = QuantumCircuit(1)
        circuit.h(0)

        suite = TestSuite(name="H Test")
        suite.add_test(TestCase(
            name="h_gate",
            input_state=np.array([1, 0], dtype=complex),
            expected_output=np.array([1, 1], dtype=complex) / np.sqrt(2)
        ))

        report = validator.validate(circuit=circuit, test_suite=suite)
        md_report = validator.generate_report(report, format="markdown")

        assert "# Validation Report" in md_report
    
    def test_save_report_to_file(self):
        """测试保存报告到文件"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        report = validator.validate(circuit=circuit)
        
        with tempfile.NamedTemporaryFile(mode='w', delete=False, suffix='.txt') as f:
            temp_path = f.name
        
        try:
            validator.save_report(report, temp_path, format="text")
            
            with open(temp_path, 'r') as f:
                content = f.read()
            
            assert "VALIDATION REPORT" in content
        finally:
            os.unlink(temp_path)


class TestValidationReport:
    """测试 ValidationReport 数据类"""
    
    def test_report_summary(self):
        """测试报告摘要"""
        report = ValidationReport(circuit_name="Test")
        
        summary = report.summary
        
        assert summary["circuit_name"] == "Test"
        assert "validation_time" in summary
        assert "passed" in summary
    
    def test_report_with_results(self):
        """测试带结果的报告"""
        from qatlas.validator.equivalence_checker import EquivalenceResult
        
        report = ValidationReport(circuit_name="Test")
        report.equivalence_result = EquivalenceResult(is_equivalent=True)
        report.test_results = None
        report.reference_results = []
        
        summary = report.summary
        
        assert summary["has_equivalence_check"] is True
        assert summary["has_test_results"] is False


class TestGlobalPhaseHandling:
    """测试全局相位处理 - 主要验收测试"""
    
    def test_global_phase_in_equivalence(self):
        """测试等价性检查中的全局相位处理"""
        validator = Validator()
        
        # H * Z * H = X (带全局相位)
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        qc1.z(0)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.x(0)
        
        result = validator.validate_equivalence(qc1, qc2)
        
        assert result.is_equivalent
        assert result.phase_difference is not None
    
    def test_no_warning_for_equivalent_with_phase(self):
        """测试带相位的等效电路不产生错误警告"""
        validator = Validator()
        
        qc1 = QuantumCircuit(1)
        qc1.h(0)
        qc1.z(0)
        qc1.h(0)
        
        qc2 = QuantumCircuit(1)
        qc2.x(0)
        
        report = validator.validate(
            circuit=qc1,
            reference_circuit=qc2
        )
        
        # 应该通过（带相位警告）
        assert report.passed
        # 应该有相位警告
        assert len(report.warnings) > 0 or report.equivalence_result.phase_difference is not None


class TestEdgeCases:
    """测试边界情况"""
    
    def test_empty_circuit(self):
        """测试空电路"""
        validator = Validator()
        
        circuit = QuantumCircuit(1)
        
        report = validator.validate(circuit=circuit)
        
        assert report.passed
    
    def test_circuit_with_only_barriers(self):
        """测试只有 barrier 的电路"""
        validator = Validator()
        
        circuit = QuantumCircuit(2)
        circuit.barrier()
        
        report = validator.validate(circuit=circuit)
        
        assert report.passed
    
    def test_large_circuit_validation(self):
        """测试大电路验证（跳过矩阵检查）"""
        validator = Validator(matrix_size_limit=5)
        
        circuit = QuantumCircuit(6)  # 超过限制
        circuit.h(0)
        
        ref_circuit = QuantumCircuit(6)
        ref_circuit.h(0)
        
        report = validator.validate(
            circuit=circuit,
            reference_circuit=ref_circuit
        )
        
        # 大电路会跳过等价性检查
        assert report.equivalence_result is not None


class TestIntegration:
    """集成测试"""
    
    def test_complex_bell_state_validation(self):
        """测试复杂 Bell state 验证 - 最终验收测试"""
        validator = Validator()
        
        # 用户实现的 Bell state
        user_circuit = QuantumCircuit(2, 2, name="User Bell")
        user_circuit.h(0)
        user_circuit.cnot(0, 1)
        
        # 创建全面的测试套件
        suite = TestSuite(name="Bell State Comprehensive")
        
        # 测试 1: 从 |00> 产生正确的叠加态
        suite.add_test(TestCase(
            name="superposition_from_00",
            description="|00> should produce (|00> + |11>)/√2",
            input_basis_state=0,
            expected_distribution={"00": 0.5, "11": 0.5}
        ))
        
        # 测试 2: 归一化
        suite.add_test(TestCase(
            name="normalization",
            description="Output should be normalized",
            input_basis_state=0,
            custom_validator=lambda s: abs(np.linalg.norm(s) - 1.0) < 1e-10
        ))
        
        # 测试 3: 概率守恒
        suite.add_test(TestCase(
            name="probability_conservation",
            description="Total probability should be 1",
            input_basis_state=0,
            custom_validator=lambda s: abs(np.sum(np.abs(s)**2) - 1.0) < 1e-10
        ))
        
        # 执行完整验证
        report = validator.validate(
            circuit=user_circuit,
            test_suite=suite,
            reference_names=["bell_state"]
        )
        
        # 验证所有检查通过
        assert report.passed, f"Validation failed with errors: {report.errors}"
        assert report.test_results.passed_tests == 3
        assert len(report.reference_results) == 1
        assert all(r.is_equivalent for r in report.reference_results)
