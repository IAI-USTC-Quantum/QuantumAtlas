"""
Validator 主模块

提供量子电路验证的主接口，整合：
- 等价性检查
- 测试框架执行
- 参考实现对比
- 验证报告生成
"""

import numpy as np
from typing import Optional, Dict, Any, List, Union
from dataclasses import dataclass, field
from datetime import datetime
import logging
import json

from atlas.designer.quantum_circuit import QuantumCircuit
from atlas.validator.equivalence_checker import EquivalenceChecker, EquivalenceResult
from atlas.validator.test_framework import (
    TestExecutor, TestSuite, TestSuiteResult, TestCase
)
from atlas.validator.reference_comparison import (
    ReferenceComparator, ReferenceComparisonResult
)

logger = logging.getLogger(__name__)


@dataclass
class ValidationReport:
    """
    验证报告数据类
    
    包含完整的验证结果和元数据
    """
    circuit_name: str
    validation_time: datetime = field(default_factory=datetime.now)
    
    # 各项验证结果
    equivalence_result: Optional[EquivalenceResult] = None
    test_results: Optional[TestSuiteResult] = None
    reference_results: List[ReferenceComparisonResult] = field(default_factory=list)
    
    # 汇总信息
    passed: bool = False
    errors: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)
    
    # 执行统计
    execution_time_ms: float = 0.0
    
    @property
    def summary(self) -> Dict[str, Any]:
        """生成摘要字典"""
        return {
            "circuit_name": self.circuit_name,
            "validation_time": self.validation_time.isoformat(),
            "passed": self.passed,
            "has_equivalence_check": self.equivalence_result is not None,
            "has_test_results": self.test_results is not None,
            "num_reference_comparisons": len(self.reference_results),
            "error_count": len(self.errors),
            "warning_count": len(self.warnings),
            "execution_time_ms": self.execution_time_ms,
        }


class Validator:
    """
    量子电路验证器主类
    
    整合等价性检查、测试执行和参考对比功能
    """
    
    def __init__(
        self,
        matrix_size_limit: int = 10,
        tolerance: float = 1e-10
    ):
        """
        初始化验证器
        
        Args:
            matrix_size_limit: 矩阵大小限制（量子比特数）
            tolerance: 数值容差
        """
        self.matrix_size_limit = matrix_size_limit
        self.tolerance = tolerance
        
        # 初始化子组件
        self._equivalence_checker = EquivalenceChecker(matrix_size_limit)
        self._test_executor = TestExecutor(matrix_size_limit)
        self._reference_comparator = ReferenceComparator(tolerance, matrix_size_limit)
        
        # 创建内置参考实现
        self._reference_comparator.create_builtin_references()
        
        logger.info(
            f"Validator initialized with matrix_size_limit={matrix_size_limit}, "
            f"tolerance={tolerance}"
        )
    
    def validate(
        self,
        circuit: QuantumCircuit,
        reference_circuit: Optional[QuantumCircuit] = None,
        test_suite: Optional[TestSuite] = None,
        reference_names: Optional[List[str]] = None,
        skip_equivalence: bool = False,
        skip_tests: bool = False,
        skip_reference: bool = False
    ) -> ValidationReport:
        """
        执行完整验证流程
        
        Args:
            circuit: 待验证电路
            reference_circuit: 可选的参考电路（用于等价性检查）
            test_suite: 可选的测试套件
            reference_names: 可选的参考实现名称列表
            skip_equivalence: 是否跳过等价性检查
            skip_tests: 是否跳过测试执行
            skip_reference: 是否跳过参考对比
            
        Returns:
            ValidationReport 包含所有验证结果
        """
        import time
        start_time = time.time()
        
        report = ValidationReport(circuit_name=circuit.name)
        
        logger.info(f"Starting validation for circuit: {circuit.name}")
        
        # 1. 等价性检查
        if not skip_equivalence and reference_circuit is not None:
            logger.info("Running equivalence check...")
            try:
                equiv_result = self._equivalence_checker.check_equivalence(
                    circuit, reference_circuit
                )
                report.equivalence_result = equiv_result
                
                if not equiv_result.is_equivalent:
                    report.errors.append(
                        f"Equivalence check failed: {equiv_result.error_message}"
                    )
                elif equiv_result.phase_difference:
                    report.warnings.append(
                        f"Circuits differ by global phase: {equiv_result.phase_difference:.6f}"
                    )
                    
            except Exception as e:
                error_msg = f"Equivalence check error: {str(e)}"
                logger.error(error_msg)
                report.errors.append(error_msg)
        
        # 2. 测试执行
        if not skip_tests and test_suite is not None:
            logger.info(f"Running test suite: {test_suite.name}...")
            try:
                test_result = self._test_executor.execute(circuit, test_suite)
                report.test_results = test_result
                
                if test_result.failed_tests > 0:
                    report.errors.append(
                        f"Test suite failed: {test_result.failed_tests}/{test_result.total_tests} tests failed"
                    )
                    
            except Exception as e:
                error_msg = f"Test execution error: {str(e)}"
                logger.error(error_msg)
                report.errors.append(error_msg)
        
        # 3. 参考对比
        if not skip_reference and reference_names:
            logger.info(f"Running reference comparisons: {reference_names}...")
            for ref_name in reference_names:
                try:
                    ref_result = self._reference_comparator.compare_with_reference(
                        circuit, ref_name
                    )
                    report.reference_results.append(ref_result)
                    
                    if not ref_result.is_equivalent:
                        report.errors.append(
                            f"Reference comparison '{ref_name}' failed: {ref_result.error_message}"
                        )
                        
                    # 检测异常
                    anomalies = self._reference_comparator.detect_anomalies(ref_result)
                    report.warnings.extend(anomalies)
                    
                except Exception as e:
                    error_msg = f"Reference comparison '{ref_name}' error: {str(e)}"
                    logger.error(error_msg)
                    report.errors.append(error_msg)
        
        # 计算执行时间
        report.execution_time_ms = (time.time() - start_time) * 1000
        
        # 确定总体结果
        report.passed = len(report.errors) == 0
        
        logger.info(
            f"Validation completed: {len(report.errors)} errors, "
            f"{len(report.warnings)} warnings, "
            f"time: {report.execution_time_ms:.2f}ms"
        )
        
        return report
    
    def validate_equivalence(
        self,
        circuit1: QuantumCircuit,
        circuit2: QuantumCircuit
    ) -> EquivalenceResult:
        """
        快速等价性检查
        
        Args:
            circuit1: 第一个电路
            circuit2: 第二个电路
            
        Returns:
            EquivalenceResult 等价性检查结果
        """
        return self._equivalence_checker.check_equivalence(circuit1, circuit2)
    
    def validate_with_tests(
        self,
        circuit: QuantumCircuit,
        test_suite: TestSuite
    ) -> TestSuiteResult:
        """
        使用测试套件验证电路
        
        Args:
            circuit: 待验证电路
            test_suite: 测试套件
            
        Returns:
            TestSuiteResult 测试结果
        """
        return self._test_executor.execute(circuit, test_suite)
    
    def compare_with_reference(
        self,
        circuit: QuantumCircuit,
        reference_name: str
    ) -> ReferenceComparisonResult:
        """
        与参考实现对比
        
        Args:
            circuit: 待验证电路
            reference_name: 参考实现名称
            
        Returns:
            ReferenceComparisonResult 对比结果
        """
        return self._reference_comparator.compare_with_reference(circuit, reference_name)
    
    def register_reference(
        self,
        name: str,
        generator
    ) -> None:
        """
        注册自定义参考实现
        
        Args:
            name: 参考实现名称
            generator: 生成参考电路的函数
        """
        self._reference_comparator.register_reference(name, generator)
    
    def generate_report(
        self,
        report: ValidationReport,
        format: str = "text",
        verbose: bool = True
    ) -> str:
        """
        生成验证报告
        
        Args:
            report: 验证报告对象
            format: 报告格式 ("text", "json", "markdown")
            verbose: 是否包含详细信息
            
        Returns:
            格式化报告字符串
        """
        if format == "json":
            return self._generate_json_report(report, verbose)
        elif format == "markdown":
            return self._generate_markdown_report(report, verbose)
        else:
            return self._generate_text_report(report, verbose)
    
    def _generate_text_report(
        self,
        report: ValidationReport,
        verbose: bool
    ) -> str:
        """生成文本格式报告"""
        lines = []
        
        # 标题
        lines.append("=" * 70)
        lines.append(" " * 20 + "QUANTUM CIRCUIT VALIDATION REPORT")
        lines.append("=" * 70)
        lines.append("")
        
        # 基本信息
        lines.append(f"Circuit Name: {report.circuit_name}")
        lines.append(f"Validation Time: {report.validation_time.strftime('%Y-%m-%d %H:%M:%S')}")
        lines.append(f"Execution Time: {report.execution_time_ms:.2f} ms")
        lines.append("")
        
        # 总体结果
        lines.append("-" * 70)
        if report.passed:
            lines.append("OVERALL RESULT: ✓ PASSED")
        else:
            lines.append("OVERALL RESULT: ✗ FAILED")
        lines.append("-" * 70)
        lines.append("")
        
        # 等价性检查
        if report.equivalence_result:
            lines.append("=" * 70)
            lines.append("EQUIVALENCE CHECK")
            lines.append("=" * 70)
            equiv = report.equivalence_result
            if equiv.is_equivalent:
                lines.append("Status: ✓ EQUIVALENT")
                if equiv.phase_difference:
                    lines.append(f"Phase Difference: {equiv.phase_difference:.6f} rad")
            else:
                lines.append("Status: ✗ NOT EQUIVALENT")
                lines.append(f"Error: {equiv.error_message}")
                if equiv.max_difference > 0:
                    lines.append(f"Max Difference: {equiv.max_difference:.2e}")
            lines.append("")
        
        # 测试结果
        if report.test_results:
            lines.append("=" * 70)
            lines.append("TEST EXECUTION")
            lines.append("=" * 70)
            test = report.test_results
            lines.append(f"Suite: {test.suite_name}")
            lines.append(f"Total Tests: {test.total_tests}")
            lines.append(f"Passed: {test.passed_tests} ✓")
            lines.append(f"Failed: {test.failed_tests} ✗")
            lines.append(f"Skipped: {test.skipped_tests} ○")
            lines.append(f"Pass Rate: {test.pass_rate * 100:.1f}%")
            
            if verbose and test.get_failed_tests():
                lines.append("")
                lines.append("Failed Test Details:")
                for ft in test.get_failed_tests():
                    lines.append(f"  • {ft.test_case.name}")
                    lines.append(f"    {ft.error_message}")
            lines.append("")
        
        # 参考对比
        if report.reference_results:
            lines.append("=" * 70)
            lines.append("REFERENCE COMPARISONS")
            lines.append("=" * 70)
            for ref in report.reference_results:
                status_icon = "✓" if ref.is_equivalent else "✗"
                lines.append(f"{status_icon} {ref.reference_name}: {ref.status.value.upper()}")
                
                if verbose:
                    for mc in ref.metric_comparisons:
                        if mc.metric_name in ["gate_count", "depth", "two_qubit_gates"]:
                            lines.append(f"    {mc.metric_name}: {mc.actual_value} (ref: {mc.reference_value})")
            lines.append("")
        
        # 错误和警告
        if report.errors:
            lines.append("=" * 70)
            lines.append("ERRORS")
            lines.append("=" * 70)
            for i, error in enumerate(report.errors, 1):
                lines.append(f"{i}. {error}")
            lines.append("")
        
        if report.warnings:
            lines.append("=" * 70)
            lines.append("WARNINGS")
            lines.append("=" * 70)
            for i, warning in enumerate(report.warnings, 1):
                lines.append(f"{i}. {warning}")
            lines.append("")
        
        # 页脚
        lines.append("=" * 70)
        lines.append(f"Report generated by QuantumAtlas Validator")
        lines.append("=" * 70)
        
        return "\n".join(lines)
    
    def _generate_json_report(
        self,
        report: ValidationReport,
        verbose: bool
    ) -> str:
        """生成JSON格式报告"""
        data = report.summary
        
        if report.equivalence_result:
            data["equivalence"] = {
                "is_equivalent": report.equivalence_result.is_equivalent,
                "phase_difference": report.equivalence_result.phase_difference,
                "error_message": report.equivalence_result.error_message,
                "max_difference": report.equivalence_result.max_difference,
            }
        
        if report.test_results:
            data["tests"] = {
                "suite_name": report.test_results.suite_name,
                "total": report.test_results.total_tests,
                "passed": report.test_results.passed_tests,
                "failed": report.test_results.failed_tests,
                "skipped": report.test_results.skipped_tests,
                "pass_rate": report.test_results.pass_rate,
            }
            if verbose:
                data["tests"]["failed_details"] = [
                    {
                        "name": r.test_case.name,
                        "error": r.error_message,
                        "max_diff": r.max_difference,
                    }
                    for r in report.test_results.get_failed_tests()
                ]
        
        data["errors"] = report.errors
        data["warnings"] = report.warnings
        
        return json.dumps(data, indent=2)
    
    def _generate_markdown_report(
        self,
        report: ValidationReport,
        verbose: bool
    ) -> str:
        """生成Markdown格式报告"""
        lines = []
        
        lines.append(f"# Validation Report: {report.circuit_name}")
        lines.append("")
        lines.append(f"**Validation Time:** {report.validation_time.strftime('%Y-%m-%d %H:%M:%S')}")
        lines.append(f"**Execution Time:** {report.execution_time_ms:.2f} ms")
        lines.append("")
        
        # 总体结果
        if report.passed:
            lines.append("> ✅ **Validation PASSED**")
        else:
            lines.append("> ❌ **Validation FAILED**")
        lines.append("")
        
        # 等价性检查
        if report.equivalence_result:
            lines.append("## Equivalence Check")
            equiv = report.equivalence_result
            if equiv.is_equivalent:
                lines.append("- **Status:** ✅ Equivalent")
                if equiv.phase_difference:
                    lines.append(f"- **Phase Difference:** {equiv.phase_difference:.6f} rad")
            else:
                lines.append("- **Status:** ❌ Not Equivalent")
                lines.append(f"- **Error:** {equiv.error_message}")
            lines.append("")
        
        # 测试结果
        if report.test_results:
            lines.append("## Test Results")
            test = report.test_results
            lines.append(f"- **Suite:** {test.suite_name}")
            lines.append(f"- **Total:** {test.total_tests}")
            lines.append(f"- **Passed:** {test.passed_tests} ✅")
            lines.append(f"- **Failed:** {test.failed_tests} ❌")
            lines.append(f"- **Pass Rate:** {test.pass_rate * 100:.1f}%")
            lines.append("")
        
        # 错误和警告
        if report.errors:
            lines.append("## Errors")
            for error in report.errors:
                lines.append(f"- ❌ {error}")
            lines.append("")
        
        if report.warnings:
            lines.append("## Warnings")
            for warning in report.warnings:
                lines.append(f"- ⚠️ {warning}")
            lines.append("")
        
        return "\n".join(lines)
    
    def save_report(
        self,
        report: ValidationReport,
        output_path: str,
        format: str = "auto"
    ) -> None:
        """
        保存报告到文件
        
        Args:
            report: 验证报告
            output_path: 输出文件路径
            format: 格式（"auto", "text", "json", "md"）
        """
        # 自动检测格式
        if format == "auto":
            if output_path.endswith('.json'):
                format = "json"
            elif output_path.endswith('.md'):
                format = "markdown"
            else:
                format = "text"
        
        # 生成报告内容
        content = self.generate_report(report, format=format, verbose=True)
        
        # 保存到文件
        with open(output_path, 'w', encoding='utf-8') as f:
            f.write(content)
        
        logger.info(f"Report saved to: {output_path}")