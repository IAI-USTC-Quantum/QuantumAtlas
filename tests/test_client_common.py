"""Unit tests for qatlas.client._common (v0.17.0+: YAML-only).

v0.17.0 removed --base-url / --token / --insecure CLI flags and all
``QATLAS_*`` env-var reads. Values come exclusively from
``~/.config/qatlas/config.yaml``. ``--request-timeout`` is the only
flag ``add_common_http_args`` still registers.

The per-host ``hosts.yml`` store (populated by ``qatlas auth login``)
is the only token fallback when ``config.yaml`` has no ``token:``.
"""

from __future__ import annotations

import argparse
from pathlib import Path

import pytest

from qatlas import config_yaml
from qatlas.client import _common


@pytest.fixture(autouse=True)
def _isolate_xdg(monkeypatch, tmp_path: Path):
    """Point XDG_CONFIG_HOME + HOME at a fresh empty dir for every test.

    Both are needed because ``user_config_yaml_path()`` uses
    XDG_CONFIG_HOME directly while ``auth.config_dir()`` honours
    XDG_CONFIG_HOME but also reads HOME as a base for ``.config``.
    """
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    (tmp_path / "home").mkdir(exist_ok=True)
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg-config"))
    (tmp_path / "xdg-config").mkdir(exist_ok=True)
    # Tests must not leak QATLAS_* env into ServerConfig (env source is
    # disabled but we belt-and-suspenders here in case a contributor
    # re-enables it).
    for var in ("QATLAS_TOKEN", "QATLAS_SERVER_URL", "QATLAS_INSECURE"):
        monkeypatch.delenv(var, raising=False)


def _ns(**overrides):
    """Make an argparse.Namespace mimicking add_common_http_args output."""
    base = dict(request_timeout=120.0)
    base.update(overrides)
    return argparse.Namespace(**base)


def _seed_config(server_url=None, token=None, insecure=None):
    """Write a config.yaml under the isolated XDG dir."""
    from qatlas.paths import user_config_yaml_path

    data = {}
    if server_url is not None:
        data["server_url"] = server_url
    if token is not None:
        data["token"] = token
    if insecure is not None:
        data["insecure"] = insecure
    config_yaml.write_yaml_atomic(user_config_yaml_path(), data)


def test_resolve_token_from_yaml():
    _seed_config(token="qat_from_yaml_token")
    assert _common.resolve_token(_ns()) == "qat_from_yaml_token"


def test_resolve_token_yaml_trims_whitespace():
    _seed_config(token="  padded  ")
    assert _common.resolve_token(_ns()) == "padded"


def test_resolve_token_empty_when_yaml_unset():
    # Fresh isolated XDG, no config.yaml. Should return "" (no
    # Authorization), not crash, not look at env.
    assert _common.resolve_token(_ns()) == ""


def test_resolve_token_per_host_store_fallback():
    """When config.yaml has no token:, fall through to hosts.yml.

    Mirrors gh CLI precedence: explicit config > stored credentials
    > anonymous. Used for users who prefer `qatlas auth login`
    over editing the yaml directly.
    """
    _seed_config(server_url="https://quantum-atlas.ai")
    from qatlas.client import auth

    store = auth._load_store()
    store.setdefault("hosts", {})["quantum-atlas.ai"] = {
        "token": "qat_StoredAbCdEfGhIjKl",
        "added_at": "2026-05-28T00:00:00Z",
    }
    auth._save_store(store)

    assert _common.resolve_token(_ns()) == "qat_StoredAbCdEfGhIjKl"


def test_resolve_token_yaml_beats_store():
    """If both yaml and store have a value, yaml wins. Users editing
    the canonical config file expect it to take precedence over a
    stored auth-login token they may have forgotten about."""
    _seed_config(server_url="https://quantum-atlas.ai", token="qat_yaml_wins")

    from qatlas.client import auth

    store = auth._load_store()
    store.setdefault("hosts", {})["quantum-atlas.ai"] = {
        "token": "qat_StoredShouldLose",
        "added_at": "2026-05-28T00:00:00Z",
    }
    auth._save_store(store)

    assert _common.resolve_token(_ns()) == "qat_yaml_wins"


def test_auth_headers_no_token():
    # No yaml, no store. Empty dict so callers can splat
    # {**auth_headers(args), ...} safely.
    assert _common.auth_headers(_ns()) == {}


def test_auth_headers_bearer_format():
    _seed_config(token="abc123")
    assert _common.auth_headers(_ns()) == {"Authorization": "Bearer abc123"}


def test_default_base_url_from_yaml():
    _seed_config(server_url="https://my.atlas.example/")
    # get_server_url strips trailing slash for consistency.
    assert _common.default_base_url() == "https://my.atlas.example"


def test_default_base_url_no_trailing_slash_preserved():
    _seed_config(server_url="https://my.atlas.example")
    assert _common.default_base_url() == "https://my.atlas.example"


def test_default_base_url_falls_back_to_local_pocketbase():
    # No config.yaml at all: dev convenience — return localhost so a
    # bare `qatlas` against `pixi run server` works without setup.
    assert _common.default_base_url() == "http://127.0.0.1:8090"


def test_base_url_from_args_returns_yaml_value():
    _seed_config(server_url="https://yaml.example/")
    # args is accepted for back-compat; no field is read from it.
    assert _common.base_url_from_args(_ns()) == "https://yaml.example"


def test_request_verify_default_is_strict():
    # No config, no insecure: TLS verification ON (default secure).
    assert _common.request_verify(_ns()) is True


def test_request_verify_insecure_from_yaml(capsys):
    _seed_config(insecure=True)
    result = _common.request_verify(_ns())
    assert result is False
    err = capsys.readouterr().err
    assert "TLS certificate verification is disabled" in err


def test_request_verify_warning_one_shot_per_args(capsys):
    _seed_config(insecure=True)
    args = _ns()
    _common.request_verify(args)
    _ = capsys.readouterr()
    # Second call on the same args namespace must not warn again.
    _common.request_verify(args)
    assert capsys.readouterr().err == ""


def test_add_common_http_args_only_registers_request_timeout():
    """v0.17.0 removed --base-url / --token / --insecure.

    Argparse should still register --request-timeout (per-call
    ergonomic knob) but reject the now-deleted flags.
    """
    parser = argparse.ArgumentParser()
    _common.add_common_http_args(parser)
    parsed = parser.parse_args([])
    assert parsed.request_timeout == 120.0

    parsed = parser.parse_args(["--request-timeout", "30"])
    assert parsed.request_timeout == 30.0

    # Removed flags must be rejected by argparse (it exits with 2 on
    # unknown arg, which surfaces as SystemExit in tests).
    for removed_flag in ("--base-url", "--token", "--insecure"):
        with pytest.raises(SystemExit):
            parser.parse_args([removed_flag, "value"] if removed_flag != "--insecure" else [removed_flag])
