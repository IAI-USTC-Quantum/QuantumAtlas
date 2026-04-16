"""
Validator CLI 模块

命令行接口，用于验证量子电路文件

Usage:
    python -m atlas.validator <circuit_file> --reference <ref_file>
    python -m atlas.validator <circuit_file> --test-suite <test_suite.yaml>
    python -m atlas.validator <circuit_file> --reference <ref_file> --output report.txt
"""

import argparse
import sys
import json
import yaml
import logging
from pathlib import Path
from typing import Optional

# 配置日志
logging.basicConfig(
    level=logging.INFO,
    format='%(levelname)s: %(message)s'
)
logger = logging.getLogger(__name__)


def load_circuit(file_path: str) -> "QuantumCircuit":
    """从文件加载电路"""
    path = Path(file_path)
    
    if not path.exists():
        raise FileNotFoundError(f"Circuit file not found: {file_path}")
    
    with open(path, 'r') as f:
        data = json.load(f)
    
    from atlas.designer.quantum_circuit import QuantumCircuit
    return QuantumCircuit.from_dict(data)


def load_test_suite(file_path: str) -> "TestSuite":
    """从YAML文件加载测试套件"""
    path = Path(file_path)
    
    if not path.exists():
        raise FileNotFoundError(f"Test suite file not found: {file_path}")
    
    with open(path, 'r') as f:
        data = yaml.safe_load(f)
    
    from atlas.validator.test_framework import TestSuite, TestCase
    import numpy as np
    
    suite = TestSuite(
        name=data.get('name', 'Unnamed Suite'),
        description=data.get('description', '')
    )
    
    for tc_data in data.get('test_cases', []):
        # 处理输入状态
        input_state = None
        if 'input_state' in tc_data:
            input_state = np.array(tc_data['input_state'], dtype=complex)
        
        # 处理期望输出
        expected_output = None
        if 'expected_output' in tc_data:
            expected_output = np.array(tc_data['expected_output'], dtype=complex)
        
        # 处理概率分布
        expected_distribution = tc_data.get('expected_distribution')
        
        test_case = TestCase(
            name=tc_data['name'],
            description=tc_data.get('description', ''),
            input_state=input_state,
            expected_output=expected_output,
            expected_distribution=expected_distribution,
            input_basis_state=tc_data.get('input_basis_state'),
            expected_basis_state=tc_data.get('expected_basis_state'),
            tolerance=tc_data.get('tolerance', 1e-10)
        )
        
        suite.add_test(test_case)
    
    return suite


def create_argument_parser() -> argparse.ArgumentParser:
    """创建命令行参数解析器"""
    parser = argparse.ArgumentParser(
        prog='atlas.validator',
        description='Quantum Circuit Validator - Verify quantum circuit correctness',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Basic equivalence check
  python -m atlas.validator circuit.json --reference ref_circuit.json
  
  # Run test suite
  python -m atlas.validator circuit.json --test-suite tests.yaml
  
  # Full validation with report
  python -m atlas.validator circuit.json \\
      --reference ref_circuit.json \\
      --test-suite tests.yaml \\
      --output report.txt \\
      --format text
  
  # Compare with built-in reference
  python -m atlas.validator bell_circuit.json --compare-with bell_state
        """
    )
    
    # 位置参数
    parser.add_argument(
        'circuit_file',
        help='Path to the quantum circuit JSON file'
    )
    
    # 可选参数
    parser.add_argument(
        '-r', '--reference',
        help='Path to reference circuit file for equivalence check'
    )
    
    parser.add_argument(
        '-t', '--test-suite',
        help='Path to test suite YAML file'
    )
    
    parser.add_argument(
        '-c', '--compare-with',
        help='Compare with built-in reference (bell_state, ghz_state, qft)'
    )
    
    parser.add_argument(
        '-o', '--output',
        help='Output file path for the validation report'
    )
    
    parser.add_argument(
        '-f', '--format',
        choices=['text', 'json', 'markdown', 'md'],
        default='text',
        help='Report format (default: text)'
    )
    
    parser.add_argument(
        '--matrix-size-limit',
        type=int,
        default=10,
        help='Matrix size limit in qubits (default: 10)'
    )
    
    parser.add_argument(
        '--tolerance',
        type=float,
        default=1e-10,
        help='Numerical tolerance (default: 1e-10)'
    )
    
    parser.add_argument(
        '-v', '--verbose',
        action='store_true',
        help='Enable verbose output'
    )
    
    parser.add_argument(
        '--skip-equivalence',
        action='store_true',
        help='Skip equivalence check'
    )
    
    parser.add_argument(
        '--skip-tests',
        action='store_true',
        help='Skip test execution'
    )
    
    parser.add_argument(
        '-q', '--quiet',
        action='store_true',
        help='Suppress non-error output'
    )
    
    return parser


def main(args: Optional[list] = None) -> int:
    """
    主入口函数
    
    Returns:
        退出码 (0=成功, 1=失败)
    """
    parser = create_argument_parser()
    parsed_args = parser.parse_args(args)
    
    # 设置日志级别
    if parsed_args.quiet:
        logging.getLogger().setLevel(logging.ERROR)
    elif parsed_args.verbose:
        logging.getLogger().setLevel(logging.DEBUG)
    
    try:
        # 加载待验证电路
        logger.info(f"Loading circuit from: {parsed_args.circuit_file}")
        circuit = load_circuit(parsed_args.circuit_file)
        logger.info(f"Loaded circuit: {circuit.name} ({circuit.num_qubits} qubits, {circuit.gate_count} gates)")
        
        # 加载参考电路（如果指定）
        reference_circuit = None
        if parsed_args.reference:
            logger.info(f"Loading reference circuit from: {parsed_args.reference}")
            reference_circuit = load_circuit(parsed_args.reference)
            logger.info(f"Loaded reference: {reference_circuit.name}")
        
        # 加载测试套件（如果指定）
        test_suite = None
        if parsed_args.test_suite:
            logger.info(f"Loading test suite from: {parsed_args.test_suite}")
            test_suite = load_test_suite(parsed_args.test_suite)
            logger.info(f"Loaded test suite: {test_suite.name} ({len(test_suite)} tests)")
        
        # 确定参考实现列表
        reference_names = []
        if parsed_args.compare_with:
            reference_names = [name.strip() for name in parsed_args.compare_with.split(',')]
        
        # 初始化验证器
        from atlas.validator.validator import Validator
        validator = Validator(
            matrix_size_limit=parsed_args.matrix_size_limit,
            tolerance=parsed_args.tolerance
        )
        
        # 执行验证
        logger.info("Starting validation...")
        report = validator.validate(
            circuit=circuit,
            reference_circuit=reference_circuit,
            test_suite=test_suite,
            reference_names=reference_names,
            skip_equivalence=parsed_args.skip_equivalence,
            skip_tests=parsed_args.skip_tests
        )
        
        # 生成报告
        report_content = validator.generate_report(
            report,
            format=parsed_args.format,
            verbose=parsed_args.verbose
        )
        
        # 输出报告
        if parsed_args.output:
            validator.save_report(report, parsed_args.output, parsed_args.format)
            if not parsed_args.quiet:
                print(f"Report saved to: {parsed_args.output}")
        else:
            print(report_content)
        
        # 返回退出码
        return 0 if report.passed else 1
        
    except FileNotFoundError as e:
        logger.error(f"File not found: {e}")
        return 1
    except json.JSONDecodeError as e:
        logger.error(f"Invalid JSON in circuit file: {e}")
        return 1
    except yaml.YAMLError as e:
        logger.error(f"Invalid YAML in test suite file: {e}")
        return 1
    except Exception as e:
        logger.error(f"Validation failed: {e}")
        if parsed_args.verbose:
            import traceback
            traceback.print_exc()
        return 1


if __name__ == '__main__':
    sys.exit(main())