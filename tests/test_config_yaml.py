"""Tests for ``qatlas.config_yaml`` (v0.17.0+ minimal surface).

config_yaml now houses only file-IO helpers used by ``qatlas config``:

* load / dump / atomic write
* coerce_for_field (string → bool/int/float per ServerConfig schema)

The earlier ``.env`` migration logic was removed when v0.17.0 dropped
all env / dotenv support.
"""

from __future__ import annotations

import stat
from pathlib import Path

import pytest

from qatlas import config_yaml
from qatlas.config import ServerConfig


# ---------------------------------------------------------------------------
# load / dump round-trip
# ---------------------------------------------------------------------------


class TestLoadYamlFile:
    def test_missing_file_returns_empty_dict(self, tmp_path: Path) -> None:
        assert config_yaml.load_yaml_file(tmp_path / "missing.yaml") == {}

    def test_empty_file_returns_empty_dict(self, tmp_path: Path) -> None:
        p = tmp_path / "config.yaml"
        p.write_text("")
        assert config_yaml.load_yaml_file(p) == {}

    def test_non_mapping_top_level_raises(self, tmp_path: Path) -> None:
        # The user might paste a YAML list by accident — fail with a
        # clear message instead of letting pydantic raise an opaque
        # validation error 30 stack frames deeper.
        p = tmp_path / "config.yaml"
        p.write_text("- one\n- two\n")
        with pytest.raises(ValueError, match="top-level mapping"):
            config_yaml.load_yaml_file(p)

    def test_invalid_yaml_raises_with_path(self, tmp_path: Path) -> None:
        p = tmp_path / "config.yaml"
        p.write_text(": malformed yaml\n")
        with pytest.raises(ValueError, match=str(p)):
            config_yaml.load_yaml_file(p)

    def test_simple_mapping_round_trip(self, tmp_path: Path) -> None:
        p = tmp_path / "config.yaml"
        p.write_text("server_url: https://atlas.example/\ninsecure: false\n")
        loaded = config_yaml.load_yaml_file(p)
        assert loaded == {"server_url": "https://atlas.example/", "insecure": False}


class TestDumpYaml:
    def test_includes_header_comment(self) -> None:
        out = config_yaml.dump_yaml({"server_url": "https://x"})
        assert "QuantumAtlas client config" in out
        assert "server_url: https://x" in out

    def test_empty_dict_emits_header_only(self) -> None:
        out = config_yaml.dump_yaml({})
        # Body is empty but the header is preserved so a fresh file
        # still tells the user how to fill it.
        assert "QuantumAtlas client config" in out
        # Verify no spurious null content.
        assert "null" not in out

    def test_preserves_caller_ordering(self) -> None:
        # PyYAML alphabetises by default; we override with
        # ``sort_keys=False`` to keep the operator-relevant fields
        # (server_url, token) on top.
        out = config_yaml.dump_yaml(
            {"server_url": "https://x", "anthropic_api_key": "sk-..."}
        )
        server_idx = out.find("server_url:")
        anth_idx = out.find("anthropic_api_key:")
        assert server_idx < anth_idx, "caller-supplied ordering must survive dump"


class TestWriteYamlAtomic:
    def test_creates_file_with_0600_mode(self, tmp_path: Path) -> None:
        target = tmp_path / "sub" / "config.yaml"
        config_yaml.write_yaml_atomic(target, {"server_url": "https://x"})
        assert target.is_file()
        # 0600: user-only read/write.
        actual_mode = stat.S_IMODE(target.stat().st_mode)
        assert actual_mode == 0o600

    def test_atomic_replace_does_not_leave_tmp(self, tmp_path: Path) -> None:
        target = tmp_path / "config.yaml"
        config_yaml.write_yaml_atomic(target, {"server_url": "https://x"})
        # Sibling tmp files (config.yaml.XXX.tmp) must be cleaned up.
        leftovers = [
            p for p in tmp_path.iterdir()
            if p.name.startswith("config.yaml.") and p.suffix == ".tmp"
        ]
        assert leftovers == []

    def test_overwrites_existing(self, tmp_path: Path) -> None:
        target = tmp_path / "config.yaml"
        config_yaml.write_yaml_atomic(target, {"server_url": "https://v1"})
        config_yaml.write_yaml_atomic(target, {"server_url": "https://v2"})
        loaded = config_yaml.load_yaml_file(target)
        assert loaded == {"server_url": "https://v2"}


# ---------------------------------------------------------------------------
# coerce_for_field — used by `qatlas config set <key> <string-value>`
# ---------------------------------------------------------------------------


class TestCoerceForField:
    def test_bool_field_truthy_strings(self) -> None:
        for raw in ("1", "true", "TRUE", "yes", "on"):
            assert config_yaml.coerce_for_field("insecure", raw) is True

    def test_bool_field_falsy_strings(self) -> None:
        for raw in ("0", "false", "no", "off", ""):
            assert config_yaml.coerce_for_field("insecure", raw) is False

    def test_int_field_parsed(self) -> None:
        assert config_yaml.coerce_for_field("mineru_timeout", "300") == 300

    def test_float_field_parsed(self) -> None:
        assert config_yaml.coerce_for_field("mineru_poll_interval", "5.5") == 5.5

    def test_string_field_passes_through(self) -> None:
        assert (
            config_yaml.coerce_for_field("server_url", "https://x.example/")
            == "https://x.example/"
        )

    def test_unknown_field_passes_through_as_string(self) -> None:
        # Defensive: a future qatlas config set invocation against a
        # since-removed field should still write _something_, not crash.
        assert config_yaml.coerce_for_field("totally_made_up_field", "value") == "value"

    def test_int_parse_failure_keeps_raw_string(self) -> None:
        # We never want a config set to hard-fail just because the
        # operator typo'd a number — let pydantic's validator emit
        # the real complaint when ServerConfig actually loads.
        assert config_yaml.coerce_for_field("mineru_timeout", "not-a-number") == "not-a-number"


# ---------------------------------------------------------------------------
# Integration: write → read via ServerConfig
# ---------------------------------------------------------------------------


class TestYamlReadsBackThroughServerConfig:
    """Sanity check the round-trip: config_yaml.write_yaml_atomic +
    ServerConfig.from_env() must agree on a few representative fields
    so a `qatlas config set` action is immediately reflected on
    subsequent loads.
    """

    def _isolate_xdg(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
        home = tmp_path / "home"
        home.mkdir()
        monkeypatch.setenv("HOME", str(home))
        monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
        return home / ".config" / "qatlas" / "config.yaml"

    def test_string_field(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        yaml_path = self._isolate_xdg(tmp_path, monkeypatch)
        config_yaml.write_yaml_atomic(yaml_path, {"server_url": "https://wat.example/"})
        cfg = ServerConfig.from_env()
        assert cfg.server_url == "https://wat.example/"

    def test_bool_field(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        yaml_path = self._isolate_xdg(tmp_path, monkeypatch)
        config_yaml.write_yaml_atomic(yaml_path, {"insecure": True})
        cfg = ServerConfig.from_env()
        assert cfg.insecure is True

    def test_nested_mineru_field(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        yaml_path = self._isolate_xdg(tmp_path, monkeypatch)
        config_yaml.write_yaml_atomic(yaml_path, {"mineru_api_tokens": ["jwt-xyz"]})
        cfg = ServerConfig.from_env()
        assert cfg.mineru_api_tokens == ["jwt-xyz"]
        # convenience accessor for the single-token shim
        assert cfg.mineru_api_token == "jwt-xyz"

    def test_mineru_tokens_csv_string_coerced(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        # YAML scalar string with CSV is a common user shape — the
        # field validator should split it into a list.
        yaml_path = self._isolate_xdg(tmp_path, monkeypatch)
        config_yaml.write_yaml_atomic(yaml_path, {"mineru_api_tokens": "jwt-a, jwt-b ,jwt-c"})
        cfg = ServerConfig.from_env()
        assert cfg.mineru_api_tokens == ["jwt-a", "jwt-b", "jwt-c"]
        assert cfg.mineru_api_token == "jwt-a"

    def test_mineru_tokens_empty_default(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        self._isolate_xdg(tmp_path, monkeypatch)
        cfg = ServerConfig.from_env()
        assert cfg.mineru_api_tokens == []
        assert cfg.mineru_api_token is None

    def test_unknown_yaml_key_silently_ignored(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        yaml_path = self._isolate_xdg(tmp_path, monkeypatch)
        config_yaml.write_yaml_atomic(yaml_path, {"server_url": "https://x", "not_a_field": "ignored"})
        cfg = ServerConfig.from_env()
        # Unknown keys don't raise (extra='ignore'); the known field still loads.
        assert cfg.server_url == "https://x"
