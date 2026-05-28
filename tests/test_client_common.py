"""Unit tests for atlas.client._common helpers (token / auth_headers / etc.).

These run fast (no network, no subprocess). End-to-end tests against a live
server are in tests/integration/test_live_server.py and are skipped unless
QATLAS_E2E=1 is set.
"""

from __future__ import annotations

import argparse

import pytest

from atlas.client import _common


@pytest.fixture(autouse=True)
def _isolate_auth_store(monkeypatch, tmp_path):
    """Point XDG_CONFIG_HOME at a fresh empty dir for every test.

    Without this, ``resolve_token`` would fall through to whatever the
    developer left in ``~/.config/qatlas/hosts.yml`` after a real
    ``qatlas auth login``, making the "empty" / "env-fallback"
    assertions in this file false-pass or false-fail depending on the
    machine. The XDG fixture forces every test to see an empty store.
    """
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))


def _ns(**overrides):
    """Make an argparse.Namespace mimicking add_common_http_args output."""
    base = dict(base_url=None, insecure=False, token=None, request_timeout=120.0)
    base.update(overrides)
    return argparse.Namespace(**base)


def test_resolve_token_cli_flag_wins(monkeypatch):
    monkeypatch.setenv("QATLAS_TOKEN", "env-token")
    args = _ns(token="cli-flag-token")
    assert _common.resolve_token(args) == "cli-flag-token"


def test_resolve_token_env_fallback(monkeypatch):
    monkeypatch.setenv("QATLAS_TOKEN", "env-token")
    args = _ns(token=None)
    assert _common.resolve_token(args) == "env-token"


def test_resolve_token_empty(monkeypatch):
    monkeypatch.delenv("QATLAS_TOKEN", raising=False)
    args = _ns(token=None)
    assert _common.resolve_token(args) == ""


def test_resolve_token_trims_whitespace(monkeypatch):
    monkeypatch.setenv("QATLAS_TOKEN", "  padded-env  ")
    args = _ns(token=None)
    assert _common.resolve_token(args) == "padded-env"

    args = _ns(token="  padded-flag  ")
    assert _common.resolve_token(args) == "padded-flag"


def test_resolve_token_store_fallback(monkeypatch, tmp_path):
    """When --token and QATLAS_TOKEN are both absent, resolve_token
    must consult the per-host store populated by ``qatlas auth login``.

    Mirrors the ``gh`` CLI precedence: explicit flag > env > stored
    credentials > anonymous. Regression guard: do not let the
    integration drift back to "env-only".
    """
    monkeypatch.delenv("QATLAS_TOKEN", raising=False)
    monkeypatch.setenv("QATLAS_SERVER_URL", "https://quantum-atlas.ai")

    # Seed the store using auth.py's own writer so we exercise the
    # same path qatlas auth login uses (host normalisation, file
    # layout, etc.).
    from atlas.client import auth

    store = auth._load_store()
    store.setdefault("hosts", {})["quantum-atlas.ai"] = {
        "token": "qat_StoredAbCdEfGhIjKl",
        "added_at": "2026-05-28T00:00:00Z",
    }
    auth._save_store(store)

    args = _ns(token=None)
    assert _common.resolve_token(args) == "qat_StoredAbCdEfGhIjKl"


def test_resolve_token_env_beats_store(monkeypatch, tmp_path):
    """If both env and store have a value, env wins. Documented
    precedence — without this, users would be unable to override a
    stored token for one-off shell invocations.
    """
    monkeypatch.setenv("QATLAS_TOKEN", "env-wins")
    monkeypatch.setenv("QATLAS_SERVER_URL", "https://quantum-atlas.ai")

    from atlas.client import auth

    store = auth._load_store()
    store.setdefault("hosts", {})["quantum-atlas.ai"] = {
        "token": "qat_StoredShouldLose",
        "added_at": "2026-05-28T00:00:00Z",
    }
    auth._save_store(store)

    args = _ns(token=None)
    assert _common.resolve_token(args) == "env-wins"


def test_auth_headers_no_token(monkeypatch):
    monkeypatch.delenv("QATLAS_TOKEN", raising=False)
    args = _ns(token=None)
    # Empty dict so callers can splat {**auth_headers(args), ...} safely.
    assert _common.auth_headers(args) == {}


def test_auth_headers_bearer_format(monkeypatch):
    monkeypatch.delenv("QATLAS_TOKEN", raising=False)
    args = _ns(token="abc123")
    assert _common.auth_headers(args) == {"Authorization": "Bearer abc123"}


def test_add_common_http_args_registers_token_flag():
    parser = argparse.ArgumentParser()
    _common.add_common_http_args(parser)
    # Argparse normalizes --token to dest=token.
    parsed = parser.parse_args(["--token", "fixture-token"])
    assert parsed.token == "fixture-token"
    assert parsed.base_url is None
    assert parsed.insecure is False


def test_add_common_http_args_default_token_is_none():
    parser = argparse.ArgumentParser()
    _common.add_common_http_args(parser)
    parsed = parser.parse_args([])
    assert parsed.token is None


@pytest.mark.parametrize(
    "insecure_env,insecure_flag,expected_verify",
    [
        ("", False, True),
        ("1", False, False),
        ("true", False, False),
        ("0", False, True),
        ("", True, False),
    ],
)
def test_request_verify_precedence(monkeypatch, insecure_env, insecure_flag, expected_verify):
    """Regression coverage for the precedence in request_verify."""
    if insecure_env:
        monkeypatch.setenv("QATLAS_INSECURE", insecure_env)
    else:
        monkeypatch.delenv("QATLAS_INSECURE", raising=False)
    args = _ns(insecure=insecure_flag)
    assert _common.request_verify(args) is expected_verify
