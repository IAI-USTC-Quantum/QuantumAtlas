"""
Tests for WikiEngine.
"""

import json
import pytest
import tempfile
from pathlib import Path

from qatlas.wiki.engine import WikiEngine, WikiWriteDisabledError
from qatlas.wiki.page import WikiPage, WikiFrontmatter


class TestWikiEngine:
    """Tests for WikiEngine class."""

    @pytest.fixture
    def temp_wiki(self, tmp_path):
        """Create a temporary wiki directory for testing."""
        wiki_dir = tmp_path / "wiki"
        raw_dir = tmp_path / "raw"

        engine = WikiEngine(
            wiki_dir=str(wiki_dir),
            raw_dir=str(raw_dir),
            project_root=str(tmp_path),
        )
        return engine

    def test_initialization(self, temp_wiki):
        """Test engine initialization."""
        assert temp_wiki.wiki_dir.exists()
        assert temp_wiki.raw_dir.exists()
        assert (temp_wiki.wiki_dir / "concepts").exists()
        assert (temp_wiki.wiki_dir / "entities" / "algorithms").exists()

    def test_content_guarded_engine_does_not_initialize_directories(self, tmp_path):
        """Content-guarded engines can inspect paths without creating a Wiki checkout."""
        engine = WikiEngine(
            wiki_dir=str(tmp_path / "wiki"),
            raw_dir=str(tmp_path / "raw"),
            project_root=str(tmp_path),
            ensure_directories=False,
            wiki_content_writable=False,
        )

        assert engine.wiki_content_writable is False
        assert not (tmp_path / "wiki").exists()
        assert not (tmp_path / "raw").exists()

    def test_content_guarded_engine_rejects_wiki_mutations(self, tmp_path):
        """Server-style engines fail loudly if code tries to mutate WIKI_DIR."""
        engine = WikiEngine(
            wiki_dir=str(tmp_path / "wiki"),
            raw_dir=str(tmp_path / "raw"),
            project_root=str(tmp_path),
            ensure_directories=False,
            wiki_content_writable=False,
        )
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="content-guarded",
                title="Content Guarded",
                type="concept",
            ),
            content="No writes.",
        )

        with pytest.raises(WikiWriteDisabledError):
            engine.save_page(page)
        with pytest.raises(WikiWriteDisabledError):
            engine.delete_page("content-guarded")
        with pytest.raises(WikiWriteDisabledError):
            engine.append_to_log("blocked")
        with pytest.raises(WikiWriteDisabledError):
            engine.update_index()

    def test_save_and_get_page(self, temp_wiki):
        """Test saving and retrieving a page."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="test-concept",
                title="Test Concept",
                type="concept",
                status="published",
            ),
            content="Test content.",
        )

        # Save
        path = temp_wiki.save_page(page)
        assert path.exists()

        # Retrieve
        retrieved = temp_wiki.get_page("test-concept")
        assert retrieved is not None
        assert retrieved.frontmatter.id == "test-concept"
        assert retrieved.content == "Test content."

    def test_list_pages(self, temp_wiki):
        """Test listing pages."""
        # Create multiple pages
        for i in range(3):
            page = WikiPage(
                frontmatter=WikiFrontmatter(
                    id=f"concept-{i}",
                    title=f"Concept {i}",
                    type="concept",
                    status="published",
                ),
                content=f"Content {i}",
            )
            temp_wiki.save_page(page)

        # Create an entity page
        entity = WikiPage(
            frontmatter=WikiFrontmatter(
                id="algo-test",
                title="Test Algorithm",
                type="entity",
                category="algorithm",
                status="published",
            ),
            content="Algorithm content.",
        )
        temp_wiki.save_page(entity)

        # List all
        all_pages = temp_wiki.list_pages()
        assert len(all_pages) == 4

        # Filter by type
        concepts = temp_wiki.list_pages(page_type="concept")
        assert len(concepts) == 3

        entities = temp_wiki.list_pages(page_type="entity")
        assert len(entities) == 1

    def test_delete_page(self, temp_wiki):
        """Test deleting a page."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="to-delete",
                title="To Delete",
                type="concept",
            ),
            content="Delete me.",
        )
        temp_wiki.save_page(page)

        # Verify exists
        assert temp_wiki.get_page("to-delete") is not None

        # Delete
        result = temp_wiki.delete_page("to-delete")
        assert result is True

        # Verify gone
        assert temp_wiki.get_page("to-delete") is None

    def test_update_page(self, temp_wiki):
        """Test updating a page."""
        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="to-update",
                title="Original Title",
                type="concept",
            ),
            content="Original content.",
        )
        temp_wiki.save_page(page)

        # Update
        updated = temp_wiki.update_page(
            "to-update",
            content="New content.",
            title="New Title",
        )

        assert updated is not None
        assert updated.content == "New content."
        assert updated.frontmatter.title == "New Title"
        assert updated.frontmatter.updated_at is not None

    def test_get_stats(self, temp_wiki):
        """Test getting wiki statistics."""
        # Create some pages
        for i in range(2):
            page = WikiPage(
                frontmatter=WikiFrontmatter(
                    id=f"concept-{i}",
                    title=f"Concept {i}",
                    type="concept",
                    status="published",
                ),
                content="Content",
            )
            temp_wiki.save_page(page)

        page = WikiPage(
            frontmatter=WikiFrontmatter(
                id="algo-test",
                title="Test Algorithm",
                type="entity",
                category="algorithm",
                status="draft",
            ),
            content="Content",
        )
        temp_wiki.save_page(page)

        stats = temp_wiki.get_stats()

        assert stats["total_pages"] == 3
        assert stats["by_type"]["concept"] == 2
        assert stats["by_type"]["entity"] == 1
        assert stats["by_status"]["published"] == 2
        assert stats["by_status"]["draft"] == 1
        assert stats["by_category"]["algorithm"] == 1

    def test_query(self, temp_wiki):
        """Test wiki search functionality."""
        # Create searchable pages
        page1 = WikiPage(
            frontmatter=WikiFrontmatter(
                id="qft-page",
                title="Quantum Fourier Transform",
                type="entity",
                category="primitive",
                tags=["fourier", "quantum"],
                status="published",
            ),
            content="The Quantum Fourier Transform is a fundamental operation.",
        )
        temp_wiki.save_page(page1)

        page2 = WikiPage(
            frontmatter=WikiFrontmatter(
                id="grover-page",
                title="Grover's Algorithm",
                type="entity",
                category="algorithm",
                tags=["search", "quantum"],
                status="published",
            ),
            content="Grover's algorithm provides quantum search.",
        )
        temp_wiki.save_page(page2)

        # Search
        results = temp_wiki.query("quantum fourier")

        assert len(results) >= 1
        # QFT should rank higher
        assert results[0]["id"] == "qft-page"


class TestWikiEngineQueries:
    """Tests for WikiEngine query functionality."""

    @pytest.fixture
    def populated_wiki(self, tmp_path):
        """Create a populated wiki for query testing."""
        engine = WikiEngine(
            wiki_dir=str(tmp_path / "wiki"),
            raw_dir=str(tmp_path / "raw"),
            project_root=str(tmp_path),
        )

        # Create linked pages
        prim = WikiPage(
            frontmatter=WikiFrontmatter(
                id="prim-qft",
                title="Quantum Fourier Transform",
                type="entity",
                category="primitive",
                status="published",
            ),
            content="A primitive for QFT.",
        )
        engine.save_page(prim)

        algo = WikiPage(
            frontmatter=WikiFrontmatter(
                id="algo-shors",
                title="Shor's Algorithm",
                type="entity",
                category="algorithm",
                related=["prim-qft"],
                status="published",
            ),
            content="Uses [[prim-qft]] for period finding.",
        )
        engine.save_page(algo)

        return engine

    def test_get_linked_pages(self, populated_wiki):
        """Test getting linked pages."""
        links = populated_wiki.querier.get_linked_pages("algo-shors")

        assert len(links) == 1
        assert links[0]["id"] == "prim-qft"

    def test_get_backlinks(self, populated_wiki):
        """Test getting backlinks."""
        backlinks = populated_wiki.querier.get_backlinks("prim-qft")

        assert len(backlinks) == 1
        assert backlinks[0]["id"] == "algo-shors"

    def test_find_by_tag(self, populated_wiki):
        """Test finding pages by tag."""
        # Add tags
        page = populated_wiki.get_page("prim-qft")
        page.frontmatter.tags = ["transformation", "fourier"]
        populated_wiki.save_page(page)

        results = populated_wiki.querier.find_by_tag("fourier")

        assert len(results) == 1
        assert results[0]["id"] == "prim-qft"


class TestWikiIngester:
    """Tests for WikiIngester asset resolution."""

    @pytest.fixture
    def temp_wiki(self, tmp_path):
        return WikiEngine(
            wiki_dir=str(tmp_path / "wiki"),
            raw_dir=str(tmp_path / "raw"),
            project_root=str(tmp_path),
        )

    def test_parse_pdf_now_raises(self, temp_wiki):
        """Local PDF parsing was removed from the open-source build.

        The previous test stubbed in a ``DummyParser`` via
        ``temp_wiki.ingester._pdf_parser = DummyParser()``; that hook no
        longer exists. We now assert ``_parse_pdf`` fails loudly so callers
        know to route through `qatlas mineru --push-pdf` or
        `qatlas upload mineru --zip <bundle>.zip`.
        """
        requested_id = "quant-ph/9508027"
        resolved_id = "quant-ph/9508027v2"
        pdf_path = temp_wiki.get_paper_asset_path("pdf", resolved_id)
        pdf_path.parent.mkdir(parents=True, exist_ok=True)
        pdf_path.write_bytes(b"%PDF-1.4")

        with pytest.raises(NotImplementedError, match="Local PDF parsing"):
            temp_wiki.ingester._parse_pdf(
                requested_id,
                {"arxiv_id": resolved_id, "title": "Shor"},
            )

    def test_parse_pdf_raises_even_with_explicit_pdf_path(self, temp_wiki):
        requested_id = "quant-ph/9508027"
        resolved_id = "quant-ph/9508027v2"
        pdf_path = temp_wiki.get_paper_asset_path("pdf", requested_id)
        pdf_path.parent.mkdir(parents=True, exist_ok=True)
        pdf_path.write_bytes(b"%PDF-1.4")

        with pytest.raises(NotImplementedError):
            temp_wiki.ingester._parse_pdf(
                requested_id,
                {"arxiv_id": resolved_id, "title": "Shor"},
                pdf_path=pdf_path,
            )

    def test_ingest_from_existing_finds_versioned_assets(self, temp_wiki):
        requested_id = "quant-ph/9508027"
        resolved_id = "quant-ph/9508027v2"
        json_path = temp_wiki.get_paper_asset_path("json", resolved_id)
        markdown_path = temp_wiki.get_paper_asset_path("markdown", resolved_id)
        json_path.parent.mkdir(parents=True, exist_ok=True)
        markdown_path.parent.mkdir(parents=True, exist_ok=True)
        json_path.write_text(
            json.dumps(
                {
                    "arxiv_id": resolved_id,
                    "title": "Polynomial-Time Algorithms for Prime Factorization",
                    "authors": ["Peter W. Shor"],
                    "abstract": "Test abstract",
                }
            ),
            encoding="utf-8",
        )
        markdown_path.write_text("# parsed", encoding="utf-8")

        result = temp_wiki.ingester.ingest_from_existing(requested_id)

        assert result["status"] == "success"
        assert result["wiki_pages"] == ["paper-arxiv-quant-ph-9508027"]
