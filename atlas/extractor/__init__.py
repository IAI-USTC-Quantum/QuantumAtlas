"""
Information Extractor Module

Responsibilities:
- Extract algorithm metadata from parsed papers
- Extract pseudocode and complexity information
- Interface with LLM for structured extraction
- Map algorithms to primitives
"""

from .algorithm_ir import AlgorithmIR, Complexity
from .extractor import AlgorithmExtractor, ExtractionError, create_extractor
from .llm_interface import (
    ClaudeProvider,
    ExtractionResult,
    LLMInterface,
    LLMProvider,
    OpenAIProvider,
    TokenUsage,
    create_llm,
)

__all__ = [
    "AlgorithmExtractor",
    "AlgorithmIR",
    "ClaudeProvider",
    "Complexity",
    "create_extractor",
    "create_llm",
    "ExtractionError",
    "ExtractionResult",
    "LLMInterface",
    "LLMProvider",
    "OpenAIProvider",
    "TokenUsage",
]
