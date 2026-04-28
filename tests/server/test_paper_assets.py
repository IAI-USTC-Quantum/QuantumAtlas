import os
from pathlib import Path

from fastapi.testclient import TestClient

from atlas.paper_assets import resolve_paper_assets, safe_paper_key
from atlas.server.config import ServerConfig
from atlas.server.main import create_app


def _write(path: Path, data: str | bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if isinstance(data, bytes):
        path.write_bytes(data)
    else:
        path.write_text(data, encoding="utf-8")


def test_resolve_paper_assets_uses_raw_root(tmp_path):
    raw_root = tmp_path / "team" / "raw"
    key = safe_paper_key("quant-ph/9508027v2")

    _write(raw_root / "pdf" / f"{key}.pdf", b"%PDF-1.4")
    _write(raw_root / "markdown" / f"{key}.md", "# parsed")
    _write(raw_root / "json" / f"{key}.json", "{}")
    _write(raw_root / "images" / key / "figure-1.png", b"png")

    resolved = resolve_paper_assets(raw_root, "quant-ph/9508027v2")

    assert resolved["key"] == key
    assert resolved["pdf_path"] == raw_root / "pdf" / f"{key}.pdf"
    assert resolved["markdown_path"] == raw_root / "markdown" / f"{key}.md"
    assert resolved["json_path"] == raw_root / "json" / f"{key}.json"
    assert resolved["images_dir"] == raw_root / "images" / key


def test_resolve_paper_assets_finds_single_versioned_old_style_asset(tmp_path):
    raw_root = tmp_path / "raw"
    key = safe_paper_key("quant-ph/9508027v2")

    _write(raw_root / "pdf" / f"{key}.pdf", b"%PDF-1.4")
    _write(raw_root / "markdown" / f"{key}.md", "# parsed")
    _write(raw_root / "json" / f"{key}.json", "{}")

    resolved = resolve_paper_assets(raw_root, "quant-ph/9508027")

    assert resolved["key"] == key
    assert resolved["pdf_path"] == raw_root / "pdf" / f"{key}.pdf"
    assert resolved["markdown_path"] == raw_root / "markdown" / f"{key}.md"
    assert resolved["json_path"] == raw_root / "json" / f"{key}.json"


def test_paper_resources_route_uses_configured_raw_root(tmp_path):
    wiki_root = tmp_path / "wiki"
    data_root = tmp_path / "data"
    raw_root = tmp_path / "mounted" / "raw"
    key = safe_paper_key("9508027v1")

    _write(raw_root / "pdf" / f"{key}.pdf", b"%PDF-1.4 test")
    _write(raw_root / "markdown" / f"{key}.md", "# parsed")
    _write(raw_root / "json" / f"{key}.json", '{"arxiv_id": "9508027v1"}')
    _write(raw_root / "images" / key / "figure-1.png", b"png")

    app = create_app(
        ServerConfig(
            wiki_dir=str(wiki_root),
            raw_dir=str(raw_root),
            data_dir=str(data_root),
        )
    )

    with TestClient(app) as client:
        response = client.get('/api/papers/9508027v1/resources')
        assert response.status_code == 200
        data = response.json()
        assert data['assets']['pdf']['exists'] is True
        assert data['assets']['pdf']['url'].startswith('/share/')
        assert data['assets']['markdown']['url'].startswith('/share/')
        assert data['assets']['json']['url'].startswith('/share/')
        assert len(data['images']) == 1
        shared_pdf = client.get(data['assets']['pdf']['url'])
        assert shared_pdf.status_code == 200


def test_paper_resources_can_use_permanent_share_token_and_public_base_url(tmp_path):
    wiki_root = tmp_path / "wiki"
    data_root = tmp_path / "data"
    raw_root = tmp_path / "mounted" / "raw"
    key = safe_paper_key("9508027v1")

    _write(raw_root / "pdf" / f"{key}.pdf", b"%PDF-1.4 test")
    _write(raw_root / "markdown" / f"{key}.md", "# parsed")
    _write(raw_root / "json" / f"{key}.json", '{"arxiv_id": "9508027v1"}')

    app = create_app(
        ServerConfig(
            wiki_dir=str(wiki_root),
            raw_dir=str(raw_root),
            data_dir=str(data_root),
            public_base_url="https://atlas.example",
            share_access_token="permanent-token",
        )
    )

    with TestClient(app) as client:
        response = client.get("/api/papers/9508027v1/resources")
        assert response.status_code == 200
        data = response.json()
        assert (
            data["assets"]["pdf"]["url"]
            == "https://atlas.example/share/permanent-token/papers/pdf/9508027v1.pdf"
        )
        shared_pdf = client.get("/share/permanent-token/papers/pdf/9508027v1.pdf")
        assert shared_pdf.status_code == 200
        assert list((data_root / "shares").glob("*.json")) == []


def test_server_config_reads_raw_dir_from_dotenv(tmp_path, monkeypatch):
    env_path = tmp_path / '.env'
    env_path.write_text('RAW_DIR=/tmp/quantum-atlas-raw\n', encoding='utf-8')

    monkeypatch.delenv('QUANTUMATLAS_SKIP_DOTENV', raising=False)
    monkeypatch.delenv('RAW_DIR', raising=False)
    monkeypatch.setattr('atlas.server.config.get_project_root', lambda: tmp_path)

    from atlas.server.config import ServerConfig

    config = ServerConfig.from_env()
    assert config.raw_dir == '/tmp/quantum-atlas-raw'


def test_server_config_defaults_user_header_to_none(tmp_path, monkeypatch):
    env_path = tmp_path / '.env'
    env_path.write_text('', encoding='utf-8')

    monkeypatch.delenv('QUANTUMATLAS_SKIP_DOTENV', raising=False)
    monkeypatch.delenv('USER_HEADER', raising=False)
    monkeypatch.setattr('atlas.server.config.get_project_root', lambda: tmp_path)

    from atlas.server.config import ServerConfig

    config = ServerConfig.from_env()
    assert config.user_header is None
