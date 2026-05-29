"""
Wiki Linter

Validates wiki pages and detects issues.

Checks:
- W001: Missing required frontmatter field
- W002: Invalid frontmatter field value
- W003: Orphan page (no incoming links)
- W004: Broken link (target page does not exist)
- W005: Missing concept definition
- W006: Duplicate page ID
- W007: Outdated page (not updated in 30 days)
- W008: Entity page has no tags
"""

import logging
import re
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from enum import Enum
from typing import Any, Dict, List, Optional, Set

logger = logging.getLogger(__name__)


class LintSeverity(str, Enum):
    """Severity levels for lint issues."""
    ERROR = "error"
    WARNING = "warning"
    INFO = "info"


@dataclass
class LintIssue:
    """
    Represents a single lint issue.

    Attributes:
        page_id: ID of the page with the issue
        severity: Issue severity level
        code: Issue code (W001-W008)
        message: Human-readable description
        suggestion: Optional fix suggestion
        filepath: Optional file path
    """
    page_id: str
    severity: LintSeverity
    code: str
    message: str
    suggestion: Optional[str] = None
    filepath: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary for serialization."""
        return {
            "page_id": self.page_id,
            "severity": self.severity.value,
            "code": self.code,
            "message": self.message,
            "suggestion": self.suggestion,
            "filepath": self.filepath,
        }


class WikiLinter:
    """
    Linter for wiki pages.

    Performs various health checks on the wiki and reports issues.

    Usage:
        engine = WikiEngine()
        result = engine.linter.run()
        print(f"Found {result['total_issues']} issues")
    """

    # Issue code definitions
    ISSUE_CODES = {
        "W001": ("ERROR", "Missing required frontmatter field"),
        "W002": ("ERROR", "Invalid frontmatter field value"),
        "W003": ("INFO", "Orphan page: no incoming links"),
        "W004": ("WARNING", "Broken link: target page does not exist"),
        "W005": ("WARNING", "Missing concept definition"),
        "W006": ("ERROR", "Duplicate page ID"),
        "W007": ("INFO", "Outdated page: not updated in 30 days"),
        "W008": ("WARNING", "Entity page has no tags"),
    }

    def __init__(self, wiki_engine):
        """
        Initialize linter.

        Args:
            wiki_engine: Parent WikiEngine instance
        """
        self.engine = wiki_engine
        self.issues: List[LintIssue] = []

    def run(self, fix: bool = False) -> Dict[str, Any]:
        """
        Run all lint checks.

        Args:
            fix: Whether to auto-fix issues where possible

        Returns:
            Dict with lint results including:
            - total_issues: Total number of issues
            - errors: Count of ERROR severity issues
            - warnings: Count of WARNING severity issues
            - info: Count of INFO severity issues
            - issues: List of all issues
            - fixed: List of auto-fixed issues
        """
        self.issues = []

        pages = self.engine.list_pages()
        page_ids = {p.frontmatter.id for p in pages}

        # Build set of all link targets
        all_link_targets = self._extract_all_links(pages)
        all_existing_ids = page_ids

        # Track seen IDs for duplicate detection
        seen_ids: Dict[str, str] = {}

        for page in pages:
            filepath = self.engine._find_page_file(page.frontmatter.id)

            # Check frontmatter
            self._check_frontmatter(page, str(filepath) if filepath else None)

            # Check for duplicates
            self._check_duplicates(page, seen_ids)
            seen_ids[page.frontmatter.id] = str(filepath) if filepath else ""

            # Check for broken links
            self._check_broken_links(page, all_existing_ids)

            # Check for tags
            self._check_tags(page)

            # Check for outdated
            self._check_outdated(page)

        # Check for orphan pages
        for page in pages:
            self._check_orphan(page, all_link_targets)

        # Fix issues if requested
        fixed = []
        if fix:
            fixed = self._auto_fix_issues()

        # Count by severity
        errors = len([i for i in self.issues if i.severity == LintSeverity.ERROR])
        warnings = len([i for i in self.issues if i.severity == LintSeverity.WARNING])
        info_count = len([i for i in self.issues if i.severity == LintSeverity.INFO])

        return {
            "total_issues": len(self.issues),
            "errors": errors,
            "warnings": warnings,
            "info": info_count,
            "issues": [i.to_dict() for i in self.issues],
            "fixed": fixed,
        }

    def _check_frontmatter(self, page: Any, filepath: Optional[str]) -> None:
        """Check frontmatter validity."""
        fm = page.frontmatter

        # Required fields
        if not fm.id:
            self.issues.append(LintIssue(
                page_id="unknown",
                severity=LintSeverity.ERROR,
                code="W001",
                message="Missing required field: id",
                filepath=filepath,
            ))

        if not fm.title:
            self.issues.append(LintIssue(
                page_id=fm.id or "unknown",
                severity=LintSeverity.ERROR,
                code="W001",
                message="Missing required field: title",
                filepath=filepath,
            ))

        if not fm.type:
            self.issues.append(LintIssue(
                page_id=fm.id or "unknown",
                severity=LintSeverity.ERROR,
                code="W001",
                message="Missing required field: type",
                filepath=filepath,
            ))

        # Validate type value
        valid_types = {"concept", "entity", "source", "comparison"}
        if fm.type and fm.type not in valid_types:
            self.issues.append(LintIssue(
                page_id=fm.id,
                severity=LintSeverity.ERROR,
                code="W002",
                message=f"Invalid type value: {fm.type}. Must be one of {valid_types}",
                suggestion=f"Change type to one of: {', '.join(valid_types)}",
                filepath=filepath,
            ))

        # Validate status value
        valid_statuses = {"draft", "review", "published"}
        if fm.status and fm.status not in valid_statuses:
            self.issues.append(LintIssue(
                page_id=fm.id,
                severity=LintSeverity.ERROR,
                code="W002",
                message=f"Invalid status value: {fm.status}. Must be one of {valid_statuses}",
                suggestion=f"Change status to one of: {', '.join(valid_statuses)}",
                filepath=filepath,
            ))

    def _check_duplicates(self, page: Any, seen_ids: Dict[str, str]) -> None:
        """Check for duplicate page IDs."""
        page_id = page.frontmatter.id
        if page_id in seen_ids:
            filepath = self.engine._find_page_file(page_id)
            self.issues.append(LintIssue(
                page_id=page_id,
                severity=LintSeverity.ERROR,
                code="W006",
                message=f"Duplicate page ID: {page_id} (first occurrence: {seen_ids[page_id]})",
                suggestion="Rename one of the pages to have a unique ID",
                filepath=str(filepath) if filepath else None,
            ))

    def _check_broken_links(self, page: Any, existing_ids: Set[str]) -> None:
        """Check for broken wiki links."""
        links = page.extract_links()

        for link_id in links:
            if link_id not in existing_ids:
                filepath = self.engine._find_page_file(page.frontmatter.id)
                self.issues.append(LintIssue(
                    page_id=page.frontmatter.id,
                    severity=LintSeverity.WARNING,
                    code="W004",
                    message=f"Broken link: [[{link_id}]]",
                    suggestion=f"Create page {link_id} or fix the link",
                    filepath=str(filepath) if filepath else None,
                ))

    def _check_orphan(self, page: Any, all_links: Set[str]) -> None:
        """Check if page is orphan (no incoming links)."""
        page_id = page.frontmatter.id

        # Skip source pages and index/log (they can be orphans)
        if page.frontmatter.type == "source":
            return
        if page_id in ("index", "log"):
            return

        if page_id not in all_links:
            filepath = self.engine._find_page_file(page_id)
            self.issues.append(LintIssue(
                page_id=page_id,
                severity=LintSeverity.INFO,
                code="W003",
                message="Orphan page: no incoming links",
                suggestion="Add links from related pages",
                filepath=str(filepath) if filepath else None,
            ))

    def _check_tags(self, page: Any) -> None:
        """Check for recommended tags."""
        # Entities should have at least one tag
        if page.frontmatter.type == "entity" and not page.frontmatter.tags:
            filepath = self.engine._find_page_file(page.frontmatter.id)
            self.issues.append(LintIssue(
                page_id=page.frontmatter.id,
                severity=LintSeverity.WARNING,
                code="W008",
                message="Entity page has no tags",
                suggestion="Add relevant tags for better discoverability",
                filepath=str(filepath) if filepath else None,
            ))

    def _check_outdated(self, page: Any) -> None:
        """Check if page hasn't been updated in a while."""
        threshold = datetime.now() - timedelta(days=30)

        # Check created_at if no updated_at
        check_date = page.frontmatter.updated_at or page.frontmatter.created_at

        if check_date and check_date < threshold:
            filepath = self.engine._find_page_file(page.frontmatter.id)
            self.issues.append(LintIssue(
                page_id=page.frontmatter.id,
                severity=LintSeverity.INFO,
                code="W007",
                message=f"Outdated page: not updated since {check_date.strftime('%Y-%m-%d')}",
                suggestion="Review and update if necessary",
                filepath=str(filepath) if filepath else None,
            ))

    def _extract_all_links(self, pages: List[Any]) -> Set[str]:
        """Extract all link targets from all pages."""
        all_links: Set[str] = set()

        for page in pages:
            links = page.extract_links()
            all_links.update(links)

        return all_links

    def _auto_fix_issues(self) -> List[str]:
        """
        Auto-fix issues where possible.

        Currently supports:
        - Setting missing created_at dates

        Returns:
            List of fix descriptions
        """
        fixed = []

        for issue in self.issues:
            # Auto-fix missing created_at
            if issue.code == "W001" and "created_at" in issue.message:
                page = self.engine.get_page(issue.page_id)
                if page:
                    page.frontmatter.created_at = datetime.now()
                    self.engine.save_page(page)
                    fixed.append(f"Set created_at for {issue.page_id}")

        return fixed

    def check_specific_page(self, page_id: str) -> List[Dict[str, Any]]:
        """
        Run lint checks on a specific page only.

        Args:
            page_id: Page ID to check

        Returns:
            List of issues for this page
        """
        page = self.engine.get_page(page_id)
        if page is None:
            return [{
                "page_id": page_id,
                "severity": "error",
                "code": "W000",
                "message": f"Page not found: {page_id}",
            }]

        self.issues = []
        filepath = self.engine._find_page_file(page_id)

        self._check_frontmatter(page, str(filepath) if filepath else None)
        self._check_tags(page)
        self._check_outdated(page)

        # Check links
        all_pages = self.engine.list_pages()
        all_ids = {p.frontmatter.id for p in all_pages}
        self._check_broken_links(page, all_ids)

        return [i.to_dict() for i in self.issues]
