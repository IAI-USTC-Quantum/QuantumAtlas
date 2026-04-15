"""
ArXiv Paper Fetcher

Downloads papers from arXiv by ID.
"""

import os
import re
from pathlib import Path
from typing import Optional, Tuple
from urllib.parse import urljoin

import requests


class ArxivFetcher:
    """
    Fetches papers from arXiv.
    
    Usage:
        fetcher = ArxivFetcher()
        pdf_path, metadata = fetcher.fetch("9508027")
    """
    
    ARXIV_ABSTRACT_URL = "https://arxiv.org/abs/"
    ARXIV_PDF_URL = "https://arxiv.org/pdf/"
    
    def __init__(self, output_dir: Optional[str] = None):
        """
        Initialize fetcher.
        
        Args:
            output_dir: Directory to save PDFs (default: ./papers)
        """
        self.output_dir = Path(output_dir or "./papers")
        self.output_dir.mkdir(parents=True, exist_ok=True)
        self.session = requests.Session()
        self.session.headers.update({
            "User-Agent": "QuantumAtlas/0.1.0 (Research Tool)"
        })
    
    def _normalize_arxiv_id(self, arxiv_id: str) -> str:
        """
        Normalize arXiv ID to standard format.
        
        Handles formats like:
        - "9508027"
        - "arXiv:9508027"
        - "arxiv:9508027"
        - "quant-ph/9508027"
        """
        # Remove prefix
        arxiv_id = arxiv_id.strip()
        arxiv_id = re.sub(r'^arxiv:', '', arxiv_id, flags=re.IGNORECASE)
        
        # Remove category prefix if present
        if '/' in arxiv_id:
            arxiv_id = arxiv_id.split('/')[-1]
        
        # Remove version suffix
        arxiv_id = re.sub(r'v\d+$', '', arxiv_id)
        
        return arxiv_id
    
    def _is_valid_arxiv_id(self, arxiv_id: str) -> bool:
        """Check if string looks like a valid arXiv ID."""
        # Old format: 7 digits
        # New format: YYMM.number (4-6 digits after decimal)
        pattern = r'^(\d{7}|\d{4}\.\d{4,6})$'
        return bool(re.match(pattern, arxiv_id))
    
    def fetch_metadata(self, arxiv_id: str) -> dict:
        """
        Fetch paper metadata from arXiv API.
        
        Args:
            arxiv_id: arXiv paper ID
            
        Returns:
            Dictionary with metadata (title, authors, abstract, etc.)
        """
        arxiv_id = self._normalize_arxiv_id(arxiv_id)
        
        if not self._is_valid_arxiv_id(arxiv_id):
            raise ValueError(f"Invalid arXiv ID format: {arxiv_id}")
        
        # Use arXiv API
        api_url = f"http://export.arxiv.org/api/query?id_list={arxiv_id}"
        
        response = self.session.get(api_url, timeout=30)
        response.raise_for_status()
        
        # Parse XML response
        import xml.etree.ElementTree as ET
        
        root = ET.fromstring(response.content)
        
        # Define namespaces
        ns = {
            'atom': 'http://www.w3.org/2005/Atom',
            'arxiv': 'http://arxiv.org/schemas/atom'
        }
        
        entry = root.find('.//atom:entry', ns)
        if entry is None:
            raise ValueError(f"Paper not found: {arxiv_id}")
        
        # Extract metadata
        title = entry.find('atom:title', ns)
        abstract = entry.find('atom:summary', ns)
        published = entry.find('atom:published', ns)
        updated = entry.find('atom:updated', ns)
        
        authors = []
        for author in entry.findall('atom:author', ns):
            name = author.find('atom:name', ns)
            if name is not None:
                authors.append(name.text)
        
        # Get categories
        categories = [cat.get('term') for cat in entry.findall('atom:category', ns)]
        
        # Get PDF link
        pdf_url = None
        for link in entry.findall('atom:link', ns):
            if link.get('title') == 'pdf':
                pdf_url = link.get('href')
                break
        
        # Get DOI if available
        doi = None
        for link in entry.findall('arxiv:doi', ns):
            doi = link.text
            break
        
        return {
            "arxiv_id": arxiv_id,
            "title": title.text.strip() if title is not None else "",
            "abstract": abstract.text.strip() if abstract is not None else "",
            "authors": authors,
            "published": published.text if published is not None else None,
            "updated": updated.text if updated is not None else None,
            "categories": categories,
            "pdf_url": pdf_url or f"{self.ARXIV_PDF_URL}{arxiv_id}.pdf",
            "doi": doi,
            "primary_category": categories[0] if categories else None,
        }
    
    def fetch_pdf(self, arxiv_id: str, filename: Optional[str] = None) -> Path:
        """
        Download PDF from arXiv.
        
        Args:
            arxiv_id: arXiv paper ID
            filename: Optional custom filename (default: {arxiv_id}.pdf)
            
        Returns:
            Path to downloaded PDF
        """
        arxiv_id = self._normalize_arxiv_id(arxiv_id)
        
        if filename is None:
            filename = f"{arxiv_id}.pdf"
        
        output_path = self.output_dir / filename
        
        # Skip if already exists
        if output_path.exists():
            print(f"PDF already exists: {output_path}")
            return output_path
        
        pdf_url = f"{self.ARXIV_PDF_URL}{arxiv_id}.pdf"
        
        print(f"Downloading from {pdf_url}...")
        response = self.session.get(pdf_url, timeout=60, stream=True)
        response.raise_for_status()
        
        with open(output_path, 'wb') as f:
            for chunk in response.iter_content(chunk_size=8192):
                f.write(chunk)
        
        print(f"Downloaded to: {output_path}")
        return output_path
    
    def fetch(self, arxiv_id: str, download_pdf: bool = True) -> Tuple[Optional[Path], dict]:
        """
        Fetch both metadata and PDF.
        
        Args:
            arxiv_id: arXiv paper ID
            download_pdf: Whether to download PDF
            
        Returns:
            Tuple of (pdf_path, metadata)
        """
        arxiv_id = self._normalize_arxiv_id(arxiv_id)
        
        # Fetch metadata
        metadata = self.fetch_metadata(arxiv_id)
        
        # Download PDF if requested
        pdf_path = None
        if download_pdf:
            pdf_path = self.fetch_pdf(arxiv_id)
        
        return pdf_path, metadata


if __name__ == "__main__":
    # Simple CLI test
    import sys
    
    if len(sys.argv) < 2:
        print("Usage: python arxiv_fetcher.py <arxiv_id>")
        print("Example: python arxiv_fetcher.py 9508027")
        sys.exit(1)
    
    arxiv_id = sys.argv[1]
    fetcher = ArxivFetcher()
    
    try:
        pdf_path, metadata = fetcher.fetch(arxiv_id)
        print(f"\nTitle: {metadata['title']}")
        print(f"Authors: {', '.join(metadata['authors'])}")
        print(f"Categories: {', '.join(metadata['categories'])}")
        print(f"PDF: {pdf_path}")
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)
