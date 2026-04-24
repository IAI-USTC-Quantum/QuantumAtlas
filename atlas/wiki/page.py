"""
WikiPage Model

Defines the structured representation of wiki pages with YAML frontmatter.

All wiki pages follow the format:
```
---
id: page-id
title: Page Title
type: concept | entity | source | comparison
...
---

Markdown content here...
```
"""

from datetime import datetime
from pathlib import Path
from typing import Any, ClassVar, Dict, List, Literal, Optional
import re

from pydantic import BaseModel, Field

try:
    import yaml
except ImportError:
    raise ImportError(
        "PyYAML is required for wiki page parsing. "
        "Install with: pip install pyyaml"
    )


class WikiFrontmatter(BaseModel):
    """
    YAML frontmatter for wiki pages.

    This model defines the structured metadata that appears at the top
    of every wiki page, enclosed in --- delimiters.
    """

    id: str = Field(..., description="Unique page identifier")
    title: str = Field(..., description="Page title for display")
    type: Literal["concept", "entity", "source", "comparison"] = Field(
        ..., description="Page type determining content structure"
    )
    category: Optional[str] = Field(
        None, description="Sub-type category (e.g., algorithm, primitive, paper)"
    )
    tags: List[str] = Field(
        default_factory=list, description="Tags for classification and search"
    )
    created_at: datetime = Field(
        default_factory=datetime.now, description="Creation timestamp"
    )
    updated_at: Optional[datetime] = Field(
        None, description="Last update timestamp"
    )
    version: int = Field(1, description="Page version number")
    status: Literal["draft", "review", "published"] = Field(
        "draft", description="Publication status"
    )
    related: List[str] = Field(
        default_factory=list, description="Related page IDs"
    )
    external_links: List["ExternalLink"] = Field(
        default_factory=list,
        description="External resource links such as PDFs, datasets, or databases",
    )
    neo4j_synced: bool = Field(
        False, description="Whether this page has been synced to Neo4j"
    )
    neo4j_id: Optional[str] = Field(
        None, description="Corresponding Neo4j node ID"
    )

    def to_yaml_dict(self) -> Dict[str, Any]:
        """Convert to dictionary suitable for YAML serialization."""
        data = self.model_dump()
        # Convert datetime to ISO strings
        if isinstance(data.get("created_at"), datetime):
            data["created_at"] = data["created_at"].strftime("%Y-%m-%d")
        if isinstance(data.get("updated_at"), datetime):
            data["updated_at"] = data["updated_at"].strftime("%Y-%m-%d")
        elif data.get("updated_at") is None:
            del data["updated_at"]
        return data


class ExternalLink(BaseModel):
    """Structured external link attached to a wiki page."""

    label: str = Field(..., description="Display label")
    url: str = Field(..., description="Absolute URL")
    kind: Literal["paper", "pdf", "dataset", "database", "code", "doc", "other"] = Field(
        "other",
        description="Link category for downstream tooling",
    )
    note: Optional[str] = Field(None, description="Optional usage note")


class WikiPage(BaseModel):
    """
    Complete wiki page with frontmatter and content.

    A wiki page consists of:
    1. YAML frontmatter (structured metadata)
    2. Markdown content (human-readable content)

    Usage:
        # Parse from file
        page = WikiPage.from_file(Path("wiki/concepts/qft.md"))

        # Parse from markdown string
        page = WikiPage.from_markdown("---\\nid: qft\\n---\\nContent")

        # Create new page
        page = WikiPage(
            frontmatter=WikiFrontmatter(id="qft", title="QFT", type="concept"),
            content="Description..."
        )

        # Serialize
        markdown = page.to_markdown()
    """

    frontmatter: WikiFrontmatter = Field(..., description="Page frontmatter")
    content: str = Field(..., description="Markdown content")

    # Regex pattern for frontmatter extraction (class variable, not a field)
    FRONTMATTER_PATTERN: ClassVar[re.Pattern] = re.compile(
        r'^---\s*\n(.*?)\n---\s*\n(.*)$',
        re.DOTALL
    )

    @classmethod
    def from_markdown(cls, markdown: str) -> "WikiPage":
        """
        Parse wiki page from markdown string with frontmatter.

        Args:
            markdown: Complete markdown string with YAML frontmatter

        Returns:
            WikiPage instance

        Raises:
            ValueError: If markdown format is invalid
        """
        match = cls.FRONTMATTER_PATTERN.match(markdown.strip())
        if not match:
            raise ValueError(
                "Invalid wiki page format: missing frontmatter. "
                "Expected format:\\n---\\n<yaml>\\n---\\n<content>"
            )

        fm_yaml = match.group(1)
        content = match.group(2).strip()

        try:
            fm_data = yaml.safe_load(fm_yaml)
        except yaml.YAMLError as e:
            raise ValueError(f"Invalid YAML frontmatter: {e}")

        # Parse datetime fields
        if isinstance(fm_data.get("created_at"), str):
            try:
                fm_data["created_at"] = datetime.strptime(
                    fm_data["created_at"], "%Y-%m-%d"
                )
            except ValueError:
                # Try ISO format
                fm_data["created_at"] = datetime.fromisoformat(
                    fm_data["created_at"]
                )

        if isinstance(fm_data.get("updated_at"), str):
            try:
                fm_data["updated_at"] = datetime.strptime(
                    fm_data["updated_at"], "%Y-%m-%d"
                )
            except ValueError:
                fm_data["updated_at"] = datetime.fromisoformat(
                    fm_data["updated_at"]
                )

        frontmatter = WikiFrontmatter(**fm_data)

        return cls(frontmatter=frontmatter, content=content)

    @classmethod
    def from_file(cls, filepath: Path) -> "WikiPage":
        """
        Parse wiki page from file.

        Args:
            filepath: Path to markdown file

        Returns:
            WikiPage instance

        Raises:
            FileNotFoundError: If file does not exist
            ValueError: If file content is invalid
        """
        filepath = Path(filepath)
        if not filepath.exists():
            raise FileNotFoundError(f"Wiki page not found: {filepath}")

        markdown = filepath.read_text(encoding="utf-8")
        return cls.from_markdown(markdown)

    def to_markdown(self) -> str:
        """
        Serialize wiki page to markdown with frontmatter.

        Returns:
            Complete markdown string with YAML frontmatter
        """
        fm_dict = self.frontmatter.to_yaml_dict()
        fm_yaml = yaml.dump(
            fm_dict,
            default_flow_style=False,
            sort_keys=False,
            allow_unicode=True,
        )

        return f"---\n{fm_yaml}---\n\n{self.content}"

    def save(self, filepath: Path) -> Path:
        """
        Save wiki page to file.

        Args:
            filepath: Destination file path

        Returns:
            Path to saved file
        """
        filepath = Path(filepath)
        filepath.parent.mkdir(parents=True, exist_ok=True)
        filepath.write_text(self.to_markdown(), encoding="utf-8")
        return filepath

    def extract_links(self) -> List[str]:
        """
        Extract all wiki links from content.

        Wiki links are in format [[page-id]] or [[page-id|display text]].

        Returns:
            List of linked page IDs
        """
        link_pattern = r'\[\[([^\]|]+)(?:\|[^\]]+)?\]\]'
        return list(set(re.findall(link_pattern, self.content)))

    def extract_section(self, section_title: str) -> Optional[str]:
        """
        Extract a specific section from content.

        Args:
            section_title: Title of the section (without #)

        Returns:
            Section content or None if not found
        """
        pattern = rf'^##\s+{re.escape(section_title)}\s*\n(.*?)(?=\n##\s+|\Z)'
        match = re.search(pattern, self.content, re.MULTILINE | re.DOTALL)
        if match:
            return match.group(1).strip()
        return None

    def get_summary(self, max_length: int = 200) -> str:
        """
        Get a brief summary of the page content.

        Args:
            max_length: Maximum summary length

        Returns:
            Truncated summary string
        """
        # Try to get Summary section first
        summary_section = self.extract_section("Summary")
        if summary_section:
            if len(summary_section) <= max_length:
                return summary_section
            return summary_section[:max_length - 3] + "..."

        # Fall back to first paragraph
        paragraphs = self.content.split("\n\n")
        for para in paragraphs:
            para = para.strip()
            if para and not para.startswith("#"):
                if len(para) <= max_length:
                    return para
                return para[:max_length - 3] + "..."

        return ""

    def update_timestamp(self) -> None:
        """Update the updated_at timestamp."""
        self.frontmatter.updated_at = datetime.now()

    def __str__(self) -> str:
        return f"WikiPage(id={self.frontmatter.id}, type={self.frontmatter.type})"

    def __repr__(self) -> str:
        return (
            f"WikiPage(frontmatter={self.frontmatter!r}, "
            f"content=<{len(self.content)} chars>)"
        )
