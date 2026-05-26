"""Tests for arXiv fetcher."""

import re

import pytest
import requests
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
        
        assert fetcher._normalize_arxiv_id("quant-ph/9508027") == "quant-ph/9508027"
        assert fetcher._normalize_arxiv_id("cs/9508027") == "cs/9508027"
    
    def test_normalize_arxiv_id_with_version(self):
        """Test normalizing arXiv IDs with version."""
        fetcher = ArxivFetcher()
        
        assert fetcher._normalize_arxiv_id("9508027v1") == "9508027v1"
        assert fetcher._normalize_arxiv_id("9508027v2") == "9508027v2"
        assert fetcher._normalize_arxiv_id("quant-ph/9508027v3") == "quant-ph/9508027v3"
    
    def test_normalize_arxiv_id_new_format(self):
        """Test normalizing new format arXiv IDs."""
        fetcher = ArxivFetcher()
        
        assert fetcher._normalize_arxiv_id("2401.12345") == "2401.12345"
        assert fetcher._normalize_arxiv_id("arxiv:2401.12345") == "2401.12345"
    
    def test_is_valid_arxiv_id(self):
        """Test validating arXiv IDs."""
        fetcher = ArxivFetcher()
        
        assert fetcher._is_valid_arxiv_id("9508027") is True
        assert fetcher._is_valid_arxiv_id("9508027v1") is True
        assert fetcher._is_valid_arxiv_id("quant-ph/9508027v1") is True
        assert fetcher._is_valid_arxiv_id("2401.12345") is True
        assert fetcher._is_valid_arxiv_id("2401.123456v2") is True
        assert fetcher._is_valid_arxiv_id("invalid") is False
        assert fetcher._is_valid_arxiv_id("123") is False

    def test_fetch_metadata_uses_https_api_endpoint(self, monkeypatch):
        """Test metadata fetch goes directly to the HTTPS export endpoint."""
        fetcher = ArxivFetcher()
        calls = {}

        class DummyResponse:
            content = b"""<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/quant-ph/9508027v1</id>
    <title>Test Paper</title>
    <summary>Test abstract</summary>
    <author><name>Test Author</name></author>
    <category term="quant-ph" />
  </entry>
</feed>
"""

            def raise_for_status(self):
                return None

        def fake_get(url, timeout):
            calls["url"] = url
            calls["timeout"] = timeout
            return DummyResponse()

        monkeypatch.setattr(fetcher.session, "get", fake_get)

        metadata = fetcher.fetch_metadata("quant-ph/9508027")

        assert calls["url"] == "https://export.arxiv.org/api/query?id_list=quant-ph/9508027"
        assert calls["timeout"] == 30
        assert metadata["arxiv_id"] == "quant-ph/9508027v1"
        assert metadata["title"] == "Test Paper"
    
    def test_fetch_pdf_old_style_versioned_id_uses_legacy_pdf_url(self, tmp_path, monkeypatch):
        """Test old-style versioned ids drop vN in the PDF download URL."""
        fetcher = ArxivFetcher(output_dir=str(tmp_path))
        calls = {}

        class DummyResponse:
            def raise_for_status(self):
                return None

            def iter_content(self, chunk_size=8192):
                yield b"%PDF-1.4 legacy"

        def fake_get(url, timeout, stream):
            calls["url"] = url
            calls["timeout"] = timeout
            calls["stream"] = stream
            return DummyResponse()

        monkeypatch.setattr(fetcher.session, "get", fake_get)

        pdf_path = fetcher.fetch_pdf("quant-ph/9508027v1")

        assert calls["url"] == "https://arxiv.org/pdf/quant-ph/9508027.pdf"
        assert calls["timeout"] == 60
        assert calls["stream"] is True
        assert pdf_path.name == "quant-ph__9508027v1.pdf"
        assert pdf_path.read_bytes().startswith(b"%PDF")

    def test_fetch_pdf_new_style_versioned_id_keeps_version_in_pdf_url(self, tmp_path, monkeypatch):
        """Test new-style versioned ids keep vN in the PDF download URL."""
        fetcher = ArxivFetcher(output_dir=str(tmp_path))
        calls = {}

        class DummyResponse:
            def raise_for_status(self):
                return None

            def iter_content(self, chunk_size=8192):
                yield b"%PDF-1.4 modern"

        def fake_get(url, timeout, stream):
            calls["url"] = url
            return DummyResponse()

        monkeypatch.setattr(fetcher.session, "get", fake_get)

        pdf_path = fetcher.fetch_pdf("2401.00001v1")

        assert calls["url"] == "https://arxiv.org/pdf/2401.00001v1.pdf"
        assert pdf_path.name == "2401.00001v1.pdf"
        assert pdf_path.read_bytes().startswith(b"%PDF")

    def test_fetch_uses_requested_id_for_saved_filename_and_metadata_pdf_url(self, tmp_path, monkeypatch):
        """Test fetch saves under the requested id while honoring metadata pdf_url."""
        fetcher = ArxivFetcher(output_dir=str(tmp_path))
        metadata = {
            "arxiv_id": "quant-ph/9508027v1",
            "title": "Test Paper",
            "abstract": "Test abstract",
            "authors": ["Test Author"],
            "published": "1995-08-01T00:00:00Z",
            "updated": "1995-08-01T00:00:00Z",
            "categories": ["quant-ph"],
            "pdf_url": "https://arxiv.org/pdf/quant-ph/9508027.pdf",
            "doi": None,
            "primary_category": "quant-ph",
        }
        captured = {}

        monkeypatch.setattr(fetcher, "fetch_metadata", lambda arxiv_id: metadata)

        def fake_fetch_pdf(arxiv_id, filename=None, pdf_url=None):
            captured["arxiv_id"] = arxiv_id
            captured["filename"] = filename
            captured["pdf_url"] = pdf_url
            path = tmp_path / filename
            path.write_bytes(b"%PDF-1.4 fetched")
            return path

        monkeypatch.setattr(fetcher, "fetch_pdf", fake_fetch_pdf)

        pdf_path, returned_metadata = fetcher.fetch("quant-ph/9508027")

        assert captured["arxiv_id"] == "quant-ph/9508027"
        assert captured["filename"] == "quant-ph__9508027.pdf"
        assert captured["pdf_url"] == "https://arxiv.org/pdf/quant-ph/9508027.pdf"
        assert pdf_path.name == "quant-ph__9508027.pdf"
        assert returned_metadata["arxiv_id"] == "quant-ph/9508027v1"

    @pytest.mark.network
    def test_fetch_metadata_shors_paper(self):
        """Test fetching metadata for Shor's algorithm paper."""
        fetcher = ArxivFetcher()
        
        # Shor's algorithm paper
        try:
            metadata = fetcher.fetch_metadata("quant-ph/9508027v1")
        except requests.RequestException as exc:
            pytest.skip(f"arXiv API unavailable: {exc}")

        assert re.search(r"9508027v\d+$", metadata["arxiv_id"])
        assert "Prime Factorization" in metadata["title"]
        assert len(metadata["authors"]) > 0
        assert "Peter W. Shor" in metadata["authors"]
        assert metadata["abstract"] is not None
        assert len(metadata["abstract"]) > 0
        assert "quant-ph" in metadata["categories"]
    
    @pytest.mark.network
    def test_fetch_metadata_old_style_paper(self):
        """Test fetching metadata for an old-style arXiv paper id."""
        fetcher = ArxivFetcher()
        
        # Old-style arXiv paper id
        try:
            metadata = fetcher.fetch_metadata("quant-ph/9704012")
        except requests.RequestException as exc:
            pytest.skip(f"arXiv API unavailable: {exc}")

        assert re.search(r"9704012v\d+$", metadata["arxiv_id"])
        assert metadata["title"] is not None
        assert len(metadata["title"]) > 0
        assert len(metadata["authors"]) > 0
        assert metadata["abstract"] is not None


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
