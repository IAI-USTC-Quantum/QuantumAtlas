"""Tests for arXiv fetcher."""

import pytest
from atlas.parser.arxiv_fetcher import ArxivFetcher


class TestArxivFetcher:
    """Test cases for ArxivFetcher."""
    
    def test_normalize_arxiv_id_simple(self):
        """Test normalizing simple arXiv IDs."""
        fetcher = ArxivFetcher()
        
        assert fetcher._normalize_arxiv_id("9508027") == "9508027"
        assert fetcher._normalize_arxiv_id("arXiv:9508027") == "9508027"
        assert fetcher._normalize_arxiv_id("arxiv:9508027") == "9508027"
    
    def test_normalize_arxiv_id_with_category(self):
        """Test normalizing arXiv IDs with category."""
        fetcher = ArxivFetcher()
        
        assert fetcher._normalize_arxiv_id("quant-ph/9508027") == "9508027"
        assert fetcher._normalize_arxiv_id("cs/9508027") == "9508027"
    
    def test_normalize_arxiv_id_with_version(self):
        """Test normalizing arXiv IDs with version."""
        fetcher = ArxivFetcher()
        
        assert fetcher._normalize_arxiv_id("9508027v1") == "9508027"
        assert fetcher._normalize_arxiv_id("9508027v2") == "9508027"
    
    def test_normalize_arxiv_id_new_format(self):
        """Test normalizing new format arXiv IDs."""
        fetcher = ArxivFetcher()
        
        assert fetcher._normalize_arxiv_id("2401.12345") == "2401.12345"
        assert fetcher._normalize_arxiv_id("arxiv:2401.12345") == "2401.12345"
    
    def test_is_valid_arxiv_id(self):
        """Test validating arXiv IDs."""
        fetcher = ArxivFetcher()
        
        assert fetcher._is_valid_arxiv_id("9508027") is True
        assert fetcher._is_valid_arxiv_id("2401.12345") is True
        assert fetcher._is_valid_arxiv_id("2401.123456") is True
        assert fetcher._is_valid_arxiv_id("invalid") is False
        assert fetcher._is_valid_arxiv_id("123") is False
    
    @pytest.mark.integration
    def test_fetch_metadata_shors_paper(self):
        """Test fetching metadata for Shor's algorithm paper."""
        fetcher = ArxivFetcher()
        
        # Shor's algorithm paper
        metadata = fetcher.fetch_metadata("9508027")
        
        assert metadata["arxiv_id"] == "9508027"
        assert "Shor" in metadata["title"]
        assert len(metadata["authors"]) > 0
        assert "Peter W. Shor" in metadata["authors"]
        assert metadata["abstract"] is not None
        assert len(metadata["abstract"]) > 0
        assert "quant-ph" in metadata["categories"]
    
    @pytest.mark.integration
    def test_fetch_metadata_grovers_paper(self):
        """Test fetching metadata for Grover's algorithm paper."""
        fetcher = ArxivFetcher()
        
        # Grover's algorithm paper
        metadata = fetcher.fetch_metadata("9704012")
        
        assert metadata["arxiv_id"] == "9704012"
        assert "Grover" in metadata["title"]
        assert len(metadata["authors"]) > 0
        assert metadata["abstract"] is not None


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
