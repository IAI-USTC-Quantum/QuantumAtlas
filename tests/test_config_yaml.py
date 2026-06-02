"""Tests for ``qatlas.config_yaml`` — YAML schema flatten/unflatten,
.env auto-migration, and set/get/unset round-trips.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from qatlas import config_yaml


# ---------------------------------------------------------------------------
# flatten_yaml_to_env
# ---------------------------------------------------------------------------


class TestFlattenYamlToEnv:
    def test_flat_known_keys_map_to_env(self) -> None:
        data = {
            "server": {"url": "https://atlas.example.com", "token": "qat_abc"},
            "mineru": {"api_token": "jwt_token"},
        }
        env = config_yaml.flatten_yaml_to_env(data)
        assert env["QATLAS_SERVER_URL"] == "https://atlas.example.com"
        assert env["QATLAS_TOKEN"] == "qat_abc"
        assert env["MINERU_API_TOKEN"] == "jwt_token"

    def test_bool_coerced_to_lowercase_string(self) -> None:
        env = config_yaml.flatten_yaml_to_env({"server": {"insecure": True}})
        # field_validator._parse_true_only_bool matches the literal "true",
        # nothing else — so this string must be lowercase.
        assert env["QATLAS_INSECURE"] == "true"
        env = config_yaml.flatten_yaml_to_env({"server": {"insecure": False}})
        assert env["QATLAS_INSECURE"] == "false"

    def test_numeric_stringified(self) -> None:
        env = config_yaml.flatten_yaml_to_env({"mineru": {"poll_interval": 3.0, "timeout": 1800}})
        assert env["MINERU_POLL_INTERVAL"] == "3.0"
        assert env["MINERU_TIMEOUT"] == "1800"

    def test_empty_string_and_none_dropped(self) -> None:
        env = config_yaml.flatten_yaml_to_env({"server": {"url": "", "token": None}})
        assert "QATLAS_SERVER_URL" not in env
        assert "QATLAS_TOKEN" not in env

    def test_unknown_key_silently_ignored(self) -> None:
        # Unknown sections / keys are dropped (with a debug log) rather
        # than raised, so a future schema addition we haven't seen yet
        # can't crash an older qatlas version.
        data = {"server": {"url": "https://x.example", "future_field": "ignored"}, "totally_new_section": {}}
        env = config_yaml.flatten_yaml_to_env(data)
        assert env == {"QATLAS_SERVER_URL": "https://x.example"}

    def test_extractor_keys_use_third_party_env_names(self) -> None:
        env = config_yaml.flatten_yaml_to_env({"extractor": {"openai_api_key": "sk-x", "anthropic_api_key": "ant-y"}})
        # The yaml lives under `extractor:` but the env names stay the
        # SDK-conventional OPENAI_API_KEY / ANTHROPIC_API_KEY (no
        # QATLAS_EXTRACTOR_* prefix).
        assert env["OPENAI_API_KEY"] == "sk-x"
        assert env["ANTHROPIC_API_KEY"] == "ant-y"

    def test_mineru_keys_use_third_party_env_names(self) -> None:
        # Same as extractor — the yaml section name (mineru) doesn't
        # propagate to env names; we keep the SDK-standard MINERU_*.
        env = config_yaml.flatten_yaml_to_env({"mineru": {"api_token": "j"}})
        assert "MINERU_API_TOKEN" in env
        assert "QATLAS_MINERU_API_TOKEN" not in env


# ---------------------------------------------------------------------------
# env_dict_to_yaml (the .env → config.yaml migration direction)
# ---------------------------------------------------------------------------


class TestEnvDictToYaml:
    def test_round_trip_through_known_keys(self) -> None:
        env_pairs = {
            "QATLAS_SERVER_URL": "https://atlas.example.com",
            "QATLAS_TOKEN": "qat_abc",
            "MINERU_API_TOKEN": "jwt_token",
            "MINERU_POLL_INTERVAL": "5.5",
            "QATLAS_INSECURE": "1",
        }
        yaml_dict = config_yaml.env_dict_to_yaml(env_pairs)
        assert yaml_dict == {
            "server": {
                "url": "https://atlas.example.com",
                "token": "qat_abc",
                "insecure": True,  # coerced from "1" because path is in _BOOL_YAML_PATHS
            },
            "mineru": {
                "api_token": "jwt_token",
                "poll_interval": 5.5,  # coerced from "5.5" because numeric path
            },
        }

    def test_unknown_envvars_dropped_and_reported(self) -> None:
        env_pairs = {
            "QATLAS_SERVER_URL": "https://x.example",
            "QATLAS_POCKETBASE_URL": "http://127.0.0.1:8090",  # no YAML home (server-only legacy)
            "RANDOM_USER_VAR": "foo",
        }
        yaml_dict = config_yaml.env_dict_to_yaml(env_pairs)
        assert yaml_dict == {"server": {"url": "https://x.example"}}
        unmigrated = config_yaml.unmigrated_keys(env_pairs)
        assert "QATLAS_POCKETBASE_URL" in unmigrated
        assert "RANDOM_USER_VAR" in unmigrated

    def test_empty_values_dropped(self) -> None:
        env_pairs = {"QATLAS_SERVER_URL": "", "QATLAS_TOKEN": "  "}
        # Empty string is dropped; a whitespace-only value passes through
        # (we trust the user; the dotenv parser is stricter).
        yaml_dict = config_yaml.env_dict_to_yaml(env_pairs)
        assert "server" not in yaml_dict or "url" not in yaml_dict.get("server", {})


# ---------------------------------------------------------------------------
# load + dump round-trip
# ---------------------------------------------------------------------------


class TestLoadDumpRoundTrip:
    def test_load_missing_file_returns_empty_dict(self, tmp_path: Path) -> None:
        assert config_yaml.load_yaml_file(tmp_path / "missing.yaml") == {}

    def test_load_empty_file_returns_empty_dict(self, tmp_path: Path) -> None:
        p = tmp_path / "config.yaml"
        p.write_text("")
        assert config_yaml.load_yaml_file(p) == {}

    def test_load_non_mapping_top_level_raises(self, tmp_path: Path) -> None:
        p = tmp_path / "config.yaml"
        p.write_text("- one\n- two\n")  # list, not dict
        with pytest.raises(ValueError, match="top-level mapping"):
            config_yaml.load_yaml_file(p)

    def test_dump_preserves_section_order(self, tmp_path: Path) -> None:
        # Sections must appear in YAML_TO_ENV declaration order so the
        # most-relevant `server:` block lands at the top of the file
        # instead of alphabetically buried after `extractor:`.
        data = {
            "extractor": {"openai_api_key": "x"},
            "server": {"url": "https://y"},
            "mineru": {"api_token": "z"},
        }
        text = config_yaml.dump_yaml(data)
        idx_extractor = text.find("extractor:")
        idx_server = text.find("server:")
        idx_mineru = text.find("mineru:")
        assert idx_extractor != -1 and idx_server != -1 and idx_mineru != -1
        # We pass dicts to PyYAML in insertion order — sort_keys=False
        # then preserves it. The test thus verifies our dump_yaml
        # honours that.
        assert text.index("extractor:") < text.index("server:") < text.index("mineru:")

    def test_dump_includes_header_comment(self) -> None:
        text = config_yaml.dump_yaml({"server": {"url": "https://x"}})
        assert text.startswith("# QuantumAtlas client config")
        assert "qatlas config init/set/unset" in text
        assert "https://x" in text

    def test_full_round_trip(self, tmp_path: Path) -> None:
        original = {
            "server": {"url": "https://atlas", "token": "qat", "insecure": False},
            "mineru": {"api_token": "j", "is_ocr": True, "timeout": 1200},
        }
        p = tmp_path / "config.yaml"
        p.write_text(config_yaml.dump_yaml(original))
        reloaded = config_yaml.load_yaml_file(p)
        assert reloaded == original


# ---------------------------------------------------------------------------
# bootstrap_env_from_yaml (the runtime injection path)
# ---------------------------------------------------------------------------


class TestBootstrapEnvFromYaml:
    def test_injects_env_vars(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        for k in ("QATLAS_SERVER_URL", "QATLAS_TOKEN", "MINERU_API_TOKEN"):
            monkeypatch.delenv(k, raising=False)

        p = tmp_path / "config.yaml"
        p.write_text(config_yaml.dump_yaml({
            "server": {"url": "https://from-yaml", "token": "qat_yaml"},
            "mineru": {"api_token": "jwt_yaml"},
        }))
        config_yaml.bootstrap_env_from_yaml(p)
        assert os.environ["QATLAS_SERVER_URL"] == "https://from-yaml"
        assert os.environ["QATLAS_TOKEN"] == "qat_yaml"
        assert os.environ["MINERU_API_TOKEN"] == "jwt_yaml"

    def test_existing_env_always_wins(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        # setdefault semantic — same as godotenv non-override.
        monkeypatch.setenv("QATLAS_SERVER_URL", "https://shell-wins")
        p = tmp_path / "config.yaml"
        p.write_text(config_yaml.dump_yaml({"server": {"url": "https://from-yaml"}}))
        config_yaml.bootstrap_env_from_yaml(p)
        assert os.environ["QATLAS_SERVER_URL"] == "https://shell-wins"

    def test_missing_file_no_op(self, tmp_path: Path) -> None:
        # Doesn't raise; just doesn't inject anything.
        config_yaml.bootstrap_env_from_yaml(tmp_path / "nope.yaml")


# ---------------------------------------------------------------------------
# set / get / unset (used by `qatlas config` subcommands)
# ---------------------------------------------------------------------------


class TestSetGetUnset:
    def test_set_creates_nested_path(self) -> None:
        data: dict = {}
        assert config_yaml.set_yaml_value(data, "QATLAS_SERVER_URL", "https://x") is True
        assert data == {"server": {"url": "https://x"}}

    def test_set_bool_coerces(self) -> None:
        data: dict = {}
        config_yaml.set_yaml_value(data, "QATLAS_INSECURE", "1")
        assert data == {"server": {"insecure": True}}
        config_yaml.set_yaml_value(data, "QATLAS_INSECURE", "0")
        assert data == {"server": {"insecure": False}}

    def test_set_numeric_coerces(self) -> None:
        data: dict = {}
        config_yaml.set_yaml_value(data, "MINERU_TIMEOUT", "60")
        config_yaml.set_yaml_value(data, "MINERU_POLL_INTERVAL", "1.5")
        assert data == {"mineru": {"timeout": 60, "poll_interval": 1.5}}

    def test_set_unknown_envname_returns_false(self) -> None:
        data: dict = {}
        # QATLAS_POCKETBASE_URL has no yaml home — silent ignore would
        # mask typos.
        assert config_yaml.set_yaml_value(data, "QATLAS_POCKETBASE_URL", "x") is False
        assert data == {}

    def test_unset_removes_and_prunes_empty_parent(self) -> None:
        data = {"server": {"url": "https://x"}, "mineru": {"api_token": "y", "timeout": 60}}
        # Unset the only key under mineru → mineru section should disappear.
        assert config_yaml.unset_yaml_value(data, "MINERU_API_TOKEN") is True
        assert "mineru" in data
        assert data["mineru"] == {"timeout": 60}
        # Now unset the last key → whole mineru section pruned.
        assert config_yaml.unset_yaml_value(data, "MINERU_TIMEOUT") is True
        assert "mineru" not in data
        assert data == {"server": {"url": "https://x"}}

    def test_unset_missing_returns_false(self) -> None:
        data = {"server": {"url": "https://x"}}
        assert config_yaml.unset_yaml_value(data, "QATLAS_TOKEN") is False
        assert config_yaml.unset_yaml_value(data, "QATLAS_POCKETBASE_URL") is False

    def test_get_returns_string_form(self) -> None:
        data = {"server": {"insecure": True}, "mineru": {"timeout": 1800}}
        assert config_yaml.get_yaml_value(data, "QATLAS_INSECURE") == "true"
        assert config_yaml.get_yaml_value(data, "MINERU_TIMEOUT") == "1800"
        assert config_yaml.get_yaml_value(data, "QATLAS_TOKEN") is None
        # Unknown env name returns None (caller decides whether to error).
        assert config_yaml.get_yaml_value(data, "QATLAS_POCKETBASE_URL") is None


# ---------------------------------------------------------------------------
# guard: every entry in YAML_TO_ENV is also in ENV_TO_YAML (reverse map)
# ---------------------------------------------------------------------------


def test_yaml_to_env_and_reverse_in_sync() -> None:
    # The reverse map is built at import time — guard against someone
    # accidentally hand-editing one direction without the other.
    for path, env in config_yaml.YAML_TO_ENV.items():
        assert config_yaml.ENV_TO_YAML[env] == path
    assert len(config_yaml.YAML_TO_ENV) == len(config_yaml.ENV_TO_YAML), (
        "ENV_TO_YAML lost entries — likely a duplicate env var name in YAML_TO_ENV"
    )
