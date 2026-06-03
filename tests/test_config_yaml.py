"""Tests for ``qatlas.config_yaml`` (v0.17.0+ minimal surface).

v0.17.0 removed the hand-maintained YAML_TO_ENV map / flatten /
bootstrap_env_from_yaml; ``ServerConfig`` now reads YAML directly
through ``pydantic_settings.YamlConfigSettingsSource``. config_yaml
keeps only:

* load / dump / atomic write
* legacy ``.env`` parser
* one-shot ``migrate_dotenv_to_yaml``

These tests focus on those surface behaviours plus a guard that
``_ENV_SYNC_TARGETS`` (in qatlas.config) stays in sync with the actual
ServerConfig fields.
"""

from __future__ import annotations

import stat
from pathlib import Path

import pytest

from qatlas import config_yaml
from qatlas.config import (
    _ENV_SYNC_TARGETS,
    ServerConfig,
    env_alias_to_field,
    field_to_env_alias,
)


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


class TestDumpYaml:
    def test_includes_header_comment(self) -> None:
        text = config_yaml.dump_yaml({"server_url": "https://x"})
        assert text.startswith("# QuantumAtlas client config")
        assert "qatlas config init/set/unset" in text
        # Header explicitly tells users `set` rewrites the file
        # (matches kubectl/gh behaviour) — they shouldn't be surprised
        # when hand-written notes vanish.
        assert "REWRITE" in text or "rewrite" in text.lower()

    def test_empty_dict_renders_blank_body(self) -> None:
        # A fresh `qatlas config init` writes an empty dict; the file
        # must NOT contain a literal `{}` — that would look like
        # garbage to a human reader.
        text = config_yaml.dump_yaml({})
        assert "{}" not in text

    def test_preserves_insertion_order(self) -> None:
        # sort_keys=False so the operator-friendly ordering survives a
        # round-trip. server_url should appear before insecure when
        # we add them in that order.
        text = config_yaml.dump_yaml(
            {"server_url": "https://x", "insecure": True, "token": "qat_y"}
        )
        idx_url = text.index("server_url:")
        idx_insecure = text.index("insecure:")
        idx_token = text.index("token:")
        assert idx_url < idx_insecure < idx_token


class TestWriteYamlAtomic:
    def test_writes_with_0600_perms(self, tmp_path: Path) -> None:
        target = tmp_path / "config.yaml"
        config_yaml.write_yaml_atomic(target, {"server_url": "https://x"})

        mode = stat.S_IMODE(target.stat().st_mode)
        assert mode == 0o600, (
            f"file perm = {oct(mode)}, want 0o600 (secrets must not be group/other readable)"
        )

    def test_creates_parent_directory(self, tmp_path: Path) -> None:
        target = tmp_path / "deeply" / "nested" / "config.yaml"
        config_yaml.write_yaml_atomic(target, {"server_url": "https://x"})
        assert target.is_file()

    def test_full_round_trip_through_disk(self, tmp_path: Path) -> None:
        original = {
            "server_url": "https://atlas.example",
            "token": "qat_xxx",
            "insecure": False,
            "mineru_api_token": "jwt_y",
            "mineru_timeout": 1200,
        }
        target = tmp_path / "config.yaml"
        config_yaml.write_yaml_atomic(target, original)
        reloaded = config_yaml.load_yaml_file(target)
        assert reloaded == original


# ---------------------------------------------------------------------------
# Migration from legacy .env
# ---------------------------------------------------------------------------


class TestMigrateDotenvToYaml:
    def test_round_trip_known_keys(self, tmp_path: Path) -> None:
        dotenv = tmp_path / ".env"
        dotenv.write_text(
            "QATLAS_SERVER_URL=https://from-env.example\n"
            'QATLAS_TOKEN="qat_from_env"\n'
            "MINERU_API_TOKEN=jwt_from_env\n"
            "QATLAS_INSECURE=1\n"
            "MINERU_TIMEOUT=600\n"
            "MINERU_POLL_INTERVAL=2.5\n"
        )
        yaml_path = tmp_path / "config.yaml"

        dropped = config_yaml.migrate_dotenv_to_yaml(dotenv, yaml_path)
        assert dropped == []

        data = config_yaml.load_yaml_file(yaml_path)
        # Field names are snake_case (ServerConfig field), not env names.
        assert data["server_url"] == "https://from-env.example"
        assert data["token"] == "qat_from_env"
        assert data["mineru_api_token"] == "jwt_from_env"
        # Bool / int / float coerced from string via ServerConfig field types.
        assert data["insecure"] is True
        assert data["mineru_timeout"] == 600
        assert data["mineru_poll_interval"] == 2.5

    def test_unknown_envvars_dropped_and_reported(self, tmp_path: Path) -> None:
        dotenv = tmp_path / ".env"
        dotenv.write_text(
            "QATLAS_SERVER_URL=https://x.example\n"
            "NEO4J_URI=bolt://server.example:7687\n"  # server-only — no client field
            "RANDOM_USER_VAR=foo\n"
        )
        yaml_path = tmp_path / "config.yaml"

        dropped = config_yaml.migrate_dotenv_to_yaml(dotenv, yaml_path)
        assert "NEO4J_URI" in dropped
        assert "RANDOM_USER_VAR" in dropped
        assert "QATLAS_SERVER_URL" not in dropped

        data = config_yaml.load_yaml_file(yaml_path)
        assert data == {"server_url": "https://x.example"}

    def test_renames_original_with_timestamped_suffix(self, tmp_path: Path) -> None:
        dotenv = tmp_path / ".env"
        dotenv.write_text("QATLAS_SERVER_URL=https://x.example\n")
        yaml_path = tmp_path / "config.yaml"

        config_yaml.migrate_dotenv_to_yaml(dotenv, yaml_path)

        # Original file gone; backup with version + timestamp present.
        assert not dotenv.exists()
        backups = list(tmp_path.glob(".env.migrated-from-v0.17.0.*"))
        assert len(backups) == 1
        # Backup retains the original content so an operator who
        # mis-migrated can rename it back.
        assert "QATLAS_SERVER_URL=https://x.example" in backups[0].read_text()

    def test_empty_dotenv_is_noop(self, tmp_path: Path) -> None:
        dotenv = tmp_path / ".env"
        dotenv.write_text("# only comments\n\n")
        yaml_path = tmp_path / "config.yaml"

        dropped = config_yaml.migrate_dotenv_to_yaml(dotenv, yaml_path)
        assert dropped == []
        # No yaml written, no rename happened — there was nothing to do.
        assert not yaml_path.exists()
        assert dotenv.exists()


class TestParseDotenv:
    def test_handles_comments_quoted_values_blanks(self, tmp_path: Path) -> None:
        p = tmp_path / ".env"
        p.write_text(
            "# top comment\n"
            "\n"
            "KEY1=plain\n"
            'KEY2="double quoted"\n'
            "KEY3='single quoted'\n"
            "  # indented comment\n"
            "KEY4=value with spaces\n"
        )
        out = config_yaml.parse_dotenv(p)
        assert out == {
            "KEY1": "plain",
            "KEY2": "double quoted",
            "KEY3": "single quoted",
            "KEY4": "value with spaces",
        }

    def test_missing_file_returns_empty(self, tmp_path: Path) -> None:
        assert config_yaml.parse_dotenv(tmp_path / "nope.env") == {}


# ---------------------------------------------------------------------------
# _coerce_for_field — used both by migration and by `qatlas config set`
# ---------------------------------------------------------------------------


class TestCoerceForField:
    def test_bool_field(self) -> None:
        # The `insecure` field is bool in ServerConfig.
        assert config_yaml._coerce_for_field("insecure", "1") is True
        assert config_yaml._coerce_for_field("insecure", "true") is True
        assert config_yaml._coerce_for_field("insecure", "TRUE") is True
        assert config_yaml._coerce_for_field("insecure", "0") is False
        assert config_yaml._coerce_for_field("insecure", "false") is False
        assert config_yaml._coerce_for_field("insecure", "no") is False

    def test_int_field(self) -> None:
        assert config_yaml._coerce_for_field("mineru_timeout", "1800") == 1800

    def test_float_field(self) -> None:
        assert config_yaml._coerce_for_field("mineru_poll_interval", "2.5") == 2.5

    def test_string_field_passes_through(self) -> None:
        assert config_yaml._coerce_for_field("server_url", "https://x") == "https://x"

    def test_unknown_field_passes_through_as_string(self) -> None:
        assert config_yaml._coerce_for_field("nope_not_a_field", "x") == "x"

    def test_unparseable_numeric_falls_back_to_string(self) -> None:
        # If a user wrote MINERU_TIMEOUT=fifteen, we'd rather pass the
        # string through (yaml will fail to load later with a clear
        # error) than crash the migration silently.
        assert config_yaml._coerce_for_field("mineru_timeout", "fifteen") == "fifteen"


# ---------------------------------------------------------------------------
# Sanity: every ServerConfig field with a validation_alias is reachable
# both ways through env_alias_to_field / field_to_env_alias
# ---------------------------------------------------------------------------


class TestEnvAliasRoundTrip:
    def test_known_pair(self) -> None:
        assert env_alias_to_field("QATLAS_SERVER_URL") == "server_url"
        assert field_to_env_alias("server_url") == "QATLAS_SERVER_URL"

    def test_third_party_sdk_alias(self) -> None:
        # SDK-standard env names (MINERU_*, OPENAI_*) map to fields
        # without the QATLAS_ prefix.
        assert env_alias_to_field("MINERU_API_TOKEN") == "mineru_api_token"
        assert field_to_env_alias("mineru_api_token") == "MINERU_API_TOKEN"

    def test_unknown_env_returns_none(self) -> None:
        assert env_alias_to_field("QATLAS_TYPO_KEY") is None
        # Server-only env that no client field claims.
        assert env_alias_to_field("QATLAS_S3_ENDPOINT") is None

    def test_every_field_with_alias_round_trips(self) -> None:
        # Schema-level guard: for every field that has a
        # validation_alias, the primary alias must round-trip through
        # the two helpers. If this breaks, future fields with quirky
        # AliasChoices won't be addressable via `qatlas config set`.
        for field_name, info in ServerConfig.model_fields.items():
            if info.validation_alias is None:
                continue
            env_name = field_to_env_alias(field_name)
            assert env_name is not None, f"no env alias for field {field_name}"
            assert env_alias_to_field(env_name) == field_name, (
                f"round-trip broken: field {field_name} -> env {env_name} -> "
                f"{env_alias_to_field(env_name)}"
            )


class TestEnvSyncTargets:
    def test_every_target_field_exists_on_model(self) -> None:
        # _ENV_SYNC_TARGETS is the list of fields whose value gets
        # mirrored back into os.environ at boot. Each entry must
        # actually exist on ServerConfig — otherwise a typo here
        # silently drops the env sync for that field.
        model_fields = set(ServerConfig.model_fields.keys())
        for field_name, _env_name in _ENV_SYNC_TARGETS:
            assert field_name in model_fields, (
                f"_ENV_SYNC_TARGETS references non-existent field {field_name!r}"
            )

    def test_every_target_env_matches_field_primary_alias(self) -> None:
        # The env name in _ENV_SYNC_TARGETS must match the field's
        # primary validation alias — otherwise we'd setdefault a name
        # that no os.getenv call site actually reads.
        for field_name, env_name in _ENV_SYNC_TARGETS:
            primary = field_to_env_alias(field_name)
            assert primary == env_name, (
                f"_ENV_SYNC_TARGETS: field {field_name} primary alias is "
                f"{primary!r} but the table says {env_name!r}"
            )
