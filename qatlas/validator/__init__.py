"""
代码验证模块 (Validator)

职责：
- 验证生成的代码语法正确性
- 验证电路逻辑等价性
- 运行模拟验证
- 生成验证报告

主要组件：
- EquivalenceChecker: 电路等价性检查器
- TestExecutor: 测试执行器
- ReferenceComparator: 参考实现对比器
- Validator: 主验证器类
"""

from qatlas.validator.equivalence_checker import EquivalenceChecker, EquivalenceResult
from qatlas.validator.test_framework import (
    TestCase, TestSuite, TestExecutor, TestSuiteResult,
    TestStatus, TestResult
)
from qatlas.validator.reference_comparison import (
    ReferenceComparator, ReferenceComparisonResult,
    ComparisonStatus, MetricComparison
)
from qatlas.validator.validator import Validator, ValidationReport

__all__ = [
    # 等价性检查
    'EquivalenceChecker',
    'EquivalenceResult',
    # 测试框架
    'TestCase',
    'TestSuite',
    'TestExecutor',
    'TestSuiteResult',
    'TestStatus',
    'TestResult',
    # 参考对比
    'ReferenceComparator',
    'ReferenceComparisonResult',
    'ComparisonStatus',
    'MetricComparison',
    # 主验证器
    'Validator',
    'ValidationReport',
]