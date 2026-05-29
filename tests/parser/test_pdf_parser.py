"""Tests for PDF parser."""

import pytest
from pathlib import Path
from qatlas.parser.pdf_parser import PDFParser, ParsedPaper


class TestPDFParser:
    """Test cases for PDFParser."""
    
    def test_parsed_paper_to_dict(self):
        """Test converting ParsedPaper to dictionary."""
        paper = ParsedPaper(
            title="Test Paper",
            authors=["Author 1", "Author 2"],
            abstract="Test abstract",
            arxiv_id="1234.56789",
            year=2024,
        )
        
        data = paper.to_dict()
        
        assert data["title"] == "Test Paper"
        assert data["authors"] == ["Author 1", "Author 2"]
        assert data["abstract"] == "Test abstract"
        assert data["arxiv_id"] == "1234.56789"
        assert data["year"] == 2024
    
    def test_parsed_paper_to_markdown(self):
        """Test converting ParsedPaper to Markdown."""
        paper = ParsedPaper(
            title="Test Paper",
            authors=["Author 1"],
            arxiv_id="1234.56789",
            year=2024,
            abstract="Test abstract",
            sections={"1 Introduction": "Intro content"},
        )
        
        markdown = paper.to_markdown()
        
        assert "# Test Paper" in markdown
        assert "**Authors:** Author 1" in markdown
        assert "**arXiv:** 1234.56789" in markdown
        assert "## Abstract" in markdown
        assert "Test abstract" in markdown
        assert "## 1 Introduction" in markdown
    
    def test_extract_abstract_from_text(self):
        """Test extracting abstract from text."""
        parser = PDFParser()
        
        text = """
Some title here

Abstract
This is the abstract of the paper. It describes the main contribution.

1. Introduction
The introduction starts here.
        """
        
        abstract = parser._extract_abstract(text)
        
        assert abstract is not None
        assert "This is the abstract" in abstract
    
    def test_extract_sections_from_text(self):
        """Test extracting sections from text."""
        parser = PDFParser()
        
        text = """
Title

Abstract
Abstract text here.

1 Introduction
Intro content here.

2 Method
Method content here.
More method content.

3 Results
Results content here.
        """
        
        sections = parser._extract_sections(text)
        
        assert "1 Introduction" in sections
        assert "2 Method" in sections
        assert "3 Results" in sections
        assert "Intro content" in sections["1 Introduction"]
        assert "Method content" in sections["2 Method"]
    
    def test_parse_nonexistent_file(self):
        """Test parsing a non-existent file raises error."""
        parser = PDFParser()
        
        with pytest.raises(FileNotFoundError):
            parser.parse("/nonexistent/path/paper.pdf")


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
