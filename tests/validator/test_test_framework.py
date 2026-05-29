"""
测试测试框架模块

包含15+测试用例：
- 测试用例创建和管理
- 测试执行器功能
- 状态向量验证
- 概率分布验证
"""

import pytest
import numpy as np

from qatlas.designer.quantum_circuit import QuantumCircuit
from qatlas.validator.test_framework import (
    TestCase, TestSuite, TestExecutor, TestSuiteResult,
    TestStatus, TestResult
)


class TestTestCase:
    """测试 TestCase 数据类"""
    
    def test_basic_creation(self):
        """测试基本创建"""
        tc = TestCase(name="test_1", description="A test")
        
        assert tc.name == "test_1"
        assert tc.description == "A test"
        assert tc.tolerance == 1e-10  # 默认值
    
    def test_with_input_state(self):
        """测试带输入状态的用例"""
        input_state = np.array([1, 0], dtype=complex)
        expected = np.array([1/np.sqrt(2), 1/np.sqrt(2)], dtype=complex)
        
        tc = TestCase(
            name="h_test",
            description="Test H gate",
            input_state=input_state,
            expected_output=expected
        )
        
        assert np.allclose(tc.input_state, input_state)
        assert np.allclose(tc.expected_output, expected)
    
    def test_state_normalization(self):
        """测试状态自动归一化"""
        # 未归一化的输入
        input_state = np.array([2, 0], dtype=complex)  # 未归一化
        
        tc = TestCase(
            name="norm_test",
            input_state=input_state
        )
        
        # 应该被归一化
        assert abs(np.linalg.norm(tc.input_state) - 1.0) < 1e-10
    
    def test_with_basis_state(self):
        """测试计算基态"""
        tc = TestCase(
            name="basis_test",
            input_basis_state=2,  # |10>
            expected_basis_state=3  # 期望 |11>
        )
        
        assert tc.input_basis_state == 2
        assert tc.expected_basis_state == 3
    
    def test_with_distribution(self):
        """测试概率分布验证"""
        dist = {"00": 0.5, "11": 0.5}
        
        tc = TestCase(
            name="dist_test",
            expected_distribution=dist
        )
        
        assert tc.expected_distribution == dist
    
    def test_custom_validator(self):
        """测试自定义验证器"""
        def validator(state):
            return np.allclose(np.linalg.norm(state), 1.0)
        
        tc = TestCase(
            name="custom_test",
            custom_validator=validator
        )
        
        assert tc.custom_validator is not None


class TestTestSuite:
    """测试 TestSuite 类"""
    
    def test_creation(self):
        """测试创建"""
        suite = TestSuite(name="My Suite", description="Test suite")
        
        assert suite.name == "My Suite"
        assert suite.description == "Test suite"
        assert len(suite) == 0
    
    def test_add_test(self):
        """测试添加测试用例"""
        suite = TestSuite(name="Suite")
        
        tc1 = TestCase(name="test_1")
        tc2 = TestCase(name="test_2")
        
        suite.add_test(tc1)
        suite.add_test(tc2)
        
        assert len(suite) == 2
    
    def test_add_tests_batch(self):
        """测试批量添加"""
        suite = TestSuite(name="Suite")
        
        tests = [
            TestCase(name="test_1"),
            TestCase(name="test_2"),
            TestCase(name="test_3"),
        ]
        
        suite.add_tests(tests)
        
        assert len(suite) == 3
    
    def test_remove_test(self):
        """测试移除测试"""
        suite = TestSuite(name="Suite")
        
        tc = TestCase(name="test_1")
        suite.add_test(tc)
        
        result = suite.remove_test("test_1")
        
        assert result is True
        assert len(suite) == 0
    
    def test_remove_nonexistent(self):
        """测试移除不存在的测试"""
        suite = TestSuite(name="Suite")
        
        result = suite.remove_test("nonexistent")
        
        assert result is False
    
    def test_get_test(self):
        """测试获取测试"""
        suite = TestSuite(name="Suite")
        
        tc = TestCase(name="test_1", description="Found me")
        suite.add_test(tc)
        
        found = suite.get_test("test_1")
        
        assert found is not None
        assert found.description == "Found me"
    
    def test_get_nonexistent(self):
        """测试获取不存在的测试"""
        suite = TestSuite(name="Suite")
        
        found = suite.get_test("nonexistent")
        
        assert found is None
    
    def test_clear(self):
        """测试清空"""
        suite = TestSuite(name="Suite")
        suite.add_test(TestCase(name="test_1"))
        suite.add_test(TestCase(name="test_2"))
        
        suite.clear()
        
        assert len(suite) == 0
    
    def test_iteration(self):
        """测试迭代"""
        suite = TestSuite(name="Suite")
        suite.add_test(TestCase(name="test_1"))
        suite.add_test(TestCase(name="test_2"))
        
        names = [tc.name for tc in suite]
        
        assert names == ["test_1", "test_2"]


class TestTestExecutor:
    """测试 TestExecutor 类"""
    
    def test_initialization(self):
        """测试初始化"""
        executor = TestExecutor(matrix_size_limit=8)
        
        assert executor.matrix_size_limit == 8
    
    def test_execute_empty_suite(self):
        """测试执行空测试套件"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        suite = TestSuite(name="Empty")
        
        result = executor.execute(circuit, suite)
        
        assert result.total_tests == 0
        assert result.passed_tests == 0
    
    def test_execute_single_passing_test(self):
        """测试执行单个通过测试"""
        executor = TestExecutor()
        
        # H 门将 |0> 变为 |+>
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        expected = np.array([1, 1], dtype=complex) / np.sqrt(2)
        
        suite = TestSuite(name="H Test")
        suite.add_test(TestCase(
            name="h_gate",
            input_state=np.array([1, 0], dtype=complex),
            expected_output=expected
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.total_tests == 1
        assert result.passed_tests == 1
        assert result.failed_tests == 0
    
    def test_execute_single_failing_test(self):
        """测试执行单个失败测试"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        circuit.x(0)  # X 门
        
        # 错误期望：应该得到 |1> 但期望 |0>
        expected = np.array([1, 0], dtype=complex)
        
        suite = TestSuite(name="X Test")
        suite.add_test(TestCase(
            name="x_gate_fail",
            input_state=np.array([1, 0], dtype=complex),
            expected_output=expected
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.total_tests == 1
        assert result.passed_tests == 0
        assert result.failed_tests == 1
    
    def test_execute_basis_state_test(self):
        """测试计算基态验证"""
        executor = TestExecutor()
        
        # X 门将 |0> 变为 |1>
        circuit = QuantumCircuit(1)
        circuit.x(0)
        
        suite = TestSuite(name="Basis Test")
        suite.add_test(TestCase(
            name="x_basis",
            input_basis_state=0,  # |0>
            expected_basis_state=1  # 期望 |1>
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.passed_tests == 1
    
    def test_execute_distribution_test(self):
        """测试概率分布验证"""
        executor = TestExecutor()
        
        # H 门产生均匀分布
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        suite = TestSuite(name="Dist Test")
        suite.add_test(TestCase(
            name="h_distribution",
            input_state=np.array([1, 0], dtype=complex),
            expected_distribution={"0": 0.5, "1": 0.5}
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.passed_tests == 1
    
    def test_execute_custom_validator(self):
        """测试自定义验证器"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        def check_normalized(state):
            return abs(np.linalg.norm(state) - 1.0) < 1e-10
        
        suite = TestSuite(name="Custom Test")
        suite.add_test(TestCase(
            name="normalized",
            input_state=np.array([1, 0], dtype=complex),
            custom_validator=check_normalized
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.passed_tests == 1
    
    def test_execute_multiple_tests(self):
        """测试执行多个测试"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        circuit.h(0)
        
        suite = TestSuite(name="Multi Test")
        suite.add_test(TestCase(
            name="test_1",
            input_state=np.array([1, 0], dtype=complex),
            expected_basis_state=None  # 会被跳过（无验证标准）
        ))
        suite.add_test(TestCase(
            name="test_2",
            input_basis_state=0,
            expected_basis_state=None  # 会被跳过
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.total_tests == 2
        assert result.skipped_tests == 2
    
    def test_execute_bell_state_test(self):
        """测试 Bell state 验证 - 主要验收测试"""
        executor = TestExecutor()
        
        # Bell state 电路
        circuit = QuantumCircuit(2, 2)
        circuit.h(0)
        circuit.cnot(0, 1)
        
        suite = TestSuite(name="Bell State")
        suite.add_test(TestCase(
            name="bell_from_00",
            description="Bell state from |00>",
            input_basis_state=0,  # |00>
            expected_distribution={"00": 0.5, "11": 0.5}
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.passed_tests == 1
        assert result.pass_rate == 1.0
    
    def test_execute_with_default_input(self):
        """测试默认输入（|0...0>）"""
        executor = TestExecutor()
        
        # 恒等电路应保持 |0>
        circuit = QuantumCircuit(1)
        
        suite = TestSuite(name="Default Input")
        suite.add_test(TestCase(
            name="identity",
            expected_basis_state=0  # 默认输入是 |0>
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.passed_tests == 1


class TestTestSuiteResult:
    """测试 TestSuiteResult 类"""
    
    def test_empty_result(self):
        """测试空结果"""
        result = TestSuiteResult(suite_name="Test")
        
        assert result.total_tests == 0
        assert result.passed_tests == 0
        assert result.failed_tests == 0
        assert result.pass_rate == 0.0
    
    def test_result_stats(self):
        """测试结果统计"""
        result = TestSuiteResult(suite_name="Test")
        
        # 添加一些测试结果
        tc1 = TestCase(name="pass")
        tc2 = TestCase(name="fail")
        tc3 = TestCase(name="skip")
        
        result.results.append(TestResult(tc1, TestStatus.PASSED))
        result.results.append(TestResult(tc2, TestStatus.FAILED))
        result.results.append(TestResult(tc3, TestStatus.SKIPPED))
        
        assert result.total_tests == 3
        assert result.passed_tests == 1
        assert result.failed_tests == 1
        assert result.skipped_tests == 1
        assert result.pass_rate == 1/3
    
    def test_get_failed_tests(self):
        """测试获取失败测试"""
        result = TestSuiteResult(suite_name="Test")
        
        tc1 = TestCase(name="pass")
        tc2 = TestCase(name="fail")
        tc3 = TestCase(name="error")
        
        result.results.append(TestResult(tc1, TestStatus.PASSED))
        result.results.append(TestResult(tc2, TestStatus.FAILED))
        result.results.append(TestResult(tc3, TestStatus.ERROR))
        
        failed = result.get_failed_tests()
        
        assert len(failed) == 2
        assert all(r.failed for r in failed)


class TestReportGeneration:
    """测试报告生成功能"""
    
    def test_generate_report_basic(self):
        """测试基本报告生成"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        suite = TestSuite(name="Test Suite")
        suite.add_test(TestCase(name="test_1"))
        
        result = executor.execute(circuit, suite)
        report = executor.generate_report(result)
        
        assert "Test Suite" in report
        assert "Total Tests: 1" in report
    
    def test_generate_report_verbose(self):
        """测试详细报告生成"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        suite = TestSuite(name="Test Suite")
        suite.add_test(TestCase(name="test_1", description="A test"))
        
        result = executor.execute(circuit, suite)
        report = executor.generate_report(result, verbose=True)
        
        assert "Detailed Results:" in report
        assert "test_1" in report
    
    def test_generate_report_with_failures(self):
        """测试带失败项的报告"""
        executor = TestExecutor()
        
        circuit = QuantumCircuit(1)
        circuit.x(0)
        
        suite = TestSuite(name="Failing Suite")
        suite.add_test(TestCase(
            name="fail_test",
            input_state=np.array([1, 0], dtype=complex),
            expected_output=np.array([1, 0], dtype=complex)  # 错误期望
        ))
        
        result = executor.execute(circuit, suite)
        report = executor.generate_report(result)
        
        assert "Failed Tests:" in report
        assert "fail_test" in report


class TestEdgeCases:
    """测试边界情况"""
    
    def test_invalid_circuit_error(self):
        """测试无效电路错误处理"""
        executor = TestExecutor()
        
        # 创建一个会导致错误的测试
        circuit = QuantumCircuit(1)
        
        def bad_validator(state):
            raise ValueError("Intentional error")
        
        suite = TestSuite(name="Error Test")
        suite.add_test(TestCase(
            name="error_test",
            custom_validator=bad_validator
        ))
        
        result = executor.execute(circuit, suite)
        
        assert result.failed_tests == 1
        failed = result.get_failed_tests()[0]
        assert failed.status == TestStatus.ERROR
    
    def test_tolerance_setting(self):
        """测试容差设置"""
        executor = TestExecutor()

        # 恒等电路 - 保持 |0>
        circuit = QuantumCircuit(1)

        # 使用严格容差
        suite = TestSuite(name="Tolerance Test")
        suite.add_test(TestCase(
            name="strict",
            input_state=np.array([1, 0], dtype=complex),
            expected_basis_state=0,
            tolerance=1e-15  # 非常严格
        ))

        result = executor.execute(circuit, suite)

        # 恒等电路应该通过严格容差测试
        assert result.passed_tests == 1