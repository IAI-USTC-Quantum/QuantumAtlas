"""
PDF Parser

Converts PDF papers to structured text/Markdown.
"""

import re
from pathlib import Path
from typing import Optional, Dict, List, Any
from dataclasses import dataclass, field


try:
    import fitz  # PyMuPDF
except ImportError:
    raise ImportError("PyMuPDF is required. Install with: pip install pymupdf")


@dataclass
class ParsedPaper:
    """Structured representation of a parsed paper."""
    title: Optional[str] = None
    authors: List[str] = field(default_factory=list)
    abstract: Optional[str] = None
    arxiv_id: Optional[str] = None
    year: Optional[int] = None
    sections: Dict[str, str] = field(default_factory=dict)
    raw_text: Optional[str] = None
    metadata: Dict[str, Any] = field(default_factory=dict)
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary."""
        return {
            "title": self.title,
            "authors": self.authors,
            "abstract": self.abstract,
            "arxiv_id": self.arxiv_id,
            "year": self.year,
            "sections": self.sections,
            "metadata": self.metadata,
        }
    
    def to_markdown(self) -> str:
        """Convert to Markdown format."""
        lines = []
        
        if self.title:
            lines.append(f"# {self.title}\n")
        
        if self.authors:
            lines.append(f"**Authors:** {', '.join(self.authors)}\n")
        
        if self.arxiv_id:
            lines.append(f"**arXiv:** {self.arxiv_id}\n")
        
        if self.year:
            lines.append(f"**Year:** {self.year}\n")
        
        if self.abstract:
            lines.append(f"\n## Abstract\n\n{self.abstract}\n")
        
        for section_title, content in self.sections.items():
            lines.append(f"\n## {section_title}\n\n{content}\n")
        
        return "\n".join(lines)


class PDFParser:
    """
    Parses PDF papers to structured text.
    
    Usage:
        parser = PDFParser()
        paper = parser.parse("path/to/paper.pdf")
        print(paper.to_markdown())
    """
    
    def __init__(self):
        """Initialize parser."""
        self.common_sections = [
            "abstract", "introduction", "background", "related work",
            "method", "methods", "methodology", "algorithm",
            "experiments", "experimental results", "results",
            "discussion", "conclusion", "conclusions",
            "references", "acknowledgments", "acknowledgements",
            "appendix", "appendices",
        ]
    
    def parse(self, pdf_path: str | Path, arxiv_metadata: Optional[Dict] = None) -> ParsedPaper:
        """
        Parse a PDF file.
        
        Args:
            pdf_path: Path to PDF file
            arxiv_metadata: Optional metadata from arXiv API
            
        Returns:
            ParsedPaper object
        """
        pdf_path = Path(pdf_path)
        
        if not pdf_path.exists():
            raise FileNotFoundError(f"PDF not found: {pdf_path}")
        
        # Open PDF
        doc = fitz.open(str(pdf_path))
        
        # Extract text from all pages
        full_text = ""
        for page in doc:
            full_text += page.get_text()
        
        # Save page count before closing
        page_count = len(doc)
        doc.close()
        
        # Initialize parsed paper
        paper = ParsedPaper(raw_text=full_text)
        
        # Use arXiv metadata if provided
        if arxiv_metadata:
            paper.title = arxiv_metadata.get("title")
            paper.authors = arxiv_metadata.get("authors", [])
            paper.abstract = arxiv_metadata.get("abstract")
            paper.arxiv_id = arxiv_metadata.get("arxiv_id")
            if arxiv_metadata.get("published"):
                try:
                    paper.year = int(arxiv_metadata["published"][:4])
                except (ValueError, IndexError):
                    pass
        else:
            # Try to extract from PDF text
            paper.title = self._extract_title(full_text)
            paper.authors = self._extract_authors(full_text)
            paper.abstract = self._extract_abstract(full_text)
        
        # Extract sections
        paper.sections = self._extract_sections(full_text)
        
        # Store metadata
        paper.metadata = {
            "page_count": page_count,
            "file_size": pdf_path.stat().st_size,
        }
        
        return paper
    
    def _extract_title(self, text: str) -> Optional[str]:
        """Try to extract title from PDF text."""
        # Title is typically in the first few lines
        lines = text.strip().split('\n')[:20]
        
        # Look for the longest line that's not a section header
        for line in lines:
            line = line.strip()
            if len(line) > 20 and len(line) < 200:
                if not any(s in line.lower() for s in self.common_sections):
                    return line
        
        return None
    
    def _extract_authors(self, text: str) -> List[str]:
        """Try to extract authors from PDF text."""
        # This is a simple heuristic - real implementation would be more robust
        authors = []
        
        # Look for patterns like "Author 1, Author 2, and Author 3"
        lines = text.strip().split('\n')[:30]
        
        for line in lines:
            # Skip lines that are likely not author lists
            if len(line) > 200 or len(line) < 5:
                continue
            
            # Look for email patterns or affiliation markers
            if '@' in line or 'University' in line or 'Institute' in line:
                continue
            
            # Look for comma or 'and' separated names
            if ',' in line or ' and ' in line:
                potential_authors = re.split(r',|\s+and\s+', line)
                potential_authors = [a.strip() for a in potential_authors if len(a.strip()) > 2]
                
                # Filter reasonable author names
                if all(len(a) < 50 and ' ' in a for a in potential_authors):
                    return potential_authors
        
        return authors
    
    def _extract_abstract(self, text: str) -> Optional[str]:
        """Try to extract abstract from PDF text."""
        # Look for "Abstract" section
        patterns = [
            r'(?i)abstract\s*[\n:]+\s*(.+?)(?=\n\s*(?:1\.|I\.|introduction|keywords))',
            r'(?i)abstract\s*(.+?)(?=\n\s*\d+\.\s+\w)',
        ]
        
        for pattern in patterns:
            match = re.search(pattern, text, re.DOTALL)
            if match:
                abstract = match.group(1).strip()
                # Clean up
                abstract = re.sub(r'\s+', ' ', abstract)
                return abstract
        
        return None
    
    def _extract_sections(self, text: str) -> Dict[str, str]:
        """Extract sections from PDF text."""
        sections = {}
        
        # Pattern for section headers
        section_pattern = r'\n\s*(\d+(?:\.\d+)?)\s+([A-Z][A-Za-z\s\-]+)(?=\n)'
        
        matches = list(re.finditer(section_pattern, text))
        
        for i, match in enumerate(matches):
            section_num = match.group(1)
            section_title = match.group(2).strip()
            start = match.end()
            
            # Find end of section
            if i + 1 < len(matches):
                end = matches[i + 1].start()
            else:
                end = len(text)
            
            content = text[start:end].strip()
            
            # Clean up content
            content = re.sub(r'\s+', ' ', content)
            content = re.sub(r'\n+', '\n\n', content)
            
            sections[f"{section_num} {section_title}"] = content
        
        return sections
    
    def save_markdown(self, paper: ParsedPaper, output_path: str | Path) -> Path:
        """
        Save parsed paper as Markdown.
        
        Args:
            paper: ParsedPaper object
            output_path: Output file path
            
        Returns:
            Path to saved file
        """
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        
        markdown = paper.to_markdown()
        output_path.write_text(markdown, encoding='utf-8')
        
        return output_path
    
    def save_json(self, paper: ParsedPaper, output_path: str | Path) -> Path:
        """
        Save parsed paper as JSON.
        
        Args:
            paper: ParsedPaper object
            output_path: Output file path
            
        Returns:
            Path to saved file
        """
        import json
        
        output_path = Path(output_path)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        
        data = paper.to_dict()
        output_path.write_text(json.dumps(data, indent=2, ensure_ascii=False), encoding='utf-8')
        
        return output_path


if __name__ == "__main__":
    # Simple CLI test
    import sys
    
    if len(sys.argv) < 2:
        print("Usage: python pdf_parser.py <pdf_path>")
        sys.exit(1)
    
    pdf_path = sys.argv[1]
    parser = PDFParser()
    
    try:
        paper = parser.parse(pdf_path)
        print(f"Title: {paper.title}")
        print(f"Authors: {', '.join(paper.authors)}")
        print(f"\nAbstract:\n{paper.abstract[:500]}...")
        print(f"\nSections found: {list(paper.sections.keys())}")
        
        # Save outputs
        output_base = Path(pdf_path).stem
        parser.save_markdown(paper, f"{output_base}.md")
        parser.save_json(paper, f"{output_base}.json")
        print(f"\nSaved to {output_base}.md and {output_base}.json")
        
    except Exception as e:
        print(f"Error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
