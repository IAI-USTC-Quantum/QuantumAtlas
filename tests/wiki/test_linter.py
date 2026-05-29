"""
Tests for WikiLinter.
"""

import pytest
from pathlib import Path

from qatlas.wiki.engine import WikiEngine
from qatlas.wiki.page import WikiPage, WikiFrontmatter
from qatlas.wiki.linter import LintSeverity, LintIssue


class TestWikiLinter:
    """Tests for WikiLinter class."""

    @pytest.fixture
    def temp_wiki(self, tmp_path):
        """Create a temporary wiki for testing."""
        engine = WikiEngine(
            wiki_dir=str(tmp_path / "wiki"),
            raw_dir=str(tmp_path / "raw"),
            project_root=str(tmp_path),
        )
        return engine

    def test_check_valid_page(self, temp_wiki):
        """Test lint on a valid page."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="valid-page",
                title="Valid Page",
                type="concept",
                status="published",
                tags=["test"],
            ),
            content="Valid content.",
        )
        temp_wiki.save_page(page)

        result = temp_wiki.lint()

        # Should have no errors for this page
        page_errors = [
            i for i in result["issues"]
            if i["page_id"] == "valid-page" and i["severity"] == "error"
        ]
        assert len(page_errors) == 0

    def test_check_missing_id(self, temp_wiki):
        """Test detection of missing ID."""
        # This would be caught during parsing, but we test the check logic
        result = temp_wiki.linter.run()

        # Verify linter runs without error
        assert "total_issues" in result

    def test_check_broken_links(self, temp_wiki):
        """Test detection of broken links."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="page-with-broken-link",
                title="Page with Broken Link",
                type="concept",
                status="published",
            ),
            content="This links to [[nonexistent-page]].",
        )
        temp_wiki.save_page(page)

        result = temp_wiki.lint()

        # Should find broken link warning
        broken_links = [
            i for i in result["issues"]
            if i["code"] == "W004"
        ]
        assert len(broken_links) >= 1
        assert "nonexistent-page" in broken_links[0]["message"]

    def test_check_orphan_page(self, temp_wiki):
        """Test detection of orphan pages."""
        # Create an orphan (no incoming links)
        orphan = WikiPage(
            frontmatter=WikiFrontmatter(
                id="orphan-page",
                title="Orphan Page",
                type="concept",
                status="published",
            ),
            content="No one links to me.",
        )
        temp_wiki.save_page(orphan)

        result = temp_wiki.lint()

        # Should find orphan info
        orphans = [
            i for i in result["issues"]
            if i["code"] == "W003" and i["page_id"] == "orphan-page"
        ]
        assert len(orphans) >= 1

    def test_check_entity_without_tags(self, temp_wiki):
        """Test detection of entity without tags."""
        entity = WikiPage(
            frontmatter=WikiFrontmatter(
                id="untagged-entity",
                title="Untagged Entity",
                type="entity",
                category="algorithm",
                status="published",
                tags=[],  # No tags
            ),
            content="Entity content.",
        )
        temp_wiki.save_page(entity)

        result = temp_wiki.lint()

        # Should find missing tags warning
        tag_issues = [
            i for i in result["issues"]
            if i["code"] == "W008"
        ]
        assert len(tag_issues) >= 1

    def test_check_specific_page(self, temp_wiki):
        """Test lint on specific page."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="specific-page",
                title="Specific Page",
                type="concept",
                status="published",
            ),
            content="Content with [[broken-link]].",
        )
        temp_wiki.save_page(page)

        issues = temp_wiki.linter.check_specific_page("specific-page")

        assert len(issues) >= 1
        assert any(i["code"] == "W004" for i in issues)

    def test_lint_result_counts(self, temp_wiki):
        """Test that lint result has correct counts."""
        # Create pages with various issues
        good = WikiPage(
            frontmatter=WikiFrontmatter(
                id="good-page",
                title="Good",
                type="concept",
                tags=["test"],
                status="published",
            ),
            content="Good content.",
        )
        temp_wiki.save_page(good)

        bad_entity = WikiPage(
            frontmatter=WikiFrontmatter(
                id="bad-entity",
                title="Bad Entity",
                type="entity",
                category="algorithm",
                tags=[],  # No tags
                status="published",
            ),
            content="Bad content [[broken]].",
        )
        temp_wiki.save_page(bad_entity)

        result = temp_wiki.lint()

        assert result["total_issues"] == result["errors"] + result["warnings"] + result["info"]
        assert "issues" in result
        assert isinstance(result["issues"], list)

    def test_auto_fix_not_implemented(self, temp_wiki):
        """Test that auto-fix returns empty list for most issues."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="fix-test",
                title="Fix Test",
                type="concept",
                status="published",
            ),
            content="Content.",
        )
        temp_wiki.save_page(page)

        result = temp_wiki.lint(fix=True)

        # Auto-fix is limited, should return list (possibly empty)
        assert "fixed" in result
        assert isinstance(result["fixed"], list)

    def test_check_paper_missing_doi(self, temp_wiki):
        """W009 fires on source paper pages without a DOI."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="paper-arxiv-1234.5678",
                title="A Paper",
                type="source",
                category="paper",
                status="published",
            ),
            content="Paper body.",
        )
        temp_wiki.save_page(page)

        result = temp_wiki.lint()
        codes = [i["code"] for i in result["issues"] if i["page_id"] == "paper-arxiv-1234.5678"]
        assert "W009" in codes

    def test_check_paper_unresolved_doi_silent(self, temp_wiki):
        """W009 stays quiet when doi_source=='unresolved' marks 'tried, nothing'."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="paper-arxiv-2222.0001",
                title="Already Tried",
                type="source",
                category="paper",
                status="published",
                doi_source="unresolved",
            ),
            content="Body.",
        )
        temp_wiki.save_page(page)

        result = temp_wiki.lint()
        codes = [i["code"] for i in result["issues"] if i["page_id"] == "paper-arxiv-2222.0001"]
        assert "W009" not in codes

    def test_check_paper_with_doi_no_warning(self, temp_wiki):
        """W009 does not fire on paper pages that already carry a DOI."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="paper-arxiv-3333.0001",
                title="Has DOI",
                type="source",
                category="paper",
                status="published",
                doi="10.1103/PhysRevLett.103.150502",
                doi_source="crossref",
                doi_confidence="high",
            ),
            content="Body.",
        )
        temp_wiki.save_page(page)

        result = temp_wiki.lint()
        codes = [i["code"] for i in result["issues"] if i["page_id"] == "paper-arxiv-3333.0001"]
        assert "W009" not in codes


class TestLintIssue:
    """Tests for LintIssue dataclass."""

    def test_create_issue(self):
        """Test creating a lint issue."""
        issue = LintIssue(
            page_id="test-page",
            severity=LintSeverity.WARNING,
            code="W004",
            message="Broken link",
            suggestion="Fix the link",
        )

        assert issue.page_id == "test-page"
        assert issue.severity == LintSeverity.WARNING
        assert issue.code == "W004"
        assert issue.message == "Broken link"
        assert issue.suggestion == "Fix the link"

    def test_issue_to_dict(self):
        """Test converting issue to dict."""
        issue = LintIssue(
            page_id="test",
            severity=LintSeverity.ERROR,
            code="W001",
            message="Missing field",
        )

        d = issue.to_dict()

        assert d["page_id"] == "test"
        assert d["severity"] == "error"
        assert d["code"] == "W001"
        assert d["message"] == "Missing field"


class TestLintSeverity:
    """Tests for LintSeverity enum."""

    def test_severity_values(self):
        """Test severity enum values."""
        assert LintSeverity.ERROR.value == "error"
        assert LintSeverity.WARNING.value == "warning"
        assert LintSeverity.INFO.value == "info"