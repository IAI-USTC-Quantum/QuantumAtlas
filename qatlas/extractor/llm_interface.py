"""
LLM Interface Module

Abstract interface for LLM-based information extraction.
Implements concrete providers for OpenAI, Claude, and extensible for others.
"""

import os
import json
import time
import logging
from abc import ABC, abstractmethod
from typing import Dict, Any, Optional, List, Callable
from dataclasses import dataclass, field
from enum import Enum
from functools import wraps

import pydantic
from pydantic import BaseModel, Field

try:
    from openai import OpenAI
except ImportError:  # pragma: no cover - exercised by provider initialization
    OpenAI = None

try:
    import anthropic
except ImportError:  # pragma: no cover - exercised by provider initialization
    anthropic = None

# Configure logging
logger = logging.getLogger(__name__)


class LLMProvider(Enum):
    """Supported LLM providers."""
    OPENAI = "openai"
    ANTHROPIC = "anthropic"
    AZURE_OPENAI = "azure_openai"
    LOCAL = "local"
    CUSTOM = "custom"


@dataclass
class TokenUsage:
    """Token usage information for LLM calls."""
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0
    
    def __add__(self, other: "TokenUsage") -> "TokenUsage":
        """Combine two token usage records."""
        return TokenUsage(
            prompt_tokens=self.prompt_tokens + other.prompt_tokens,
            completion_tokens=self.completion_tokens + other.completion_tokens,
            total_tokens=self.total_tokens + other.total_tokens,
        )


@dataclass
class RetryConfig:
    """Configuration for retry mechanism."""
    max_retries: int = 3
    base_delay: float = 1.0
    max_delay: float = 60.0
    exponential_base: float = 2.0
    retryable_exceptions: tuple = (Exception,)


@dataclass
class ExtractionResult:
    """Result of LLM extraction."""
    success: bool
    data: Optional[Dict[str, Any]] = None
    error: Optional[str] = None
    raw_response: Optional[str] = None
    confidence: Optional[float] = None
    token_usage: TokenUsage = field(default_factory=TokenUsage)


def with_retry(config: Optional[RetryConfig] = None):
    """Decorator for adding retry mechanism to LLM calls."""
    if config is None:
        config = RetryConfig()
    
    def decorator(func: Callable) -> Callable:
        @wraps(func)
        def wrapper(*args, **kwargs) -> ExtractionResult:
            last_exception = None
            
            for attempt in range(config.max_retries + 1):
                try:
                    return func(*args, **kwargs)
                except config.retryable_exceptions as e:
                    last_exception = e
                    if attempt < config.max_retries:
                        delay = min(
                            config.base_delay * (config.exponential_base ** attempt),
                            config.max_delay
                        )
                        logger.warning(
                            f"{func.__name__} failed (attempt {attempt + 1}/{config.max_retries + 1}): {e}. "
                            f"Retrying in {delay:.1f}s..."
                        )
                        time.sleep(delay)
                    else:
                        logger.error(f"{func.__name__} failed after {config.max_retries + 1} attempts: {e}")
                except Exception as e:
                    logger.error(f"{func.__name__} failed with non-retryable error: {e}")
                    return ExtractionResult(success=False, error=str(e))
            
            return ExtractionResult(
                success=False,
                error=f"Failed after {config.max_retries + 1} attempts: {str(last_exception)}"
            )
        return wrapper
    return decorator


# Pydantic models for structured output

class AlgorithmMetadata(BaseModel):
    """Structured output for algorithm metadata extraction."""
    name: str = Field(description="The name of the algorithm")
    description: str = Field(description="A concise description of what the algorithm does")
    authors: List[str] = Field(default_factory=list, description="List of paper authors")
    year: Optional[int] = Field(None, description="Publication year")
    problem_type: str = Field(description="Type of problem the algorithm solves")
    venue: Optional[str] = Field(None, description="Publication venue")
    doi: Optional[str] = Field(None, description="DOI of the paper")


class PseudocodeExtraction(BaseModel):
    """Structured output for pseudocode extraction."""
    algorithm_name: str = Field(description="Name of the algorithm")
    pseudocode: str = Field(description="The pseudocode in plain text format")
    input_description: str = Field(description="Description of input parameters")
    output_description: str = Field(description="Description of output")


class ComplexityExtraction(BaseModel):
    """Structured output for complexity extraction."""
    time_complexity: str = Field(description="Time complexity (e.g., O(n^2), O(2^n))")
    space_complexity: str = Field(description="Space complexity")
    query_complexity: Optional[str] = Field(None, description="Query complexity for quantum algorithms")
    gate_count: Optional[str] = Field(None, description="Number of quantum gates required")
    circuit_depth: Optional[str] = Field(None, description="Depth of the quantum circuit")
    qubit_count: Optional[str] = Field(None, description="Number of qubits required")


class PrimitiveIdentification(BaseModel):
    """Structured output for primitive identification."""
    primitives: List[str] = Field(description="List of primitive IDs used by the algorithm")
    usage_context: Dict[str, str] = Field(default_factory=dict, description="How each primitive is used")


class LLMInterface(ABC):
    """
    Abstract base class for LLM interfaces.
    
    Usage:
        llm = OpenAIProvider(model="gpt-4")
        result = llm.extract_metadata(paper_text)
    """
    
    def __init__(self, provider: LLMProvider, model: str, **kwargs):
        """
        Initialize LLM interface.
        
        Args:
            provider: LLM provider type
            model: Model name/identifier
            **kwargs: Provider-specific configuration
        """
        self.provider = provider
        self.model = model
        self.config = kwargs
        self.retry_config = RetryConfig(
            max_retries=kwargs.get("max_retries", 3),
            base_delay=kwargs.get("base_delay", 1.0),
        )
        self._total_token_usage = TokenUsage()
    
    @property
    def total_token_usage(self) -> TokenUsage:
        """Get total token usage across all extractions."""
        return self._total_token_usage
    
    @abstractmethod
    def extract_metadata(self, paper_text: str) -> ExtractionResult:
        """
        Extract paper metadata (title, authors, year, etc.).
        
        Args:
            paper_text: Full text of the paper
            
        Returns:
            ExtractionResult with structured metadata
        """
        raise NotImplementedError("Subclasses must implement extract_metadata()")
    
    @abstractmethod
    def extract_pseudocode(self, paper_text: str) -> ExtractionResult:
        """
        Extract algorithm pseudocode from paper.
        
        Args:
            paper_text: Full text of the paper
            
        Returns:
            ExtractionResult with structured pseudocode
        """
        raise NotImplementedError("Subclasses must implement extract_pseudocode()")
    
    @abstractmethod
    def extract_complexity(self, paper_text: str) -> ExtractionResult:
        """
        Extract complexity information (gate count, depth, qubits).
        
        Args:
            paper_text: Full text of the paper
            
        Returns:
            ExtractionResult with complexity metrics
        """
        raise NotImplementedError("Subclasses must implement extract_complexity()")
    
    @abstractmethod
    def identify_primitives(
        self,
        paper_text: str,
        available_primitives: Optional[List[str]] = None
    ) -> ExtractionResult:
        """
        Identify quantum primitives used in the algorithm.

        Args:
            paper_text: Full text of the paper
            available_primitives: Optional list of primitive IDs to consider

        Returns:
            ExtractionResult with list of primitive IDs
        """
        raise NotImplementedError("Subclasses must implement identify_primitives()")

    def get_total_usage(self) -> TokenUsage:
        """Get total token usage across all extractions."""
        return self._total_token_usage
    
    def _truncate_text(self, text: str, max_chars: int = 15000) -> str:
        """Truncate text to fit context window."""
        if len(text) <= max_chars:
            return text
        return text[:max_chars] + "\n...[truncated]"


class OpenAIProvider(LLMInterface):
    """
    OpenAI GPT provider for algorithm extraction.
    
    Usage:
        provider = OpenAIProvider(model="gpt-4o")
        result = provider.extract_metadata(paper_text)
    """
    
    def __init__(self, api_key: Optional[str] = None, model: str = "gpt-4o", **kwargs):
        """
        Initialize OpenAI provider.
        
        Args:
            api_key: OpenAI API key (default: from OPENAI_API_KEY env var)
            model: Model name (default: gpt-4o)
            **kwargs: Additional configuration
        """
        super().__init__(LLMProvider.OPENAI, model, **kwargs)
        
        self.api_key = api_key or os.getenv("OPENAI_API_KEY")
        if not self.api_key:
            raise ValueError(
                "OpenAI API key is required. Set OPENAI_API_KEY environment variable "
                "or pass api_key parameter."
            )
        
        if OpenAI is None:
            raise ImportError(
                "openai package is required. Install with: pip install openai>=1.0.0"
            )
        self.client = OpenAI(api_key=self.api_key)
    
    def _call_structured(
        self, 
        system_prompt: str, 
        user_prompt: str, 
        response_format: type
    ) -> ExtractionResult:
        """Make a structured call to OpenAI API."""
        try:
            response = self.client.beta.chat.completions.parse(
                model=self.model,
                messages=[
                    {"role": "system", "content": system_prompt},
                    {"role": "user", "content": user_prompt},
                ],
                response_format=response_format,
            )
            
            # Track token usage
            usage = TokenUsage(
                prompt_tokens=response.usage.prompt_tokens,
                completion_tokens=response.usage.completion_tokens,
                total_tokens=response.usage.total_tokens,
            )
            self._total_token_usage = self._total_token_usage + usage
            
            return ExtractionResult(
                success=True,
                data=response.choices[0].message.parsed.model_dump(),
                token_usage=usage,
            )
        except Exception as e:
            logger.error(f"OpenAI API error: {e}")
            return ExtractionResult(
                success=False,
                error=f"OpenAI API error: {str(e)}"
            )
    
    @with_retry()
    def extract_metadata(self, paper_text: str) -> ExtractionResult:
        """Extract paper metadata using structured output."""
        system_prompt = """You are an expert in quantum computing research. 
Extract algorithm metadata from the provided paper text.
Be precise and extract only information explicitly stated in the text."""
        
        user_prompt = f"""Please analyze the following paper text and extract the algorithm metadata.

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract the algorithm name, description, authors, year, problem type, venue, and DOI."""
        
        return self._call_structured(system_prompt, user_prompt, AlgorithmMetadata)
    
    @with_retry()
    def extract_pseudocode(self, paper_text: str) -> ExtractionResult:
        """Extract algorithm pseudocode from paper."""
        system_prompt = """You are an expert in quantum computing algorithms.
Extract the pseudocode from the provided paper text.
If no explicit pseudocode is present, reconstruct it from the algorithm description."""
        
        user_prompt = f"""Please analyze the following paper text and extract or reconstruct the algorithm pseudocode.

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract the algorithm name, pseudocode, input description, and output description."""
        
        return self._call_structured(system_prompt, user_prompt, PseudocodeExtraction)
    
    @with_retry()
    def extract_complexity(self, paper_text: str) -> ExtractionResult:
        """Extract complexity information from paper."""
        system_prompt = """You are an expert in quantum computing complexity analysis.
Extract complexity metrics from the provided paper text.
Include time, space, query complexity, gate count, circuit depth, and qubit count if mentioned."""
        
        user_prompt = f"""Please analyze the following paper text and extract complexity information.

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract time complexity, space complexity, query complexity, gate count, circuit depth, and qubit count."""
        
        return self._call_structured(system_prompt, user_prompt, ComplexityExtraction)
    
    @with_retry()
    def identify_primitives(
        self,
        paper_text: str,
        available_primitives: Optional[List[str]] = None
    ) -> ExtractionResult:
        """Identify quantum primitives used in the algorithm."""
        system_prompt = """You are an expert in quantum computing primitives.
Identify which quantum primitives (QFT, QPE, Grover, etc.) are used in the algorithm.
Use standard primitive IDs like: primitive_qft, primitive_qpe, primitive_grover,
primitive_vqe, primitive_qaoa, primitive_shors, etc."""

        # Include available primitives in the prompt if provided
        primitives_hint = ""
        if available_primitives:
            primitives_hint = f"\n\nAvailable primitives to consider: {', '.join(available_primitives)}"

        user_prompt = f"""Please analyze the following paper text and identify quantum primitives used.{primitives_hint}

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Identify all quantum primitives used in the algorithm and describe how each is used."""

        return self._call_structured(system_prompt, user_prompt, PrimitiveIdentification)


class ClaudeProvider(LLMInterface):
    """
    Anthropic Claude provider for algorithm extraction.
    
    Usage:
        provider = ClaudeProvider(model="claude-3-sonnet-20240229")
        result = provider.extract_metadata(paper_text)
    """
    
    def __init__(self, api_key: Optional[str] = None, model: str = "claude-3-sonnet-20240229", **kwargs):
        """
        Initialize Claude provider.
        
        Args:
            api_key: Anthropic API key (default: from ANTHROPIC_API_KEY env var)
            model: Model name (default: claude-3-sonnet-20240229)
            **kwargs: Additional configuration
        """
        super().__init__(LLMProvider.ANTHROPIC, model, **kwargs)
        
        self.api_key = api_key or os.getenv("ANTHROPIC_API_KEY")
        if not self.api_key:
            raise ValueError(
                "Anthropic API key is required. Set ANTHROPIC_API_KEY environment variable "
                "or pass api_key parameter."
            )
        
        if anthropic is None:
            raise ImportError(
                "anthropic package is required. Install with: pip install anthropic"
            )
        self.client = anthropic.Anthropic(api_key=self.api_key)
    
    def _call_with_json_output(
        self, 
        system_prompt: str, 
        user_prompt: str,
        response_schema: type
    ) -> ExtractionResult:
        """Make a call to Claude API with JSON output."""
        try:
            # Add JSON instruction to system prompt
            json_instruction = "\n\nRespond with valid JSON only."
            full_system = system_prompt + json_instruction
            
            response = self.client.messages.create(
                model=self.model,
                max_tokens=4096,
                system=full_system,
                messages=[
                    {"role": "user", "content": user_prompt},
                ],
            )
            
            # Claude doesn't provide token usage directly in the same way
            # We'll estimate or set to 0 for now
            usage = TokenUsage(
                prompt_tokens=response.usage.input_tokens,
                completion_tokens=response.usage.output_tokens,
                total_tokens=response.usage.input_tokens + response.usage.output_tokens,
            )
            self._total_token_usage = self._total_token_usage + usage
            
            # Parse JSON response
            content = response.content[0].text
            try:
                data = json.loads(content)
                # Validate with pydantic model
                validated = response_schema(**data)
                return ExtractionResult(
                    success=True,
                    data=validated.model_dump(),
                    raw_response=content,
                    token_usage=usage,
                )
            except (json.JSONDecodeError, pydantic.ValidationError) as e:
                logger.error(f"Failed to parse Claude response as JSON: {e}")
                return ExtractionResult(
                    success=False,
                    error=f"JSON parsing error: {str(e)}",
                    raw_response=content,
                    token_usage=usage,
                )
                
        except Exception as e:
            logger.error(f"Claude API error: {e}")
            return ExtractionResult(
                success=False,
                error=f"Claude API error: {str(e)}"
            )
    
    @with_retry()
    def extract_metadata(self, paper_text: str) -> ExtractionResult:
        """Extract paper metadata using Claude."""
        system_prompt = """You are an expert in quantum computing research. 
Extract algorithm metadata from the provided paper text.
Be precise and extract only information explicitly stated in the text."""
        
        user_prompt = f"""Please analyze the following paper text and extract the algorithm metadata.

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract the following information as JSON:
- name: The name of the algorithm
- description: A concise description of what the algorithm does
- authors: List of paper authors
- year: Publication year (integer or null)
- problem_type: Type of problem the algorithm solves
- venue: Publication venue (string or null)
- doi: DOI of the paper (string or null)"""
        
        return self._call_with_json_output(system_prompt, user_prompt, AlgorithmMetadata)
    
    @with_retry()
    def extract_pseudocode(self, paper_text: str) -> ExtractionResult:
        """Extract algorithm pseudocode from paper using Claude."""
        system_prompt = """You are an expert in quantum computing algorithms.
Extract the pseudocode from the provided paper text.
If no explicit pseudocode is present, reconstruct it from the algorithm description."""
        
        user_prompt = f"""Please analyze the following paper text and extract or reconstruct the algorithm pseudocode.

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract the following information as JSON:
- algorithm_name: Name of the algorithm
- pseudocode: The pseudocode in plain text format
- input_description: Description of input parameters
- output_description: Description of output"""
        
        return self._call_with_json_output(system_prompt, user_prompt, PseudocodeExtraction)
    
    @with_retry()
    def extract_complexity(self, paper_text: str) -> ExtractionResult:
        """Extract complexity information from paper using Claude."""
        system_prompt = """You are an expert in quantum computing complexity analysis.
Extract complexity metrics from the provided paper text.
Include time, space, query complexity, gate count, circuit depth, and qubit count if mentioned."""
        
        user_prompt = f"""Please analyze the following paper text and extract complexity information.

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract the following information as JSON:
- time_complexity: Time complexity (e.g., O(n^2), O(2^n))
- space_complexity: Space complexity
- query_complexity: Query complexity for quantum algorithms (string or null)
- gate_count: Number of quantum gates required (string or null)
- circuit_depth: Depth of the quantum circuit (string or null)
- qubit_count: Number of qubits required (string or null)"""
        
        return self._call_with_json_output(system_prompt, user_prompt, ComplexityExtraction)
    
    @with_retry()
    def identify_primitives(
        self,
        paper_text: str,
        available_primitives: Optional[List[str]] = None
    ) -> ExtractionResult:
        """Identify quantum primitives used in the algorithm using Claude."""
        system_prompt = """You are an expert in quantum computing primitives.
Identify which quantum primitives (QFT, QPE, Grover, etc.) are used in the algorithm.
Use standard primitive IDs like: primitive_qft, primitive_qpe, primitive_grover,
primitive_vqe, primitive_qaoa, primitive_shors, etc."""

        # Include available primitives in the prompt if provided
        primitives_hint = ""
        if available_primitives:
            primitives_hint = f"\n\nAvailable primitives to consider: {', '.join(available_primitives)}"

        user_prompt = f"""Please analyze the following paper text and identify quantum primitives used.{primitives_hint}

--- Paper Text ---

{self._truncate_text(paper_text)}

--- End of Paper Text ---

Extract the following information as JSON:
- primitives: List of primitive IDs used by the algorithm (e.g., ["primitive_qft", "primitive_qpe"])
- usage_context: Object mapping primitive IDs to description of how they are used"""

        return self._call_with_json_output(system_prompt, user_prompt, PrimitiveIdentification)


class LocalLLM(LLMInterface):
    """
    Local LLM interface (vLLM, llama.cpp, etc.).
    
    TODO: Implement local model integration
    """
    
    def __init__(self, model_path: str, **kwargs):
        super().__init__(LLMProvider.LOCAL, model_path, **kwargs)
        # TODO: Initialize local model
    
    def extract_metadata(self, paper_text: str) -> ExtractionResult:
        # TODO: Implement
        return ExtractionResult(success=False, error="Not implemented")
    
    def extract_pseudocode(self, paper_text: str) -> ExtractionResult:
        # TODO: Implement
        return ExtractionResult(success=False, error="Not implemented")
    
    def extract_complexity(self, paper_text: str) -> ExtractionResult:
        # TODO: Implement
        return ExtractionResult(success=False, error="Not implemented")
    
    def identify_primitives(
        self,
        paper_text: str,
        available_primitives: Optional[List[str]] = None
    ) -> ExtractionResult:
        # TODO: Implement
        return ExtractionResult(success=False, error="Not implemented")


def create_llm(provider: str, **kwargs) -> LLMInterface:
    """
    Factory function to create LLM interface.
    
    Usage:
        llm = create_llm("openai", model="gpt-4o")
        llm = create_llm("anthropic", model="claude-3-sonnet-20240229")
        llm = create_llm("local", model_path="/path/to/model")
    """
    provider = provider.lower()
    
    if provider == "openai":
        return OpenAIProvider(**kwargs)
    elif provider in ["anthropic", "claude"]:
        return ClaudeProvider(**kwargs)
    elif provider == "local":
        return LocalLLM(**kwargs)
    else:
        raise ValueError(f"Unknown provider: {provider}")
