"""
等价性检查模块

提供量子电路的等价性验证功能，包括：
- 将电路转换为矩阵表示（小规模电路）
- 比较两个矩阵的等价性（允许全局相位差异）
- 支持特定输入状态的等价性验证
"""

import numpy as np
from typing import Optional, Tuple, Union, List
from dataclasses import dataclass
import logging

from atlas.designer.quantum_circuit import QuantumCircuit, Gate

logger = logging.getLogger(__name__)


@dataclass
class EquivalenceResult:
    """等价性检查结果"""
    is_equivalent: bool
    phase_difference: Optional[float] = None
    error_message: str = ""
    max_difference: float = 0.0
    input_state_tested: Optional[np.ndarray] = None


class EquivalenceChecker:
    """
    量子电路等价性检查器
    
    用于验证两个量子电路是否在数学上等价，允许全局相位差异。
    """
    
    # 标准单量子比特门的矩阵表示
    SINGLE_QUBIT_MATRICES = {
        'H': np.array([[1, 1], [1, -1]]) / np.sqrt(2),
        'X': np.array([[0, 1], [1, 0]]),
        'Y': np.array([[0, -1j], [1j, 0]]),
        'Z': np.array([[1, 0], [0, -1]]),
        'S': np.array([[1, 0], [0, 1j]]),
        'T': np.array([[1, 0], [0, np.exp(1j * np.pi / 4)]]),
    }
    
    def __init__(self, matrix_size_limit: int = 10):
        """
        初始化等价性检查器
        
        Args:
            matrix_size_limit: 矩阵大小限制（量子比特数），超过此限制将使用状态向量验证
        """
        self.matrix_size_limit = matrix_size_limit
        self._tolerance = 1e-10
    
    def circuit_to_matrix(self, circuit: QuantumCircuit) -> np.ndarray:
        """
        将量子电路转换为矩阵表示
        
        Args:
            circuit: 量子电路
            
        Returns:
            电路的酉矩阵表示 (2^n x 2^n)
            
        Raises:
            ValueError: 如果电路量子比特数超过限制或包含测量操作
        """
        if circuit.num_qubits > self.matrix_size_limit:
            raise ValueError(
                f"Circuit has {circuit.num_qubits} qubits, exceeding limit {self.matrix_size_limit}. "
                f"Use state vector verification instead."
            )
        
        # 检查是否包含测量
        for gate in circuit.gates:
            if gate.name == "MEASURE":
                raise ValueError("Cannot convert circuit with measurements to matrix")
        
        n = circuit.num_qubits
        dim = 2 ** n
        
        # 从单位矩阵开始
        matrix = np.eye(dim, dtype=complex)
        
        # 按顺序应用每个门
        for gate in circuit.gates:
            if gate.name == "BARRIER":
                continue
            gate_matrix = self._get_gate_matrix(gate, n)
            matrix = gate_matrix @ matrix
        
        return matrix
    
    def _get_gate_matrix(self, gate: Gate, num_qubits: int) -> np.ndarray:
        """
        获取门在n量子比特系统中的完整矩阵表示
        
        Args:
            gate: 门操作
            num_qubits: 系统总量子比特数
            
        Returns:
            门的完整矩阵表示 (2^n x 2^n)
        """
        if gate.name in self.SINGLE_QUBIT_MATRICES:
            return self._expand_single_qubit_gate(
                self.SINGLE_QUBIT_MATRICES[gate.name],
                gate.target_qubits[0],
                num_qubits
            )
        elif gate.name == "RX":
            theta = gate.params.get("theta", 0)
            rx_matrix = np.array([
                [np.cos(theta/2), -1j * np.sin(theta/2)],
                [-1j * np.sin(theta/2), np.cos(theta/2)]
            ])
            return self._expand_single_qubit_gate(rx_matrix, gate.target_qubits[0], num_qubits)
        elif gate.name == "RY":
            theta = gate.params.get("theta", 0)
            ry_matrix = np.array([
                [np.cos(theta/2), -np.sin(theta/2)],
                [np.sin(theta/2), np.cos(theta/2)]
            ])
            return self._expand_single_qubit_gate(ry_matrix, gate.target_qubits[0], num_qubits)
        elif gate.name == "RZ":
            theta = gate.params.get("theta", 0)
            rz_matrix = np.array([
                [np.exp(-1j * theta/2), 0],
                [0, np.exp(1j * theta/2)]
            ])
            return self._expand_single_qubit_gate(rz_matrix, gate.target_qubits[0], num_qubits)
        elif gate.name == "CNOT":
            return self._get_cnot_matrix(gate.control_qubits[0], gate.target_qubits[0], num_qubits)
        elif gate.name == "CZ":
            return self._get_cz_matrix(gate.control_qubits[0], gate.target_qubits[0], num_qubits)
        elif gate.name == "SWAP":
            return self._get_swap_matrix(gate.target_qubits[0], gate.target_qubits[1], num_qubits)
        else:
            raise ValueError(f"Unknown gate: {gate.name}")
    
    def _expand_single_qubit_gate(
        self, 
        gate_matrix: np.ndarray, 
        target: int, 
        num_qubits: int
    ) -> np.ndarray:
        """
        将单量子比特门扩展到整个系统
        
        Args:
            gate_matrix: 2x2 门矩阵
            target: 目标量子比特索引
            num_qubits: 系统总量子比特数
            
        Returns:
            扩展后的矩阵 (2^n x 2^n)
        """
        # 使用张量积构建完整矩阵
        matrices = []
        for i in range(num_qubits):
            if i == target:
                matrices.append(gate_matrix)
            else:
                matrices.append(np.eye(2))
        
        # 从最高位到最低位构建张量积
        result = matrices[0]
        for mat in matrices[1:]:
            result = np.kron(result, mat)
        
        return result
    
    def _get_cnot_matrix(self, control: int, target: int, num_qubits: int) -> np.ndarray:
        """获取CNOT门的矩阵表示"""
        dim = 2 ** num_qubits
        matrix = np.zeros((dim, dim), dtype=complex)
        
        for i in range(dim):
            # 检查控制位是否为1
            if (i >> (num_qubits - 1 - control)) & 1:
                # 翻转目标位
                j = i ^ (1 << (num_qubits - 1 - target))
                matrix[j, i] = 1
            else:
                matrix[i, i] = 1
        
        return matrix
    
    def _get_cz_matrix(self, control: int, target: int, num_qubits: int) -> np.ndarray:
        """获取CZ门的矩阵表示"""
        dim = 2 ** num_qubits
        matrix = np.eye(dim, dtype=complex)
        
        for i in range(dim):
            # 当控制位和目标位都为1时，添加-1相位
            control_bit = (i >> (num_qubits - 1 - control)) & 1
            target_bit = (i >> (num_qubits - 1 - target)) & 1
            if control_bit and target_bit:
                matrix[i, i] = -1
        
        return matrix
    
    def _get_swap_matrix(self, qubit1: int, qubit2: int, num_qubits: int) -> np.ndarray:
        """获取SWAP门的矩阵表示"""
        dim = 2 ** num_qubits
        matrix = np.zeros((dim, dim), dtype=complex)
        
        for i in range(dim):
            # 交换两个量子比特的状态
            bit1 = (i >> (num_qubits - 1 - qubit1)) & 1
            bit2 = (i >> (num_qubits - 1 - qubit2)) & 1
            
            # 如果两位不同，交换它们
            if bit1 != bit2:
                j = i ^ ((1 << (num_qubits - 1 - qubit1)) | (1 << (num_qubits - 1 - qubit2)))
                matrix[j, i] = 1
            else:
                matrix[i, i] = 1
        
        return matrix
    
    def compare_matrices(
        self, 
        matrix1: np.ndarray, 
        matrix2: np.ndarray,
        tolerance: Optional[float] = None
    ) -> Tuple[bool, float]:
        """
        比较两个矩阵的等价性（允许全局相位差异）
        
        两个矩阵 U1 和 U2 等价当且仅当 U1 = e^(iθ) * U2 对于某个全局相位 θ
        
        Args:
            matrix1: 第一个矩阵
            matrix2: 第二个矩阵
            tolerance: 数值容差，默认使用类设置的容差
            
        Returns:
            (是否等价, 相位差异)
        """
        if tolerance is None:
            tolerance = self._tolerance
        
        if matrix1.shape != matrix2.shape:
            return False, 0.0
        
        # 找到第一个非零元素来确定相位
        # 使用第一个绝对值大于容差的元素
        phase_diff = 0.0
        found_phase = False
        
        for i in range(matrix1.shape[0]):
            for j in range(matrix1.shape[1]):
                if abs(matrix1[i, j]) > tolerance and abs(matrix2[i, j]) > tolerance:
                    # 计算相位差: phase(U2) - phase(U1)
                    # 这样当 U2 = e^(iθ) * U1 时，phase_diff = θ
                    phase1 = np.angle(matrix1[i, j])
                    phase2 = np.angle(matrix2[i, j])
                    phase_diff = phase2 - phase1
                    found_phase = True
                    break
            if found_phase:
                break
        
        if not found_phase:
            # 两个矩阵都接近零矩阵
            return False, 0.0
        
        # 应用相位校正到 matrix1
        # matrix2 = e^(i*phase_diff) * matrix1, 所以 matrix1 = e^(-i*phase_diff) * matrix2
        matrix1_corrected = matrix1 * np.exp(1j * phase_diff)
        
        # 比较校正后的矩阵
        difference = np.abs(matrix1_corrected - matrix2)
        max_diff = np.max(difference)
        
        is_equivalent = max_diff < tolerance
        
        return is_equivalent, phase_diff
    
    def check_equivalence(
        self,
        circuit1: QuantumCircuit,
        circuit2: QuantumCircuit,
        input_state: Optional[np.ndarray] = None
    ) -> EquivalenceResult:
        """
        检查两个电路是否等价
        
        Args:
            circuit1: 第一个电路
            circuit2: 第二个电路
            input_state: 可选的特定输入状态用于验证
            
        Returns:
            EquivalenceResult 包含检查结果和详细信息
        """
        # 检查量子比特数是否一致
        if circuit1.num_qubits != circuit2.num_qubits:
            return EquivalenceResult(
                is_equivalent=False,
                error_message=f"Qubit count mismatch: {circuit1.num_qubits} vs {circuit2.num_qubits}"
            )
        
        # 如果提供了特定输入状态，使用状态向量验证
        if input_state is not None:
            return self._check_equivalence_with_input(circuit1, circuit2, input_state)
        
        # 对于小电路，使用矩阵验证
        if circuit1.num_qubits <= self.matrix_size_limit:
            try:
                matrix1 = self.circuit_to_matrix(circuit1)
                matrix2 = self.circuit_to_matrix(circuit2)
                
                is_equivalent, phase_diff = self.compare_matrices(matrix1, matrix2)
                
                if is_equivalent:
                    return EquivalenceResult(
                        is_equivalent=True,
                        phase_difference=phase_diff
                    )
                else:
                    max_diff = np.max(np.abs(matrix1 - matrix2 * np.exp(1j * phase_diff)))
                    return EquivalenceResult(
                        is_equivalent=False,
                        phase_difference=phase_diff,
                        error_message="Matrices differ beyond tolerance",
                        max_difference=max_diff
                    )
            except ValueError as e:
                # 矩阵转换失败（如包含测量），使用状态向量验证
                logger.warning(f"Matrix conversion failed: {e}, using state vector verification")
                return self._check_equivalence_state_vector(circuit1, circuit2)
        else:
            # 大电路使用状态向量验证
            return self._check_equivalence_state_vector(circuit1, circuit2)
    
    def _check_equivalence_with_input(
        self,
        circuit1: QuantumCircuit,
        circuit2: QuantumCircuit,
        input_state: np.ndarray
    ) -> EquivalenceResult:
        """使用特定输入状态验证等价性"""
        try:
            output1 = self._apply_circuit_to_state(circuit1, input_state)
            output2 = self._apply_circuit_to_state(circuit2, input_state)
            
            # 考虑全局相位
            is_equivalent, phase_diff = self._compare_state_vectors(output1, output2)
            
            if is_equivalent:
                return EquivalenceResult(
                    is_equivalent=True,
                    phase_difference=phase_diff,
                    input_state_tested=input_state
                )
            else:
                max_diff = np.max(np.abs(output1 - output2))
                return EquivalenceResult(
                    is_equivalent=False,
                    error_message="Output states differ for given input",
                    max_difference=max_diff,
                    input_state_tested=input_state
                )
        except Exception as e:
            return EquivalenceResult(
                is_equivalent=False,
                error_message=f"State vector verification failed: {str(e)}"
            )
    
    def _check_equivalence_state_vector(
        self,
        circuit1: QuantumCircuit,
        circuit2: QuantumCircuit
    ) -> EquivalenceResult:
        """使用多个输入状态验证等价性"""
        n = circuit1.num_qubits
        dim = 2 ** n
        
        # 测试计算基态
        for i in range(min(dim, 10)):  # 最多测试10个基态
            input_state = np.zeros(dim, dtype=complex)
            input_state[i] = 1.0
            
            result = self._check_equivalence_with_input(circuit1, circuit2, input_state)
            if not result.is_equivalent:
                return result
        
        # 测试叠加态
        test_states = [
            np.ones(dim, dtype=complex) / np.sqrt(dim),  # 均匀叠加
        ]
        
        for state in test_states:
            result = self._check_equivalence_with_input(circuit1, circuit2, state)
            if not result.is_equivalent:
                return result
        
        return EquivalenceResult(
            is_equivalent=True,
            phase_difference=0.0,
            error_message=""
        )
    
    def _apply_circuit_to_state(
        self, 
        circuit: QuantumCircuit, 
        state: np.ndarray
    ) -> np.ndarray:
        """将电路应用到输入状态"""
        # 移除测量门后进行矩阵模拟
        circuit_no_measure = QuantumCircuit(
            num_qubits=circuit.num_qubits,
            num_clbits=circuit.num_clbits,
            name=circuit.name
        )
        
        for gate in circuit.gates:
            if gate.name != "MEASURE" and gate.name != "BARRIER":
                circuit_no_measure.add_gate(gate)
        
        if circuit.num_qubits <= self.matrix_size_limit:
            matrix = self.circuit_to_matrix(circuit_no_measure)
            return matrix @ state
        else:
            # 对于大电路，逐门应用
            current_state = state.copy()
            for gate in circuit_no_measure.gates:
                gate_matrix = self._get_gate_matrix(gate, circuit.num_qubits)
                current_state = gate_matrix @ current_state
            return current_state
    
    def _compare_state_vectors(
        self,
        state1: np.ndarray,
        state2: np.ndarray,
        tolerance: Optional[float] = None
    ) -> Tuple[bool, float]:
        """比较两个状态向量（允许全局相位）"""
        if tolerance is None:
            tolerance = self._tolerance

        # 找到最大幅度分量来确定相位
        max_idx = np.argmax(np.abs(state1))
        if abs(state1[max_idx]) < tolerance:
            return False, 0.0

        # phase_diff: state2 = e^(i*phase_diff) * state1
        phase_diff = np.angle(state2[max_idx]) - np.angle(state1[max_idx])
        state1_corrected = state1 * np.exp(1j * phase_diff)

        difference = np.abs(state1_corrected - state2)
        max_diff = np.max(difference)

        return max_diff < tolerance, phase_diff