"""
Tests for FastAPI Server

Tests the web server routes and API endpoints.
"""

import pytest
from unittest.mock import patch, MagicMock
from pathlib import Path

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
def client():
    """Create test client."""
    from atlas.server.main import create_app
    app = create_app()
    return TestClient(app)


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

    def test_wiki_new_page(self, client):
        """Test wiki new page form."""
        response = client.get("/wiki/new")
        assert response.status_code == 200


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


class TestPageNotFound:
    """Tests for 404 handling."""

    def test_wiki_page_not_found(self, client):
        """Test 404 for non-existent wiki page."""
        response = client.get("/wiki/page/non-existent-page-12345")
        assert response.status_code == 404
