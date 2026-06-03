"""
Tests for LLM Interface module.

Tests cover:
- OpenAIProvider initialization
- Retry mechanism
- TokenUsage calculations
"""

import os
import time
from unittest.mock import Mock, patch, MagicMock
from typing import Generator

import pytest

from qatlas.extractor.llm_interface import (
    TokenUsage,
    RetryConfig,
    ExtractionResult,
    with_retry,
    OpenAIProvider,
    ClaudeProvider,
    LLMProvider,
    create_llm,
)


class TestTokenUsage:
    """Tests for TokenUsage dataclass."""
    
    def test_default_values(self):
        """Test TokenUsage with default values."""
        usage = TokenUsage()
        assert usage.prompt_tokens == 0
        assert usage.completion_tokens == 0
        assert usage.total_tokens == 0
    
    def test_custom_values(self):
        """Test TokenUsage with custom values."""
        usage = TokenUsage(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        assert usage.prompt_tokens == 100
        assert usage.completion_tokens == 50
        assert usage.total_tokens == 150
    
    def test_addition(self):
        """Test adding two TokenUsage objects."""
        usage1 = TokenUsage(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        usage2 = TokenUsage(prompt_tokens=200, completion_tokens=100, total_tokens=300)
        
        result = usage1 + usage2
        
        assert result.prompt_tokens == 300
        assert result.completion_tokens == 150
        assert result.total_tokens == 450
    
    def test_addition_with_defaults(self):
        """Test adding TokenUsage with default values."""
        usage1 = TokenUsage(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        usage2 = TokenUsage()
        
        result = usage1 + usage2
        
        assert result.prompt_tokens == 100
        assert result.completion_tokens == 50
        assert result.total_tokens == 150


class TestRetryConfig:
    """Tests for RetryConfig dataclass."""
    
    def test_default_values(self):
        """Test RetryConfig with default values."""
        config = RetryConfig()
        assert config.max_retries == 3
        assert config.base_delay == 1.0
        assert config.max_delay == 60.0
        assert config.exponential_base == 2.0
        assert config.retryable_exceptions == (Exception,)
    
    def test_custom_values(self):
        """Test RetryConfig with custom values."""
        config = RetryConfig(
            max_retries=5,
            base_delay=2.0,
            max_delay=30.0,
            exponential_base=3.0,
        )
        assert config.max_retries == 5
        assert config.base_delay == 2.0
        assert config.max_delay == 30.0
        assert config.exponential_base == 3.0


class TestWithRetry:
    """Tests for the with_retry decorator."""
    
    def test_successful_call_no_retry(self):
        """Test successful function call doesn't trigger retry."""
        config = RetryConfig(max_retries=2, base_delay=0.1)
        
        @with_retry(config)
        def successful_func():
            return ExtractionResult(success=True, data={"test": "value"})
        
        result = successful_func()
        
        assert result.success is True
        assert result.data == {"test": "value"}
    
    def test_retry_on_failure_then_success(self):
        """Test retry on failure, then success."""
        config = RetryConfig(max_retries=2, base_delay=0.01)
        call_count = 0
        
        @with_retry(config)
        def sometimes_fails():
            nonlocal call_count
            call_count += 1
            if call_count < 2:
                raise ValueError("Temporary error")
            return ExtractionResult(success=True, data={"attempt": call_count})
        
        result = sometimes_fails()
        
        assert result.success is True
        assert result.data["attempt"] == 2
        assert call_count == 2
    
    def test_retry_exhausted(self):
        """Test when all retries are exhausted."""
        config = RetryConfig(max_retries=2, base_delay=0.01)
        
        @with_retry(config)
        def always_fails():
            raise ValueError("Persistent error")
        
        result = always_fails()
        
        assert result.success is False
        assert "Failed after 3 attempts" in result.error
        assert "Persistent error" in result.error
    
    def test_no_retry_for_non_retryable_exception(self):
        """Test that non-retryable exceptions are not retried."""
        config = RetryConfig(max_retries=2, base_delay=0.01, retryable_exceptions=(ValueError,))
        call_count = 0
        
        @with_retry(config)
        def raises_type_error():
            nonlocal call_count
            call_count += 1
            raise TypeError("Not retryable")
        
        result = raises_type_error()
        
        # Should fail immediately without retry
        assert result.success is False
        assert call_count == 1


class TestOpenAIProvider:
    """Tests for OpenAIProvider."""
    
    def test_init_with_api_key_parameter(self):
        """Test initialization with API key parameter."""
        with patch("qatlas.extractor.llm_interface.OpenAI") as mock_openai:
            provider = OpenAIProvider(api_key="test-key", model="gpt-4")
            
            assert provider.provider == LLMProvider.OPENAI
            assert provider.model == "gpt-4"
            assert provider.api_key == "test-key"
            mock_openai.assert_called_once_with(api_key="test-key")
    
    def test_init_with_yaml_config(self, tmp_path, monkeypatch):
        """Test initialization reads ``openai_api_key`` from
        ``~/.config/qatlas/config.yaml`` when ``api_key`` not passed."""
        home = tmp_path / "home"
        home.mkdir()
        cfg = home / ".config" / "qatlas"
        cfg.mkdir(parents=True)
        (cfg / "config.yaml").write_text("openai_api_key: yaml-key\n")
        monkeypatch.setenv("HOME", str(home))
        monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
        with patch("qatlas.extractor.llm_interface.OpenAI"):
            provider = OpenAIProvider()
            assert provider.api_key == "yaml-key"
    
    def test_init_without_api_key_raises_error(self, tmp_path, monkeypatch):
        """Test that initialization fails without API key."""
        # Isolated empty home so no yaml is present.
        home = tmp_path / "home"
        home.mkdir()
        monkeypatch.setenv("HOME", str(home))
        monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
        with pytest.raises(ValueError, match="OpenAI API key is required"):
            OpenAIProvider()
    
    def test_total_token_usage_tracking(self):
        """Test that token usage is tracked across calls."""
        with patch("qatlas.extractor.llm_interface.OpenAI"):
            provider = OpenAIProvider(api_key="test-key")
            
            # Simulate adding token usage
            provider._total_token_usage = TokenUsage(
                prompt_tokens=100, 
                completion_tokens=50, 
                total_tokens=150
            )
            
            assert provider.total_token_usage.total_tokens == 150
    
    def test_truncate_text(self):
        """Test text truncation."""
        with patch("qatlas.extractor.llm_interface.OpenAI"):
            provider = OpenAIProvider(api_key="test-key")
            
            short_text = "Short text"
            assert provider._truncate_text(short_text, max_chars=100) == short_text
            
            long_text = "A" * 20000
            truncated = provider._truncate_text(long_text, max_chars=15000)
            assert len(truncated) < len(long_text)
            assert "...[truncated]" in truncated


class TestClaudeProvider:
    """Tests for ClaudeProvider."""
    
    def test_init_with_api_key_parameter(self):
        """Test initialization with API key parameter."""
        with patch("qatlas.extractor.llm_interface.anthropic.Anthropic") as mock_anthropic:
            provider = ClaudeProvider(api_key="test-key", model="claude-3-opus")
            
            assert provider.provider == LLMProvider.ANTHROPIC
            assert provider.model == "claude-3-opus"
            assert provider.api_key == "test-key"
            mock_anthropic.assert_called_once_with(api_key="test-key")
    
    def test_init_with_yaml_config(self, tmp_path, monkeypatch):
        """Test initialization reads ``anthropic_api_key`` from
        ``~/.config/qatlas/config.yaml`` when ``api_key`` not passed."""
        home = tmp_path / "home"
        home.mkdir()
        cfg = home / ".config" / "qatlas"
        cfg.mkdir(parents=True)
        (cfg / "config.yaml").write_text("anthropic_api_key: yaml-key\n")
        monkeypatch.setenv("HOME", str(home))
        monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
        with patch("qatlas.extractor.llm_interface.anthropic.Anthropic"):
            provider = ClaudeProvider()
            assert provider.api_key == "yaml-key"
    
    def test_init_without_api_key_raises_error(self, tmp_path, monkeypatch):
        """Test that initialization fails without API key."""
        home = tmp_path / "home"
        home.mkdir()
        monkeypatch.setenv("HOME", str(home))
        monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
        with pytest.raises(ValueError, match="Anthropic API key is required"):
            ClaudeProvider()


class TestCreateLLM:
    """Tests for the create_llm factory function."""
    
    def test_create_openai_provider(self):
        """Test creating OpenAI provider."""
        with patch("qatlas.extractor.llm_interface.OpenAI"):
            provider = create_llm("openai", api_key="test-key")
            assert isinstance(provider, OpenAIProvider)
    
    def test_create_anthropic_provider(self):
        """Test creating Anthropic provider."""
        with patch("qatlas.extractor.llm_interface.anthropic.Anthropic"):
            provider = create_llm("anthropic", api_key="test-key")
            assert isinstance(provider, ClaudeProvider)
    
    def test_create_claude_provider(self):
        """Test creating Claude provider (alias for anthropic)."""
        with patch("qatlas.extractor.llm_interface.anthropic.Anthropic"):
            provider = create_llm("claude", api_key="test-key")
            assert isinstance(provider, ClaudeProvider)
    
    def test_create_unknown_provider_raises_error(self):
        """Test that unknown provider raises error."""
        with pytest.raises(ValueError, match="Unknown provider: unknown"):
            create_llm("unknown")


class TestExtractionResult:
    """Tests for ExtractionResult dataclass."""
    
    def test_success_result(self):
        """Test successful extraction result."""
        result = ExtractionResult(
            success=True,
            data={"name": "Test Algorithm"},
            token_usage=TokenUsage(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        )
        
        assert result.success is True
        assert result.data["name"] == "Test Algorithm"
        assert result.token_usage.total_tokens == 150
    
    def test_failure_result(self):
        """Test failed extraction result."""
        result = ExtractionResult(
            success=False,
            error="API rate limit exceeded",
        )
        
        assert result.success is False
        assert result.error == "API rate limit exceeded"
        assert result.data is None
