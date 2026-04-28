"""
Tests for FastAPI Server

Tests the web server routes and API endpoints.
"""

import json
import subprocess
from pathlib import Path
from unittest.mock import MagicMock

import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def mock_wiki_engine():
    """Mock WikiEngine for testing."""
    engine = MagicMock()
    engine.get_stats.return_value = {
        "total_pages": 10,
        "by_type": {"concept": 5, "entity": 3, "source": 2},
        "by_status": {"published": 8, "draft": 2},
        "by_category": {"primitive": 4, "algorithm": 4, "paper": 2},
    }
    engine.list_pages.return_value = []
    engine.querier.get_recent_pages.return_value = [
        {"id": "test-page", "title": "Test Page", "type": "concept"}
    ]
    return engine


@pytest.fixture
def client(tmp_path):
    """Create test client (runs ASGI lifespan so stores are initialized)."""
    from atlas.server.config import ServerConfig
    from atlas.server.main import create_app

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
    )
    with TestClient(create_app(config)) as test_client:
        yield test_client


def git(cwd: Path, *args: str) -> subprocess.CompletedProcess[str]:
    """Run git in tests and fail with useful output."""
    result = subprocess.run(
        ["git", *args],
        cwd=str(cwd),
        capture_output=True,
        check=False,
        text=True,
    )
    assert result.returncode == 0, result.stderr or result.stdout
    return result


def commit_all(repo: Path, message: str) -> None:
    git(repo, "add", ".")
    git(
        repo,
        "-c",
        "user.email=test@example.com",
        "-c",
        "user.name=Test User",
        "commit",
        "-m",
        message,
    )


class TestHomePage:
    """Tests for the home page."""

    def test_home_page_loads(self, client):
        """Test that home page returns 200."""
        response = client.get("/")
        assert response.status_code == 200

    def test_home_page_contains_title(self, client):
        """Test that home page contains QuantumAtlas."""
        response = client.get("/")
        assert b"QuantumAtlas" in response.content

    def test_health_check(self, client):
        """Test health endpoint."""
        response = client.get("/health")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "healthy"


class TestWikiRoutes:
    """Tests for Wiki routes."""

    def test_wiki_home(self, client):
        """Test wiki home page."""
        response = client.get("/wiki/")
        assert response.status_code == 200

    def test_wiki_search_page(self, client):
        """Test wiki search page loads."""
        response = client.get("/wiki/search")
        assert response.status_code == 200

    def test_wiki_search_with_query(self, client):
        """Test wiki search with query parameter."""
        response = client.get("/wiki/search?q=test")
        assert response.status_code == 200

    def test_wiki_write_forms_are_not_exposed(self, client):
        """Wiki server pages are read-only; edits go through the Git workflow."""
        assert client.get("/wiki/new").status_code == 404
        assert client.post("/wiki/new").status_code == 404
        assert client.get("/wiki/edit/test-page").status_code == 404
        assert client.post("/wiki/edit/test-page").status_code == 404


class TestGraphRoutes:
    """Tests for Graph routes."""

    def test_graph_home(self, client):
        """Test graph explorer home page."""
        response = client.get("/graph/")
        assert response.status_code == 200


class TestAPIRoutes:
    """Tests for REST API routes."""

    def test_api_docs(self, client):
        """Test API docs endpoint."""
        response = client.get("/api/docs")
        assert response.status_code == 200

    def test_api_stats(self, client):
        """Test API stats endpoint."""
        response = client.get("/api/stats")
        assert response.status_code == 200
        data = response.json()
        assert "total_pages" in data

    def test_api_pages_list(self, client):
        """Test API pages list endpoint."""
        response = client.get("/api/pages")
        assert response.status_code == 200
        data = response.json()
        assert "total" in data
        assert "pages" in data

    def test_api_search(self, client):
        """Test API search endpoint."""
        response = client.get("/api/search?q=test")
        assert response.status_code == 200
        data = response.json()
        assert "query" in data
        assert "results" in data

    def test_api_lint(self, client):
        """Test API lint endpoint."""
        response = client.get("/api/lint")
        assert response.status_code == 200

    def test_download_api_is_not_exposed(self, client):
        """Downloads are handled as the fetch stage of ingest, not as a standalone API."""
        response = client.get("/api/downloads")
        assert response.status_code == 404

    def test_api_server_info_is_safe_summary(self, client, tmp_path):
        """Test server info endpoint does not expose local filesystem paths."""
        response = client.get("/api/server/info")
        assert response.status_code == 200
        data = response.json()

        assert data["mode"] == "server"
        from atlas import __version__

        assert data["version"] == __version__
        assert set(data) == {"mode", "version", "code", "wiki", "assets", "audit"}
        assert data["code"]["tag"] == f"v{__version__}"
        assert data["code"]["require_release_tag"] is False
        assert set(data["code"]["git"]) >= {"enabled"}
        assert set(data["wiki"]) == {"exists", "external", "git"}
        assert "enabled" in data["wiki"]["git"]
        assert data["assets"] == {
            "public_base_url": None,
            "share_access_token_enabled": False,
        }
        assert data["audit"] == {"user_header_enabled": False}
        assert str(tmp_path) not in response.text

    def test_server_startup_writes_code_version_manifests(self, tmp_path):
        """Test raw and data stores include the serving code version."""
        from atlas import __version__
        from atlas.runtime_metadata import MANIFEST_FILENAME
        from atlas.server.config import ServerConfig
        from atlas.server.main import create_app

        config = ServerConfig(
            wiki_dir=str(tmp_path / "wiki"),
            raw_dir=str(tmp_path / "raw"),
            data_dir=str(tmp_path / "data"),
        )

        with TestClient(create_app(config)):
            pass

        for root_name in ["raw", "data"]:
            manifest = tmp_path / root_name / MANIFEST_FILENAME
            payload = json.loads(manifest.read_text(encoding="utf-8"))
            assert payload["project"] == "quantumatlas"
            assert payload["version"] == __version__
            assert payload["tag"] == f"v{__version__}"
            assert payload["schema_version"] == 1

    def test_wiki_sync_status_is_safe_summary(self, tmp_path):
        """Test wiki sync status reports Git state without exposing paths."""
        from atlas.server.config import ServerConfig
        from atlas.server.main import create_app

        wiki_dir = tmp_path / "wiki"
        wiki_dir.mkdir()
        git(wiki_dir, "init")
        (wiki_dir / "index.md").write_text("# Wiki\n", encoding="utf-8")
        commit_all(wiki_dir, "initial wiki")

        config = ServerConfig(
            wiki_dir=str(wiki_dir),
            raw_dir=str(tmp_path / "raw"),
            data_dir=str(tmp_path / "data"),
        )

        with TestClient(create_app(config)) as test_client:
            response = test_client.get("/api/wiki/sync/status")

        assert response.status_code == 200
        data = response.json()
        assert data["wiki"] == {"exists": True, "external": True}
        assert data["git"]["enabled"] is True
        assert data["git"]["commit"]
        assert data["git"]["dirty"] is False
        assert data["git"]["warnings"] == []
        assert str(tmp_path) not in response.text

    def test_wiki_sync_status_warns_on_non_main_branch(self, tmp_path):
        """Test wiki sync status warns when the server checkout is not main/master."""
        from atlas.server.config import ServerConfig
        from atlas.server.main import create_app

        wiki_dir = tmp_path / "wiki"
        wiki_dir.mkdir()
        git(wiki_dir, "init")
        (wiki_dir / "index.md").write_text("# Wiki\n", encoding="utf-8")
        commit_all(wiki_dir, "initial wiki")
        git(wiki_dir, "checkout", "-b", "staging")

        config = ServerConfig(
            wiki_dir=str(wiki_dir),
            raw_dir=str(tmp_path / "raw"),
            data_dir=str(tmp_path / "data"),
        )

        with TestClient(create_app(config)) as test_client:
            response = test_client.get("/api/wiki/sync/status")

        assert response.status_code == 200
        warnings = response.json()["git"]["warnings"]
        assert warnings == [
            {
                "code": "wiki_branch_not_main",
                "message": "Wiki repo is not checked out on main or master.",
                "branch": "staging",
            }
        ]

    def test_wiki_sync_pull_rejects_dirty_worktree(self, tmp_path):
        """Test wiki sync pull refuses local changes."""
        from atlas.server.config import ServerConfig
        from atlas.server.main import create_app

        wiki_dir = tmp_path / "wiki"
        wiki_dir.mkdir()
        git(wiki_dir, "init")
        (wiki_dir / "index.md").write_text("# Wiki\n", encoding="utf-8")
        commit_all(wiki_dir, "initial wiki")
        (wiki_dir / "index.md").write_text("# Local edit\n", encoding="utf-8")

        config = ServerConfig(
            wiki_dir=str(wiki_dir),
            raw_dir=str(tmp_path / "raw"),
            data_dir=str(tmp_path / "data"),
        )

        with TestClient(create_app(config)) as test_client:
            response = test_client.post("/api/wiki/sync/pull")

        assert response.status_code == 409
        assert response.json()["detail"] == "wiki worktree has local changes"

    def test_wiki_sync_pull_fast_forwards(self, tmp_path):
        """Test wiki sync pull fetches and fast-forwards the configured Wiki repo."""
        from atlas.server.config import ServerConfig
        from atlas.server.main import create_app

        remote = tmp_path / "remote.git"
        seed = tmp_path / "seed"
        wiki_dir = tmp_path / "wiki"
        remote.mkdir()
        seed.mkdir()
        git(remote, "init", "--bare")
        git(seed, "init")
        git(seed, "remote", "add", "origin", str(remote))
        (seed / "index.md").write_text("# Wiki\n", encoding="utf-8")
        commit_all(seed, "initial wiki")
        git(seed, "push", "-u", "origin", "HEAD:main")
        git(tmp_path, "clone", str(remote), str(wiki_dir))
        git(wiki_dir, "checkout", "main")

        (seed / "page.md").write_text("# New page\n", encoding="utf-8")
        commit_all(seed, "add page")
        git(seed, "push", "origin", "HEAD:main")

        config = ServerConfig(
            wiki_dir=str(wiki_dir),
            raw_dir=str(tmp_path / "raw"),
            data_dir=str(tmp_path / "data"),
        )

        with TestClient(create_app(config)) as test_client:
            response = test_client.post("/api/wiki/sync/pull")

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "succeeded"
        assert data["changed"] is True
        assert data["old_commit"] != data["new_commit"]
        assert data["git"]["dirty"] is False
        assert (wiki_dir / "page.md").read_text(encoding="utf-8") == "# New page\n"


class TestPageNotFound:
    """Tests for 404 handling."""

    def test_wiki_page_deep_link_serves_web_shell(self, client):
        """Wiki deep links are handled by the web shell."""
        response = client.get("/wiki/page/non-existent-page-12345")
        assert response.status_code == 200
        assert b"QuantumAtlas" in response.content

    def test_missing_wiki_page_api_returns_404(self, client):
        """The JSON API still reports missing wiki pages as 404."""
        response = client.get("/api/pages/non-existent-page-12345")
        assert response.status_code == 404
