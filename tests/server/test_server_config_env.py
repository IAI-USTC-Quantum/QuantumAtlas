from pathlib import Path

import pytest

from atlas.server.config import ServerConfig

ENV_KEYS = [
    # New QATLAS_* names
    "QATLAS_SERVER_HOST",
    "QATLAS_SERVER_PORT",
    "QATLAS_SERVER_DEBUG",
    "QATLAS_WIKI_DIR",
    "QATLAS_RAW_DIR",
    "QATLAS_DATA_DIR",
    "QATLAS_SERVER_URL",
    "QATLAS_INSECURE",
    "QATLAS_SHARE_ACCESS_TOKEN",
    "QATLAS_DEFAULT_SHARE_EXPIRES_IN",
    "QATLAS_USER_HEADER",
    "QATLAS_REQUIRE_RELEASE_TAG",
    # Legacy bare aliases
    "SERVER_HOST",
    "SERVER_PORT",
    "SERVER_DEBUG",
    "NEO4J_URI",
    "NEO4J_USER",
    "NEO4J_PASSWORD",
    "WIKI_DIR",
    "RAW_DIR",
    "DATA_DIR",
    "PUBLIC_BASE_URL",
    "SHARE_ACCESS_TOKEN",
    "PUBLIC_SHARE_TOKEN",
    "DEFAULT_SHARE_EXPIRES_IN",
    "USER_HEADER",
    "QUANTUMATLAS_REQUIRE_RELEASE_TAG",
    "REQUIRE_RELEASE_TAG",
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "MINERU_API_TOKEN",
    "MINERU_API_BASE_URL",
    "MINERU_MODEL_VERSION",
    "MINERU_LANGUAGE",
    "MINERU_IS_OCR",
    "MINERU_ENABLE_FORMULA",
    "MINERU_ENABLE_TABLE",
    "MINERU_POLL_INTERVAL",
    "MINERU_TIMEOUT",
]


def _reset_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("QATLAS_SKIP_DOTENV", raising=False)
    monkeypatch.delenv("QUANTUMATLAS_SKIP_DOTENV", raising=False)
    for key in ENV_KEYS:
        monkeypatch.delenv(key, raising=False)


def _write_env(tmp_path: Path, content: str) -> None:
    (tmp_path / ".env").write_text(content, encoding="utf-8")


def test_server_config_reads_all_supported_env_vars_from_dotenv(tmp_path, monkeypatch):
    _reset_env(monkeypatch)
    _write_env(
        tmp_path,
        "\n".join(
            [
                "SERVER_HOST=0.0.0.0",
                "SERVER_PORT=9000",
                "SERVER_DEBUG=true",
                "NEO4J_URI=bolt://neo4j.internal:7687",
                "NEO4J_USER=graph-user",
                "NEO4J_PASSWORD=top-secret",
                "WIKI_DIR=/srv/wiki",
                "RAW_DIR=/srv/raw",
                "DATA_DIR=/srv/data",
                "PUBLIC_BASE_URL=https://atlas.example",
                "SHARE_ACCESS_TOKEN=share-secret",
                "DEFAULT_SHARE_EXPIRES_IN=3600",
                "USER_HEADER=X-From-Dotenv",
                "QUANTUMATLAS_REQUIRE_RELEASE_TAG=true",
                "OPENAI_API_KEY=openai-key",
                "ANTHROPIC_API_KEY=anthropic-key",
                "MINERU_API_TOKEN=mineru-token",
                "MINERU_API_BASE_URL=https://mineru.example",
                "MINERU_MODEL_VERSION=vlm",
                "MINERU_LANGUAGE=en",
                "MINERU_IS_OCR=true",
                "MINERU_ENABLE_FORMULA=false",
                "MINERU_ENABLE_TABLE=false",
                "MINERU_POLL_INTERVAL=1.5",
                "MINERU_TIMEOUT=900",
            ]
        )
        + "\n",
    )
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)

    config = ServerConfig.from_env()

    assert config.host == "0.0.0.0"
    assert config.port == 9000
    assert config.debug is True
    assert config.neo4j_uri == "bolt://neo4j.internal:7687"
    assert config.neo4j_user == "graph-user"
    assert config.neo4j_password == "top-secret"
    assert config.wiki_dir == "/srv/wiki"
    assert config.raw_dir == "/srv/raw"
    assert config.data_dir == "/srv/data"
    assert config.public_base_url == "https://atlas.example"
    assert config.get_public_base_url() == "https://atlas.example"
    assert config.share_access_token == "share-secret"
    assert config.default_share_expires_in == 3600
    assert config.user_header == "X-From-Dotenv"
    assert config.require_release_tag is True
    assert config.openai_api_key == "openai-key"
    assert config.anthropic_api_key == "anthropic-key"
    assert config.mineru_api_token == "mineru-token"
    assert config.mineru_api_base_url == "https://mineru.example"
    assert config.mineru_model_version == "vlm"
    assert config.mineru_language == "en"
    assert config.mineru_is_ocr is True
    assert config.mineru_enable_formula is False
    assert config.mineru_enable_table is False
    assert config.mineru_poll_interval == 1.5
    assert config.mineru_timeout == 900


def test_server_config_prefers_process_env_over_dotenv(tmp_path, monkeypatch):
    _reset_env(monkeypatch)
    _write_env(
        tmp_path,
        "\n".join(
            [
                "SERVER_PORT=9000",
                "RAW_DIR=/from-dotenv",
                "USER_HEADER=X-From-Dotenv",
            ]
        )
        + "\n",
    )
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)
    monkeypatch.setenv("SERVER_PORT", "9100")
    monkeypatch.setenv("RAW_DIR", "/from-env")
    monkeypatch.setenv("USER_HEADER", "X-From-Env")

    config = ServerConfig.from_env()

    assert config.port == 9100
    assert config.raw_dir == "/from-env"
    assert config.user_header == "X-From-Env"


def test_server_config_strips_quoted_dotenv_values(tmp_path, monkeypatch):
    _reset_env(monkeypatch)
    _write_env(
        tmp_path,
        "\n".join(
            [
                'WIKI_DIR="team wiki"',
                "RAW_DIR='team raw'",
                'USER_HEADER="X-Token-User-Name"',
            ]
        )
        + "\n",
    )
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)

    config = ServerConfig.from_env()

    assert config.wiki_dir == "team wiki"
    assert config.raw_dir == "team raw"
    assert config.user_header == "X-Token-User-Name"


def test_server_config_uses_standard_dotenv_syntax(tmp_path, monkeypatch):
    _reset_env(monkeypatch)
    _write_env(
        tmp_path,
        "\n".join(
            [
                "export SERVER_PORT=9300",
                "RAW_DIR=/srv/raw # inline comment",
                "WIKI_DIR=${RAW_DIR}/wiki",
            ]
        )
        + "\n",
    )
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)

    config = ServerConfig.from_env()

    assert config.port == 9300
    assert config.raw_dir == "/srv/raw"
    assert config.wiki_dir == "/srv/raw/wiki"


@pytest.mark.parametrize(
    ("env_value", "expected"),
    [
        ("true", True),
        ("TRUE", True),
        ("false", False),
        ("1", False),
        ("yes", False),
    ],
)
def test_server_config_boolean_env_parsing(tmp_path, monkeypatch, env_value, expected):
    _reset_env(monkeypatch)
    _write_env(
        tmp_path,
        f"SERVER_DEBUG={env_value}\nMINERU_IS_OCR={env_value}\n"
        f"QUANTUMATLAS_REQUIRE_RELEASE_TAG={env_value}\n",
    )
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)

    config = ServerConfig.from_env()

    assert config.debug is expected
    assert config.mineru_is_ocr is expected
    assert config.require_release_tag is expected


def test_server_config_get_raw_root_resolves_relative_to_project_root(tmp_path, monkeypatch):
    _reset_env(monkeypatch)
    _write_env(tmp_path, "RAW_DIR=shared/raw\n")
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)

    config = ServerConfig.from_env()

    assert config.get_raw_root() == (tmp_path / "shared" / "raw").resolve()
    assert (
        config.get_paper_asset_dir("markdown")
        == (tmp_path / "shared" / "raw" / "markdown").resolve()
    )


def test_server_config_rejects_unknown_paper_asset_kind():
    config = ServerConfig(raw_dir="/tmp/raw")

    with pytest.raises(ValueError, match="unknown paper asset kind: audio"):
        config.get_paper_asset_dir("audio")
