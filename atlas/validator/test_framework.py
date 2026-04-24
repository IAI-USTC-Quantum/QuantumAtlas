"""
单元测试框架模块

提供量子电路的测试框架，包括：
- 测试用例定义和管理
- 测试执行器
- 输入/输出状态验证
- 概率分布验证
- 测试结果报告生成
"""

import numpy as np
from typing import List, Dict, Any, Optional, Callable, Union
from dataclasses import dataclass, field
from enum import Enum
import logging
from datetime import datetime

from atlas.designer.quantum_circuit import QuantumCircuit, Gate

logger = logging.getLogger(__name__)


class TestStatus(Enum):
    """测试状态枚举"""
    PENDING = "pending"
    RUNNING = "running"
    PASSED = "passed"
    FAILED = "failed"
    ERROR = "error"
    SKIPPED = "skipped"


@dataclass
class TestCase:
    """
    测试用例数据类
    
    Attributes:
        name: 测试用例名称
        description: 测试描述
        input_state: 输入状态向量（可选）
        expected_output: 期望输出状态向量（可选）
        expected_distribution: 期望概率分布（可选）
        input_basis_state: 输入计算基态（整数，可选）
        expected_basis_state: 期望输出计算基态（整数，可选）
        tolerance: 数值容差
        custom_validator: 自定义验证函数（可选）
    """
    name: str
    description: str = ""
    input_state: Optional[np.ndarray] = None
    expected_output: Optional[np.ndarray] = None
    expected_distribution: Optional[Dict[str, float]] = None
    input_basis_state: Optional[int] = None
    expected_basis_state: Optional[int] = None
    tolerance: float = 1e-10
    custom_validator: Optional[Callable[[np.ndarray], bool]] = None
    
    def __post_init__(self):
        """验证测试用例配置"""
        # 确保输入状态归一化
        if self.input_state is not None:
            norm = np.linalg.norm(self.input_state)
            if abs(norm - 1.0) > 1e-10:
                self.input_state = self.input_state / norm
        
        # 确保期望输出归一化
        if self.expected_output is not None:
            norm = np.linalg.norm(self.expected_output)
            if abs(norm - 1.0) > 1e-10:
                self.expected_output = self.expected_output / norm


@dataclass
class TestResult:
    """单个测试结果"""
    test_case: TestCase
    status: TestStatus
    actual_output: Optional[np.ndarray] = None
    actual_distribution: Optional[Dict[str, float]] = None
    error_message: str = ""
    execution_time_ms: float = 0.0
    max_difference: float = 0.0
    
    @property
    def passed(self) -> bool:
        """检查测试是否通过"""
        return self.status == TestStatus.PASSED
    
    @property
    def failed(self) -> bool:
        """检查测试是否失败"""
        return self.status in [TestStatus.FAILED, TestStatus.ERROR]


@dataclass
class TestSuiteResult:
    """测试套件执行结果"""
    suite_name: str
    results: List[TestResult] = field(default_factory=list)
    start_time: Optional[datetime] = None
    end_time: Optional[datetime] = None
    
    @property
    def total_tests(self) -> int:
        return len(self.results)
    
    @property
    def passed_tests(self) -> int:
        return sum(1 for r in self.results if r.status == TestStatus.PASSED)
    
    @property
    def failed_tests(self) -> int:
        return sum(1 for r in self.results if r.status in [TestStatus.FAILED, TestStatus.ERROR])
    
    @property
    def skipped_tests(self) -> int:
        return sum(1 for r in self.results if r.status == TestStatus.SKIPPED)
    
    @property
    def pass_rate(self) -> float:
        if self.total_tests == 0:
            return 0.0
        return self.passed_tests / self.total_tests
    
    @property
    def execution_time_ms(self) -> float:
        return sum(r.execution_time_ms for r in self.results)
    
    def get_failed_tests(self) -> List[TestResult]:
        """获取所有失败的测试"""
        return [r for r in self.results if r.failed]


class TestSuite:
    """
    测试套件类
    
    管理多个测试用例的集合
    """
    
    def __init__(self, name: str, description: str = ""):
        """
        初始化测试套件
        
        Args:
            name: 套件名称
            description: 套件描述
        """
        self.name = name
        self.description = description
        self.test_cases: List[TestCase] = []
    
    def add_test(self, test_case: TestCase) -> "TestSuite":
        """添加测试用例"""
        self.test_cases.append(test_case)
        return self
    
    def add_tests(self, test_cases: List[TestCase]) -> "TestSuite":
        """批量添加测试用例"""
        self.test_cases.extend(test_cases)
        return self
    
    def remove_test(self, test_name: str) -> bool:
        """移除指定名称的测试用例"""
        for i, tc in enumerate(self.test_cases):
            if tc.name == test_name:
                self.test_cases.pop(i)
                return True
        return False
    
    def get_test(self, test_name: str) -> Optional[TestCase]:
        """获取指定名称的测试用例"""
        for tc in self.test_cases:
            if tc.name == test_name:
                return tc
        return None
    
    def clear(self) -> None:
        """清空所有测试用例"""
        self.test_cases.clear()
    
    def __len__(self) -> int:
        return len(self.test_cases)
    
    def __iter__(self):
        return iter(self.test_cases)


class TestExecutor:
    """
    测试执行器类
    
    执行测试套件并生成测试报告
    """
    
    def __init__(self, matrix_size_limit: int = 10):
        """
        初始化测试执行器
        
        Args:
            matrix_size_limit: 矩阵大小限制（用于状态模拟）
        """
        self.matrix_size_limit = matrix_size_limit
        self._tolerance = 1e-10
    
    def execute(
        self, 
        circuit: QuantumCircuit, 
        test_suite: TestSuite
    ) -> TestSuiteResult:
        """
        执行测试套件
        
        Args:
            circuit: 待测试的量子电路
            test_suite: 测试套件
            
        Returns:
            TestSuiteResult 包含所有测试结果
        """
        suite_result = TestSuiteResult(suite_name=test_suite.name)
        suite_result.start_time = datetime.now()
        
        logger.info(f"Executing test suite '{test_suite.name}' with {len(test_suite)} tests")
        
        for test_case in test_suite:
            result = self._execute_single_test(circuit, test_case)
            suite_result.results.append(result)
        
        suite_result.end_time = datetime.now()
        
        logger.info(
            f"Test suite '{test_suite.name}' completed: "
            f"{suite_result.passed_tests}/{suite_result.total_tests} passed"
        )
        
        return suite_result
    
    def _execute_single_test(
        self, 
        circuit: QuantumCircuit, 
        test_case: TestCase
    ) -> TestResult:
        """执行单个测试用例"""
        import time
        start_time = time.time()
        
        try:
            # 确定输入状态
            input_state = self._get_input_state(circuit.num_qubits, test_case)
            
            # 模拟电路执行
            output_state = self._simulate_circuit(circuit, input_state)
            
            # 执行验证
            if test_case.custom_validator:
                # 使用自定义验证器
                is_valid = test_case.custom_validator(output_state)
                if is_valid:
                    status = TestStatus.PASSED
                    error_message = ""
                else:
                    status = TestStatus.FAILED
                    error_message = "Custom validator returned False"
                max_diff = 0.0
            elif test_case.expected_output is not None:
                # 验证输出状态
                is_valid, max_diff, error_message = self._validate_output_state(
                    output_state, test_case.expected_output, test_case.tolerance
                )
                status = TestStatus.PASSED if is_valid else TestStatus.FAILED
            elif test_case.expected_distribution is not None:
                # 验证概率分布
                actual_dist = self._compute_distribution(output_state)
                is_valid, max_diff, error_message = self._validate_distribution(
                    actual_dist, test_case.expected_distribution, test_case.tolerance
                )
                status = TestStatus.PASSED if is_valid else TestStatus.FAILED
            elif test_case.expected_basis_state is not None:
                # 验证输出是否为特定计算基态
                is_valid, max_diff, error_message = self._validate_basis_state(
                    output_state, test_case.expected_basis_state, test_case.tolerance
                )
                status = TestStatus.PASSED if is_valid else TestStatus.FAILED
            else:
                status = TestStatus.SKIPPED
                error_message = "No validation criteria specified"
                max_diff = 0.0
            
            execution_time = (time.time() - start_time) * 1000  # 转换为毫秒
            
            return TestResult(
                test_case=test_case,
                status=status,
                actual_output=output_state,
                actual_distribution=self._compute_distribution(output_state) if status != TestStatus.SKIPPED else None,
                error_message=error_message,
                execution_time_ms=execution_time,
                max_difference=max_diff
            )
            
        except Exception as e:
            execution_time = (time.time() - start_time) * 1000
            logger.error(f"Test '{test_case.name}' encountered error: {e}")
            return TestResult(
                test_case=test_case,
                status=TestStatus.ERROR,
                error_message=str(e),
                execution_time_ms=execution_time,
                max_difference=0.0
            )
    
    def _get_input_state(
        self, 
        num_qubits: int, 
        test_case: TestCase
    ) -> np.ndarray:
        """获取输入状态向量"""
        if test_case.input_state is not None:
            return test_case.input_state
        elif test_case.input_basis_state is not None:
            dim = 2 ** num_qubits
            state = np.zeros(dim, dtype=complex)
            state[test_case.input_basis_state] = 1.0
            return state
        else:
            # 默认使用 |0...0⟩ 态
            dim = 2 ** num_qubits
            state = np.zeros(dim, dtype=complex)
            state[0] = 1.0
            return state
    
    def _simulate_circuit(
        self, 
        circuit: QuantumCircuit, 
        input_state: np.ndarray
    ) -> np.ndarray:
        """模拟电路执行（简化版本）"""
        from atlas.validator.equivalence_checker import EquivalenceChecker
        
        checker = EquivalenceChecker(matrix_size_limit=self.matrix_size_limit)
        
        try:
            # 尝试使用矩阵模拟
            matrix = checker.circuit_to_matrix(circuit)
            return matrix @ input_state
        except ValueError:
            # 包含测量，使用逐门模拟
            state = input_state.copy()
            for gate in circuit.gates:
                if gate.name == "MEASURE" or gate.name == "BARRIER":
                    continue
                gate_matrix = checker._get_gate_matrix(gate, circuit.num_qubits)
                state = gate_matrix @ state
            return state
    
    def _validate_output_state(
        self,
        actual: np.ndarray,
        expected: np.ndarray,
        tolerance: float
    ) -> tuple[bool, float, str]:
        """验证输出状态"""
        if actual.shape != expected.shape:
            return False, 0.0, f"Shape mismatch: {actual.shape} vs {expected.shape}"
        
        # 考虑全局相位
        max_idx = np.argmax(np.abs(actual))
        if abs(actual[max_idx]) < tolerance:
            return False, 0.0, "Actual state is zero"
        
        phase_diff = np.angle(actual[max_idx]) - np.angle(expected[max_idx])
        expected_corrected = expected * np.exp(1j * phase_diff)
        
        difference = np.abs(actual - expected_corrected)
        max_diff = np.max(difference)
        
        if max_diff < tolerance:
            return True, max_diff, ""
        else:
            return False, max_diff, f"State difference {max_diff:.2e} exceeds tolerance {tolerance:.2e}"
    
    def _validate_distribution(
        self,
        actual: Dict[str, float],
        expected: Dict[str, float],
        tolerance: float
    ) -> tuple[bool, float, str]:
        """验证概率分布"""
        max_diff = 0.0
        all_keys = set(actual.keys()) | set(expected.keys())
        
        for key in all_keys:
            a = actual.get(key, 0.0)
            e = expected.get(key, 0.0)
            diff = abs(a - e)
            if diff > max_diff:
                max_diff = diff
        
        if max_diff < tolerance:
            return True, max_diff, ""
        else:
            return False, max_diff, f"Distribution difference {max_diff:.2e} exceeds tolerance {tolerance:.2e}"
    
    def _validate_basis_state(
        self,
        state: np.ndarray,
        expected_basis: int,
        tolerance: float
    ) -> tuple[bool, float, str]:
        """验证输出是否为特定计算基态"""
        # 检查概率是否集中在期望的基态上
        probabilities = np.abs(state) ** 2
        expected_prob = probabilities[expected_basis]
        
        if expected_prob < 1.0 - tolerance:
            other_probs = np.sum(probabilities) - expected_prob
            return False, 1.0 - expected_prob, f"Expected basis state probability {expected_prob:.4f}, other states: {other_probs:.4f}"
        
        return True, 1.0 - expected_prob, ""
    
    def _compute_distribution(self, state: np.ndarray) -> Dict[str, float]:
        """计算概率分布"""
        probabilities = np.abs(state) ** 2
        distribution = {}
        n_qubits = int(np.log2(len(state)))
        
        for i, prob in enumerate(probabilities):
            if prob > 1e-15:  # 只保留显著的概率
                bitstring = format(i, f'0{n_qubits}b')
                distribution[bitstring] = float(prob)
        
        return distribution
    
    def generate_report(self, result: TestSuiteResult, verbose: bool = False) -> str:
        """
        生成测试报告
        
        Args:
            result: 测试套件结果
            verbose: 是否包含详细信息
            
        Returns:
            格式化报告字符串
        """
        lines = []
        lines.append("=" * 60)
        lines.append(f"Test Suite: {result.suite_name}")
        lines.append("=" * 60)
        lines.append(f"Total Tests: {result.total_tests}")
        lines.append(f"Passed: {result.passed_tests}")
        lines.append(f"Failed: {result.failed_tests}")
        lines.append(f"Skipped: {result.skipped_tests}")
        lines.append(f"Pass Rate: {result.pass_rate * 100:.1f}%")
        lines.append(f"Execution Time: {result.execution_time_ms:.2f} ms")
        lines.append("")
        
        if verbose:
            lines.append("-" * 60)
            lines.append("Detailed Results:")
            lines.append("-" * 60)
            
            for test_result in result.results:
                status_icon = "✓" if test_result.passed else "✗" if test_result.failed else "○"
                lines.append(f"\n{status_icon} {test_result.test_case.name} - {test_result.status.value}")
                lines.append(f"  Description: {test_result.test_case.description}")
                lines.append(f"  Time: {test_result.execution_time_ms:.2f} ms")
                
                if test_result.error_message:
                    lines.append(f"  Error: {test_result.error_message}")
                
                if test_result.max_difference > 0:
                    lines.append(f"  Max Difference: {test_result.max_difference:.2e}")
                
                if test_result.actual_distribution and verbose:
                    lines.append(f"  Actual Distribution: {test_result.actual_distribution}")
        
        # 列出失败的测试
        failed_tests = result.get_failed_tests()
        if failed_tests:
            lines.append("")
            lines.append("-" * 60)
            lines.append("Failed Tests:")
            lines.append("-" * 60)
            for ft in failed_tests:
                lines.append(f"  ✗ {ft.test_case.name}")
                lines.append(f"    {ft.error_message}")
                if ft.max_difference > 0:
                    lines.append(f"    Max Difference: {ft.max_difference:.2e}")
        
        lines.append("")
        lines.append("=" * 60)
        
        return "\n".join(lines)