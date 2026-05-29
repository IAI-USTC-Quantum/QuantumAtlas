"""
Parameter Mapper Module

Maps Algorithm IR parameters to circuit parameters.
Supports dynamic circuit generation based on input parameters.
"""

import re
import math
from typing import Any, Dict, List, Optional, Tuple, Union
from dataclasses import dataclass


@dataclass
class ParameterSpec:
    """
    Specification for a circuit parameter.
    
    Attributes:
        name: Parameter name
        param_type: Type (int, float, string, bool)
        default: Default value
        min_value: Minimum value (for numeric types)
        max_value: Maximum value (for numeric types)
        description: Parameter description
        required: Whether parameter is required
    """
    name: str
    param_type: str = "int"
    default: Any = None
    min_value: Optional[float] = None
    max_value: Optional[float] = None
    description: str = ""
    required: bool = False


class ParameterMapper:
    """
    Maps Algorithm IR parameters to circuit parameters.
    
    Handles:
    - Parameter extraction from Algorithm IR
    - Type conversion and validation
    - Dynamic qubit count calculation
    - Boundary checking
    
    Usage:
        mapper = ParameterMapper()
        circuit_params = mapper.map_parameters(
            algorithm_ir,
            overrides={"precision": 0.01}
        )
    """
    
    # Common parameter patterns in quantum algorithms
    PARAMETER_PATTERNS = {
        # Input size parameters
        "n": {"type": "int", "min": 1, "max": 30, "description": "Input size / number of qubits"},
        "N": {"type": "int", "min": 1, "max": 2**30, "description": "Problem size (often 2^n)"},
        "num_qubits": {"type": "int", "min": 1, "max": 30, "description": "Number of qubits"},
        "qubits": {"type": "int", "min": 1, "max": 30, "description": "Number of qubits"},
        
        # Precision parameters
        "precision": {"type": "float", "min": 0.0, "max": 1.0, "description": "Precision parameter (epsilon)"},
        "epsilon": {"type": "float", "min": 0.0, "max": 1.0, "description": "Error tolerance"},
        "delta": {"type": "float", "min": 0.0, "max": 1.0, "description": "Confidence parameter"},
        
        # Iteration parameters
        "iterations": {"type": "int", "min": 1, "max": 10000, "description": "Number of iterations"},
        "t": {"type": "int", "min": 1, "max": 10000, "description": "Number of steps/time"},
        "steps": {"type": "int", "min": 1, "max": 10000, "description": "Number of steps"},
        
        # Algorithm-specific parameters
        "a": {"type": "int", "min": 0, "description": "First operand (Shor's algorithm)"},
        "modulus": {"type": "int", "min": 1, "description": "Modulus value"},
        "time": {"type": "float", "min": 0.0, "description": "Evolution time (Hamiltonian simulation)"},
        "lambda": {"type": "float", "min": 0.0, "description": "Eigenvalue"},
    }
    
    def __init__(self):
        """Initialize parameter mapper."""
        self._extracted_params: Dict[str, Any] = {}
        self._validation_errors: List[str] = []
    
    def map_parameters(
        self,
        algorithm_ir: Any,
        overrides: Optional[Dict[str, Any]] = None,
        defaults: Optional[Dict[str, Any]] = None
    ) -> Dict[str, Any]:
        """
        Map Algorithm IR parameters to circuit parameters.
        
        Args:
            algorithm_ir: Algorithm IR object or dict
            overrides: Parameter values to override extracted values
            defaults: Default values for missing parameters
            
        Returns:
            Dictionary of circuit parameters
        """
        overrides = overrides or {}
        defaults = defaults or {}
        
        # Extract parameters from Algorithm IR
        extracted = self._extract_from_ir(algorithm_ir)
        
        # Merge: overrides > extracted > defaults
        params = {}
        
        # Start with defaults
        params.update(defaults)
        
        # Add extracted parameters
        for key, value in extracted.items():
            if key not in overrides:  # Don't override if in overrides
                params[key] = value
        
        # Apply overrides
        params.update(overrides)
        
        # Validate all parameters
        self._validation_errors = []
        params = self._validate_parameters(params)
        
        # Calculate derived parameters
        params = self._calculate_derived(params)
        
        self._extracted_params = params
        return params
    
    def _extract_from_ir(self, algorithm_ir: Any) -> Dict[str, Any]:
        """
        Extract parameters from Algorithm IR.
        
        Args:
            algorithm_ir: Algorithm IR object or dict
            
        Returns:
            Dictionary of extracted parameters
        """
        params = {}
        
        # Handle different input types
        if hasattr(algorithm_ir, 'input_params'):
            # Pydantic model
            ir_data = algorithm_ir.model_dump() if hasattr(algorithm_ir, 'model_dump') else algorithm_ir.__dict__
        elif isinstance(algorithm_ir, dict):
            ir_data = algorithm_ir
        else:
            return params
        
        # Extract input_params
        input_params = ir_data.get('input_params', [])
        if isinstance(input_params, list):
            for param in input_params:
                if isinstance(param, dict):
                    name = param.get('name', param.get('id', ''))
                    value = param.get('value', param.get('default'))
                    if name and value is not None:
                        params[name] = self._parse_value(value)
                elif isinstance(param, str):
                    # Try to parse "name=value" format
                    parsed = self._parse_param_string(param)
                    params.update(parsed)
        
        # Also check complexity for qubit counts
        complexity = ir_data.get('complexity', {})
        if isinstance(complexity, dict):
            qubit_count = complexity.get('qubit_count') or complexity.get('qubits')
            if qubit_count:
                # Try to parse qubit count (could be "n" or "2n+1" or a number)
                parsed = self._parse_qubit_expression(qubit_count)
                params.update(parsed)
        
        # Extract from algorithm ID or name for hints
        algo_id = ir_data.get('id', '')
        algo_name = ir_data.get('name', '')
        
        # Algorithm-specific parameter extraction
        if 'shor' in algo_id.lower() or 'factor' in algo_name.lower():
            # Shor's algorithm - look for modulus
            if 'N' not in params and 'n' in params:
                params['N'] = 2 ** params['n']
        
        elif 'grover' in algo_id.lower() or 'search' in algo_name.lower():
            # Grover's algorithm - iterations ~ sqrt(N)
            if 'iterations' not in params and 'N' in params:
                params['iterations'] = int(math.pi / 4 * math.sqrt(params['N']))
        
        elif 'qpe' in algo_id.lower() or 'estimation' in algo_name.lower():
            # QPE - precision determines counting qubits
            if 'precision' in params and 'num_qubits' not in params:
                # n_counting = ceil(log2(1/precision))
                params['num_qubits'] = math.ceil(math.log2(1 / params['precision']))
        
        return params
    
    def _parse_value(self, value: Any) -> Any:
        """Parse a value to appropriate type."""
        if isinstance(value, (int, float, bool)):
            return value
        
        if isinstance(value, str):
            # Try int
            try:
                return int(value)
            except ValueError:
                pass
            
            # Try float
            try:
                return float(value)
            except ValueError:
                pass
            
            # Try bool
            if value.lower() in ('true', 'yes', '1'):
                return True
            if value.lower() in ('false', 'no', '0'):
                return False
        
        return value
    
    def _parse_param_string(self, param_str: str) -> Dict[str, Any]:
        """Parse parameter string like 'n=4' or 'precision=0.01'."""
        result = {}
        
        # Match "name=value" pattern
        match = re.match(r'^(\w+)\s*=\s*(.+)$', param_str)
        if match:
            name = match.group(1)
            value = self._parse_value(match.group(2))
            result[name] = value
        
        return result
    
    def _parse_qubit_expression(self, expr: Union[str, int]) -> Dict[str, Any]:
        """
        Parse qubit count expression.
        
        Examples:
        - "n" -> {"n": "n"}
        - "2n+1" -> {"n": "n"}
        - 8 -> {"num_qubits": 8}
        """
        result = {}
        
        if isinstance(expr, int):
            result['num_qubits'] = expr
        elif isinstance(expr, str):
            # Check if it's just a number
            try:
                result['num_qubits'] = int(expr)
            except ValueError:
                # Contains variable, extract 'n' if present
                if 'n' in expr.lower():
                    result['n'] = expr
        
        return result
    
    def _validate_parameters(self, params: Dict[str, Any]) -> Dict[str, Any]:
        """
        Validate and sanitize parameters.
        
        Args:
            params: Parameters to validate
            
        Returns:
            Validated parameters
        """
        validated = {}
        
        for name, value in params.items():
            spec = self.PARAMETER_PATTERNS.get(name, {})
            
            # Type conversion
            param_type = spec.get('type', 'string')
            try:
                if param_type == 'int':
                    value = int(value)
                elif param_type == 'float':
                    value = float(value)
                elif param_type == 'bool':
                    value = bool(value)
            except (ValueError, TypeError):
                self._validation_errors.append(
                    f"Parameter '{name}' should be {param_type}, got {type(value).__name__}"
                )
                continue
            
            # Range validation
            if param_type in ('int', 'float'):
                min_val = spec.get('min')
                max_val = spec.get('max')
                
                if min_val is not None and value < min_val:
                    self._validation_errors.append(
                        f"Parameter '{name}' ({value}) is below minimum ({min_val})"
                    )
                    value = min_val
                
                if max_val is not None and value > max_val:
                    self._validation_errors.append(
                        f"Parameter '{name}' ({value}) exceeds maximum ({max_val})"
                    )
                    value = max_val
            
            validated[name] = value
        
        return validated
    
    def _calculate_derived(self, params: Dict[str, Any]) -> Dict[str, Any]:
        """
        Calculate derived parameters.
        
        Args:
            params: Input parameters
            
        Returns:
            Parameters with derived values added
        """
        # Calculate num_qubits if not present
        if 'num_qubits' not in params:
            if 'n' in params:
                params['num_qubits'] = int(params['n'])
            elif 'N' in params:
                # Estimate qubits from problem size
                params['num_qubits'] = math.ceil(math.log2(params['N']))
        
        # Calculate precision if not present
        if 'precision' not in params and 'num_qubits' in params:
            params['precision'] = 1 / (2 ** params['num_qubits'])
        
        # Calculate iterations for amplitude amplification
        if 'iterations' not in params and 'N' in params:
            params['iterations'] = int(math.pi / 4 * math.sqrt(params['N']))
        
        return params
    
    def get_qubit_count(self, params: Dict[str, Any]) -> int:
        """
        Get the number of qubits needed for the circuit.
        
        Args:
            params: Circuit parameters
            
        Returns:
            Number of qubits
        """
        # Direct specification
        if 'num_qubits' in params:
            return int(params['num_qubits'])
        
        if 'n' in params:
            return int(params['n'])
        
        if 'N' in params:
            return math.ceil(math.log2(params['N']))
        
        # Default
        return 4
    
    def get_validation_errors(self) -> List[str]:
        """Get list of validation errors from last mapping."""
        return self._validation_errors.copy()
    
    def is_valid(self) -> bool:
        """Check if last mapping was valid."""
        return len(self._validation_errors) == 0
    
    def add_parameter_spec(
        self,
        name: str,
        param_type: str = "int",
        default: Any = None,
        min_value: Optional[float] = None,
        max_value: Optional[float] = None,
        description: str = "",
        required: bool = False
    ) -> None:
        """
        Add a custom parameter specification.
        
        Args:
            name: Parameter name
            param_type: Type (int, float, string, bool)
            default: Default value
            min_value: Minimum value
            max_value: Maximum value
            description: Description
            required: Whether required
        """
        self.PARAMETER_PATTERNS[name] = {
            "type": param_type,
            "default": default,
            "min": min_value,
            "max": max_value,
            "description": description,
            "required": required,
        }
