"""
ArXiv Paper Fetcher

Downloads papers from arXiv by ID.
"""

import os
import re
from pathlib import Path
from typing import Optional, Tuple
import requests

from atlas import __version__


class ArxivFetcher:
    """
    Fetches papers from arXiv.
    
    Usage:
        fetcher = ArxivFetcher()
        pdf_path, metadata = fetcher.fetch("quant-ph/9508027")
    """
    
    ARXIV_ABSTRACT_URL = "https://arxiv.org/abs/"
    ARXIV_PDF_URL = "https://arxiv.org/pdf/"
    ARXIV_API_URL = "https://export.arxiv.org/api/query?id_list="
    
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
            "User-Agent": f"QuantumAtlas/{__version__} (Research Tool)"
        })
    
    def _normalize_arxiv_id(self, arxiv_id: str) -> str:
        """Normalize arXiv ID while preserving category prefixes and version suffixes."""
        return re.sub(r'^arxiv:', '', arxiv_id.strip(), flags=re.IGNORECASE)

    def _strip_version_suffix(self, arxiv_id: str) -> str:
        """Drop a trailing vN suffix when grouping different versions."""
        return re.sub(r'v\d+$', '', self._normalize_arxiv_id(arxiv_id))

    def _extract_entry_arxiv_id(self, entry, ns: dict) -> Optional[str]:
        """Extract the exact versioned arXiv id from an API entry when available."""
        entry_id = entry.find('atom:id', ns)
        if entry_id is None or not entry_id.text:
            return None
        text = entry_id.text.strip()
        if '/abs/' in text:
            return text.split('/abs/', 1)[1]
        return text.rsplit('/', 1)[-1]
    
    def _is_valid_arxiv_id(self, arxiv_id: str) -> bool:
        """Check if string looks like a valid arXiv ID."""
        # Old format: 7 digits
        # New format: YYMM.number (4-6 digits after decimal)
        pattern = r'^(?:[A-Za-z.-]+/\d{7}|\d{7}|\d{4}\.\d{4,6})(?:v\d+)?$'
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

        query_id = arxiv_id
        if '/' in arxiv_id and re.search(r'v\d+$', arxiv_id, flags=re.IGNORECASE):
            query_id = self._strip_version_suffix(arxiv_id)

        # Query the export API over HTTPS directly to avoid flaky HTTP redirects.
        api_url = f"{self.ARXIV_API_URL}{query_id}"
        
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
        
        resolved_arxiv_id = arxiv_id if re.search(r'v\d+$', arxiv_id, flags=re.IGNORECASE) else (self._extract_entry_arxiv_id(entry, ns) or arxiv_id)

        return {
            "arxiv_id": resolved_arxiv_id,
            "title": title.text.strip() if title is not None else "",
            "abstract": abstract.text.strip() if abstract is not None else "",
            "authors": authors,
            "published": published.text if published is not None else None,
            "updated": updated.text if updated is not None else None,
            "categories": categories,
            "pdf_url": pdf_url or f"{self.ARXIV_PDF_URL}{resolved_arxiv_id}.pdf",
            "doi": doi,
            "primary_category": categories[0] if categories else None,
        }
    
    def fetch_pdf(self, arxiv_id: str, filename: Optional[str] = None, pdf_url: Optional[str] = None) -> Path:
        """
        Download PDF from arXiv.
        
        Args:
            arxiv_id: arXiv paper ID
            filename: Optional custom filename (default: {arxiv_id}.pdf)
            pdf_url: Optional explicit PDF URL from arXiv metadata
            
        Returns:
            Path to downloaded PDF
        """
        arxiv_id = self._normalize_arxiv_id(arxiv_id)

        if filename is None:
            filename = f"{arxiv_id.replace('/', '__')}.pdf"
        
        output_path = self.output_dir / filename
        
        # Skip if already exists
        if output_path.exists():
            print(f"PDF already exists: {output_path}")
            return output_path
        
        if pdf_url is None:
            pdf_id = arxiv_id
            # Legacy category-prefixed IDs typically omit the version suffix in PDF URLs.
            if '/' in pdf_id and re.search(r'v\d+$', pdf_id, flags=re.IGNORECASE):
                pdf_id = self._strip_version_suffix(pdf_id)
            pdf_url = f"{self.ARXIV_PDF_URL}{pdf_id}.pdf"
        
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
            filename = f"{arxiv_id.replace('/', '__')}.pdf"
            pdf_path = self.fetch_pdf(
                arxiv_id,
                filename=filename,
                pdf_url=metadata.get("pdf_url"),
            )
        
        return pdf_path, metadata


if __name__ == "__main__":
    # Simple CLI test
    import sys
    
    if len(sys.argv) < 2:
        print("Usage: python arxiv_fetcher.py <arxiv_id>")
        print("Example: python arxiv_fetcher.py quant-ph/9508027")
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
