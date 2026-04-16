"""Tests for TemplateEngine."""

import os
import tempfile
import pytest
from atlas.codegen.template_engine import TemplateEngine


class TestTemplateEngine:
    """Test TemplateEngine class."""
    
    def test_init_without_jinja2(self, monkeypatch):
        """Test initialization when Jinja2 is not available."""
        # Mock Jinja2 as unavailable
        monkeypatch.setattr("atlas.codegen.template_engine.JINJA2_AVAILABLE", False)
        
        with pytest.raises(ImportError, match="Jinja2 is required"):
            TemplateEngine()
    
    def test_load_from_string(self):
        """Test loading template from string."""
        engine = TemplateEngine()
        
        template_content = "Hello, {{ name }}!"
        engine.load_from_string("greeting", template_content)
        
        result = engine.render("greeting", name="World")
        assert result == "Hello, World!"
    
    def test_render_string_directly(self):
        """Test rendering string without caching."""
        engine = TemplateEngine()
        
        template = "Value: {{ value }}"
        result = engine.render_string(template, value=42)
        
        assert result == "Value: 42"
    
    def test_load_from_file(self):
        """Test loading template from file."""
        engine = TemplateEngine()
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.j2', delete=False) as f:
            f.write("Result: {{ x }} + {{ y }} = {{ x + y }}")
            temp_path = f.name
        
        try:
            engine.load_from_file(temp_path)
            result = engine.render(os.path.basename(temp_path), x=1, y=2)
            assert result == "Result: 1 + 2 = 3"
        finally:
            os.unlink(temp_path)
    
    def test_load_from_nonexistent_file(self):
        """Test loading from non-existent file raises error."""
        engine = TemplateEngine()
        
        with pytest.raises(FileNotFoundError):
            engine.load_from_file("/nonexistent/path/template.j2")
    
    def test_render_uncached_template(self):
        """Test rendering uncached template raises error."""
        engine = TemplateEngine()
        
        with pytest.raises(KeyError, match="not found in cache"):
            engine.render("nonexistent", value=1)
    
    def test_caching(self):
        """Test template caching works."""
        engine = TemplateEngine()
        
        template = "Test: {{ value }}"
        engine.load_from_string("test", template)
        
        assert engine.is_cached("test")
        assert "test" in engine.get_cached_templates()
        
        # Clear cache
        engine.clear_cache()
        assert not engine.is_cached("test")
    
    def test_multiple_templates(self):
        """Test handling multiple templates."""
        engine = TemplateEngine()
        
        engine.load_from_string("t1", "A: {{ v }}")
        engine.load_from_string("t2", "B: {{ v }}")
        
        result1 = engine.render("t1", v=1)
        result2 = engine.render("t2", v=2)
        
        assert result1 == "A: 1"
        assert result2 == "B: 2"
    
    def test_template_filters(self):
        """Test custom template filters."""
        engine = TemplateEngine()
        
        template = "{{ name|upper }} - {{ name|lower }}"
        result = engine.render_string(template, name="Test")
        
        assert result == "TEST - test"
    
    def test_chaining(self):
        """Test method chaining."""
        engine = TemplateEngine()
        
        result = (engine
            .load_from_string("t1", "Hello")
            .load_from_string("t2", "World")
        )
        
        assert isinstance(result, TemplateEngine)
        assert engine.is_cached("t1")
        assert engine.is_cached("t2")
    
    def test_complex_template(self):
        """Test complex template with conditionals."""
        engine = TemplateEngine()
        
        template = """
{% if enabled %}
Enabled: {{ value }}
{% else %}
Disabled
{% endif %}
""".strip()
        
        engine.load_from_string("complex", template)
        
        result_enabled = engine.render("complex", enabled=True, value=42)
        assert "Enabled: 42" in result_enabled
        
        result_disabled = engine.render("complex", enabled=False, value=42)
        assert "Disabled" in result_disabled


class TestGetDefaultEngine:
    """Test get_default_engine function."""
    
    def test_default_engine_creation(self):
        """Test creating default engine with templates directory."""
        from atlas.codegen.template_engine import get_default_engine
        
        engine = get_default_engine()
        assert isinstance(engine, TemplateEngine)
