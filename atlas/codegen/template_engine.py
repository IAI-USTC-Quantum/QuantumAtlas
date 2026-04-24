"""
Template Engine Module

Provides a Jinja2-based template engine for code generation.
Supports template caching and rendering with context variables.
"""

import os
from typing import Any, Dict, Optional
from pathlib import Path

try:
    from jinja2 import Environment, BaseLoader, FileSystemLoader, DictLoader
    JINJA2_AVAILABLE = True
except ImportError:
    JINJA2_AVAILABLE = False


class TemplateEngine:
    """
    Jinja2-based template engine for code generation.
    
    Features:
    - Load templates from files or strings
    - Template caching for performance
    - Context variable rendering
    
    Attributes:
        env: Jinja2 Environment instance
        _cache: Cache for loaded templates
    """
    
    def __init__(self, template_dir: Optional[str] = None):
        """
        Initialize the template engine.
        
        Args:
            template_dir: Directory containing template files (optional)
        """
        if not JINJA2_AVAILABLE:
            raise ImportError(
                "Jinja2 is required for template engine. "
                "Install with: pip install jinja2"
            )
        
        self._cache: Dict[str, Any] = {}
        
        if template_dir and os.path.isdir(template_dir):
            self.env = Environment(loader=FileSystemLoader(template_dir))
        else:
            self.env = Environment(loader=BaseLoader())
        
        # Add custom filters
        self.env.filters['upper'] = str.upper
        self.env.filters['lower'] = str.lower
    
    def load_from_file(self, filepath: str) -> "TemplateEngine":
        """
        Load a template from a file and cache it.
        
        Args:
            filepath: Path to the template file
            
        Returns:
            Self for method chaining
            
        Raises:
            FileNotFoundError: If file doesn't exist
        """
        if not os.path.exists(filepath):
            raise FileNotFoundError(f"Template file not found: {filepath}")
        
        template_name = os.path.basename(filepath)
        
        if template_name not in self._cache:
            with open(filepath, 'r', encoding='utf-8') as f:
                template_content = f.read()
            self._cache[template_name] = self.env.from_string(template_content)
        
        return self
    
    def load_from_string(self, name: str, template_content: str) -> "TemplateEngine":
        """
        Load a template from a string and cache it.
        
        Args:
            name: Template name for caching
            template_content: Template content string
            
        Returns:
            Self for method chaining
        """
        if name not in self._cache:
            self._cache[name] = self.env.from_string(template_content)
        
        return self
    
    def render(self, template_name: str, **context: Any) -> str:
        """
        Render a cached template with context variables.
        
        Args:
            template_name: Name of the cached template
            **context: Context variables for rendering
            
        Returns:
            Rendered template string
            
        Raises:
            KeyError: If template not found in cache
        """
        if template_name not in self._cache:
            raise KeyError(f"Template '{template_name}' not found in cache. "
                          "Load it first with load_from_file() or load_from_string()")
        
        template = self._cache[template_name]
        return template.render(**context)
    
    def render_string(self, template_content: str, **context: Any) -> str:
        """
        Render a template string directly without caching.
        
        Args:
            template_content: Template content string
            **context: Context variables for rendering
            
        Returns:
            Rendered template string
        """
        template = self.env.from_string(template_content)
        return template.render(**context)
    
    def clear_cache(self) -> None:
        """Clear the template cache."""
        self._cache.clear()
    
    def get_cached_templates(self) -> list:
        """
        Get list of cached template names.
        
        Returns:
            List of cached template names
        """
        return list(self._cache.keys())
    
    def is_cached(self, template_name: str) -> bool:
        """
        Check if a template is cached.
        
        Args:
            template_name: Template name to check
            
        Returns:
            True if template is cached
        """
        return template_name in self._cache


# Default template directory
DEFAULT_TEMPLATE_DIR = Path(__file__).parent / "templates"


def get_default_engine() -> TemplateEngine:
    """
    Get a template engine with default template directory.
    
    Returns:
        TemplateEngine instance with templates directory loaded
    """
    return TemplateEngine(str(DEFAULT_TEMPLATE_DIR))
