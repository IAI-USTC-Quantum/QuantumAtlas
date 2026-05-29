"""
电路设计模块 (Designer)

职责：
- 根据提取的算法要素生成量子电路结构
- 实现标准量子门到目标框架的映射
- 优化电路深度和门数量
- 生成电路的拓扑表示

主要组件：
- CircuitDesigner: 主设计器类
- QuantumCircuit: 量子电路数据模型
- QuantumIR: 量子电路中间表示
- PrimitiveComposer: 原语组合引擎
- CircuitOptimizer: 电路优化器
- ParameterMapper: 参数映射系统
"""

from .quantum_circuit import QuantumCircuit, Gate, GateType
from .quantum_ir import QuantumIR
from .designer import CircuitDesigner
from .primitive_loader import PrimitiveLoader, PrimitiveDefinition
from .primitive_composer import PrimitiveComposer, CompositionResult
from .optimizer import CircuitOptimizer, OptimizationLevel
from .parameter_mapper import ParameterMapper

__all__ = [
    # Main classes
    "CircuitDesigner",
    "QuantumCircuit",
    "QuantumIR",
    "Gate",
    "GateType",
    
    # Primitives
    "PrimitiveLoader",
    "PrimitiveDefinition",
    "PrimitiveComposer",
    "CompositionResult",
    
    # Optimization
    "CircuitOptimizer",
    "OptimizationLevel",
    
    # Parameters
    "ParameterMapper",
]
