import os

import pytest


CONFIG_ENV_KEYS = [
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
    "OPENAI_API_KEY",
    "OPENAI_BASE_URL",
    "OPENAI_ORG_ID",
    "OPENAI_PROJECT",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_BASE_URL",
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


def _clear_config_env() -> None:
    for key in CONFIG_ENV_KEYS:
        os.environ.pop(key, None)


os.environ["QUANTUMATLAS_SKIP_DOTENV"] = "1"
_clear_config_env()


@pytest.fixture(autouse=True)
def isolate_project_env(request, monkeypatch):
    """Keep ordinary tests independent from the developer's repository .env."""
    if request.node.get_closest_marker("user_env"):
        monkeypatch.delenv("QUANTUMATLAS_SKIP_DOTENV", raising=False)
        yield
    else:
        monkeypatch.setenv("QUANTUMATLAS_SKIP_DOTENV", "1")
        _clear_config_env()
        yield

    _clear_config_env()
    os.environ["QUANTUMATLAS_SKIP_DOTENV"] = "1"
