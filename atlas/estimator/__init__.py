"""
资源估算模块 (Estimator)

职责：
- 估算算法所需的量子比特数量
- 估算电路深度和 T 门数量
- 估算运行时间和错误率
- 生成资源需求报告

核心组件：
- ResourceAnalyzer: 分析电路资源需求
- ReportGenerator: 生成资源报告
- ResourceEstimator: 主入口类，整合分析和报告功能

使用示例：
    from atlas.estimator import ResourceEstimator, ResourceAnalyzer
    
    # 使用主类进行完整分析
    estimator = ResourceEstimator()
    report = estimator.estimate(circuit, algorithm_name="My Algorithm")
    
    # 快速分析
    analyzer = ResourceAnalyzer()
    stats = analyzer.analyze(circuit)
    print(f"Depth: {stats.depth}, Gates: {stats.total_gates}")
    
CLI 使用：
    python -m atlas.estimator <circuit_file>
    python -m atlas.estimator --demo
"""

from .resource_analyzer import ResourceAnalyzer, ResourceStats
from .report_generator import ReportGenerator
from .estimator import ResourceEstimator

__all__ = [
    "ResourceAnalyzer",
    "ResourceStats", 
    "ReportGenerator",
    "ResourceEstimator",
]