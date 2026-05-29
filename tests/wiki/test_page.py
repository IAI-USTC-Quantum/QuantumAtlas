"""
Tests for WikiPage and WikiFrontmatter models.
"""

import pytest
from datetime import datetime
from pathlib import Path

from qatlas.wiki.page import WikiPage, WikiFrontmatter


class TestWikiFrontmatter:
    """Tests for WikiFrontmatter model."""

    def test_create_frontmatter(self):
        """Test creating a basic frontmatter."""
        fm = WikiFrontmatter(
            id="test-concept",
            title="Test Concept",
            type="concept",
            tags=["test", "example"],
        )

        assert fm.id == "test-concept"
        assert fm.title == "Test Concept"
        assert fm.type == "concept"
        assert fm.tags == ["test", "example"]
        assert fm.status == "draft"
        assert fm.neo4j_synced is False

    def test_frontmatter_to_yaml_dict(self):
        """Test converting frontmatter to YAML-compatible dict."""
        fm = WikiFrontmatter(
            id="test",
            title="Test",
            type="entity",
            category="algorithm",
            created_at=datetime(2024, 1, 15),
        )

        yaml_dict = fm.to_yaml_dict()

        assert yaml_dict["id"] == "test"
        assert yaml_dict["title"] == "Test"
        assert yaml_dict["type"] == "entity"
        assert yaml_dict["category"] == "algorithm"
        assert yaml_dict["created_at"] == "2024-01-15"

    def test_frontmatter_valid_types(self):
        """Test that valid types are accepted."""
        for valid_type in ["concept", "entity", "source", "comparison"]:
            fm = WikiFrontmatter(id="test", title="Test", type=valid_type)
            assert fm.type == valid_type

    def test_frontmatter_valid_status(self):
        """Test that valid statuses are accepted."""
        for valid_status in ["draft", "review", "published"]:
            fm = WikiFrontmatter(
                id="test",
                title="Test",
                type="concept",
                status=valid_status,
            )
            assert fm.status == valid_status

    def test_frontmatter_doi_fields_roundtrip(self):
        """All four DOI fields serialize with the canonical bare DOI + date."""
        fm = WikiFrontmatter(
            id="paper-arxiv-1234.5678",
            title="DOI Paper",
            type="source",
            category="paper",
            doi="10.1103/PhysRevLett.103.150502",
            doi_source="crossref",
            doi_confidence="high",
            doi_resolved_at=datetime(2026, 5, 28),
        )

        yaml_dict = fm.to_yaml_dict()

        assert yaml_dict["doi"] == "10.1103/PhysRevLett.103.150502"
        assert yaml_dict["doi_source"] == "crossref"
        assert yaml_dict["doi_confidence"] == "high"
        assert yaml_dict["doi_resolved_at"] == "2026-05-28"

    def test_frontmatter_doi_unset_fields_dropped(self):
        """to_yaml_dict drops every doi* key when fully unset (no YAML noise)."""
        fm = WikiFrontmatter(
            id="plain-paper",
            title="Plain Paper",
            type="source",
            category="paper",
        )

        yaml_dict = fm.to_yaml_dict()

        for key in ("doi", "doi_source", "doi_confidence", "doi_resolved_at"):
            assert key not in yaml_dict, f"unset {key} leaked into yaml dict"

    def test_frontmatter_doi_unresolved_marker_kept(self):
        """doi_source='unresolved' must persist even when doi is None.

        This is how `qatlas wiki enrich-doi` tells "never tried" from
        "tried, no hit". Dropping the marker would silently re-fire
        W009 + waste API quota on repeat lookups.
        """
        fm = WikiFrontmatter(
            id="tried-paper",
            title="Tried Paper",
            type="source",
            category="paper",
            doi_source="unresolved",
            doi_resolved_at=datetime(2026, 5, 28),
        )

        yaml_dict = fm.to_yaml_dict()

        assert yaml_dict.get("doi_source") == "unresolved"
        assert yaml_dict.get("doi_resolved_at") == "2026-05-28"
        assert "doi" not in yaml_dict
        assert "doi_confidence" not in yaml_dict


class TestWikiPage:
    """Tests for WikiPage model."""

    def test_create_page(self):
        """Test creating a basic page."""
        fm = WikiFrontmatter(
            id="test-page",
            title="Test Page",
            type="concept",
        )
        page = WikiPage(frontmatter=fm, content="This is test content.")

        assert page.frontmatter.id == "test-page"
        assert page.content == "This is test content."

    def test_to_markdown(self):
        """Test serializing page to markdown."""
        fm = WikiFrontmatter(
            id="test",
            title="Test",
            type="concept",
            tags=["a", "b"],
            created_at=datetime(2024, 1, 1),
        )
        page = WikiPage(frontmatter=fm, content="Content here.")

        markdown = page.to_markdown()

        assert markdown.startswith("---\n")
        assert "id: test" in markdown
        assert "title: Test" in markdown
        assert "type: concept" in markdown
        assert "---\n\nContent here." in markdown

    def test_from_markdown(self):
        """Test parsing page from markdown."""
        markdown = """---
id: my-page
title: My Page
type: entity
category: algorithm
tags:
  - quantum
  - algorithm
created_at: 2024-01-15
status: published
---

## Overview

This is the content.
"""

        page = WikiPage.from_markdown(markdown)

        assert page.frontmatter.id == "my-page"
        assert page.frontmatter.title == "My Page"
        assert page.frontmatter.type == "entity"
        assert page.frontmatter.category == "algorithm"
        assert page.frontmatter.tags == ["quantum", "algorithm"]
        assert page.frontmatter.status == "published"
        assert "## Overview" in page.content

    def test_roundtrip(self):
        """Test that to_markdown and from_markdown are inverses."""
        original = WikiPage(
            frontmatter=WikiFrontmatter(
                id="roundtrip",
                title="Roundtrip Test",
                type="concept",
                tags=["test"],
            ),
            content="Test content with [[links]].",
        )

        markdown = original.to_markdown()
        parsed = WikiPage.from_markdown(markdown)

        assert parsed.frontmatter.id == original.frontmatter.id
        assert parsed.frontmatter.title == original.frontmatter.title
        assert parsed.frontmatter.type == original.frontmatter.type
        assert parsed.content == original.content

    def test_extract_links(self):
        """Test extracting wiki links from content."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(id="test", title="Test", type="concept"),
            content="See [[prim-qft]] and [[algo-shors|Shor's Algorithm]].",
        )

        links = page.extract_links()

        assert len(links) == 2
        assert "prim-qft" in links
        assert "algo-shors" in links

    def test_get_summary(self):
        """Test extracting summary from content."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(id="test", title="Test", type="concept"),
            content="## Summary\n\nThis is the summary.\n\n## Details\n\nMore info.",
        )

        summary = page.get_summary(100)

        assert "This is the summary" in summary

    def test_extract_section(self):
        """Test extracting a specific section."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(id="test", title="Test", type="concept"),
            content="## Summary\n\nSummary text.\n\n## Definition\n\nDef text.",
        )

        section = page.extract_section("Definition")

        assert "Def text" in section

    def test_update_timestamp(self):
        """Test updating the timestamp."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(id="test", title="Test", type="concept"),
            content="Content",
        )

        assert page.frontmatter.updated_at is None

        page.update_timestamp()

        assert page.frontmatter.updated_at is not None

    def test_roundtrip_with_doi_fields(self):
        """End-to-end markdown round-trip preserves all four DOI fields."""
        original = WikiPage(
            frontmatter=WikiFrontmatter(
                id="paper-arxiv-1234.5678",
                title="DOI Round-trip",
                type="source",
                category="paper",
                doi="10.1103/PhysRevLett.103.150502",
                doi_source="openalex",
                doi_confidence="medium",
                doi_resolved_at=datetime(2026, 5, 28),
            ),
            content="Body.",
        )

        markdown = original.to_markdown()
        parsed = WikiPage.from_markdown(markdown)

        assert parsed.frontmatter.doi == "10.1103/PhysRevLett.103.150502"
        assert parsed.frontmatter.doi_source == "openalex"
        assert parsed.frontmatter.doi_confidence == "medium"
        assert parsed.frontmatter.doi_resolved_at is not None
        assert parsed.frontmatter.doi_resolved_at.strftime("%Y-%m-%d") == "2026-05-28"
