"""Tests for ``qatlas config <subcommand>``.

Cover the user-visible behaviours: file is created with 0600 perms,
sensitive values masked on `show` / `set` echo, get resolves via the
full precedence chain (env > file), set/unset round-trip, init seeds
from cwd .env when present.
"""

from __future__ import annotations

import os
import stat
from pathlib import Path
from typing import Iterator

import pytest

from qatlas.client import config as cfg_cmd


@pytest.fixture
def isolated_env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[Path]:
    """Same recipe as tests/test_paths.py — clean home + clean cwd.

    Critically also un-sets ``QATLAS_SKIP_DOTENV`` (tests/conftest.py
    sets it to ``1`` globally to prevent stray .env files from
    polluting test runs). For *these* tests we WANT the dotenv loader
    active so we can verify set/get round-trips through the file.
    """
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    for var in (
        "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
        "QATLAS_DOTENV", "QATLAS_TOKEN", "QATLAS_SERVER_URL",
        "MINERU_API_TOKEN", "QATLAS_WIKI_DIR",
        "QATLAS_SKIP_DOTENV", "QUANTUMATLAS_SKIP_DOTENV",
    ):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.chdir(tmp_path)
    yield tmp_path


def _run(argv: list[str]) -> int:
    return cfg_cmd.main(argv)


class TestInit:
    def test_init_creates_file_with_template(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        rc = _run(["init"])
        assert rc == 0
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        assert target.is_file()
        content = target.read_text()
        # Required keys: present uncommented even when empty.
        assert "QATLAS_SERVER_URL=" in content
        assert "QATLAS_TOKEN=" in content
        # Optional keys: commented when no seed.
        assert "# QATLAS_INSECURE=" in content
        assert "# MINERU_API_TOKEN=" in content

    def test_init_does_not_seed_from_cwd_env(self, isolated_env: Path) -> None:
        # As of v0.15.0a5 init only seeds from an existing XDG file
        # (when --force is used). A cwd ./.env must NOT be silently
        # imported — that would re-introduce the security gap we
        # closed by dropping the cwd fallback in the loader.
        (isolated_env / ".env").write_text(
            "QATLAS_SERVER_URL=https://staging.example.com\n"
            "QATLAS_TOKEN=qat_should_not_leak_in\n"
        )
        rc = _run(["init"])
        assert rc == 0
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        content = target.read_text()
        # Cwd value MUST NOT have made it into the XDG file.
        assert "https://staging.example.com" not in content
        assert "qat_should_not_leak_in" not in content
        # Required keys are still emitted as empty templates.
        assert "QATLAS_SERVER_URL=" in content
        assert "QATLAS_TOKEN=" in content

    def test_init_refuses_to_overwrite_without_force(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        target.parent.mkdir(parents=True)
        target.write_text("KEEP=me\n")
        rc = _run(["init"])
        assert rc == 1
        err = capsys.readouterr().err
        assert "already exists" in err
        assert "--force" in err
        # File untouched.
        assert target.read_text() == "KEEP=me\n"

    def test_init_force_preserves_existing_values(self, isolated_env: Path) -> None:
        # --force refreshes the template (so new fields appear after
        # version upgrades) while preserving the user's existing keys
        # — they don't want to lose their token on every refresh.
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        target.parent.mkdir(parents=True)
        target.write_text("QATLAS_TOKEN=qat_user_already_set\nCUSTOM_KEY=v\n")
        assert _run(["init", "--force"]) == 0
        content = target.read_text()
        # User's existing token must survive.
        assert "qat_user_already_set" in content
        # Non-template keys also preserved under "Inherited keys".
        assert "CUSTOM_KEY=v" in content

    def test_init_file_has_0600_perms(self, isolated_env: Path) -> None:
        assert _run(["init"]) == 0
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        mode = stat.S_IMODE(target.stat().st_mode)
        # Owner rw, no group/other access.
        assert mode == 0o600


class TestSetGetUnset:
    def test_set_creates_file_if_missing(self, isolated_env: Path) -> None:
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        assert not target.exists()
        assert _run(["set", "QATLAS_SERVER_URL", "https://x.example.com"]) == 0
        assert target.is_file()
        assert "QATLAS_SERVER_URL=https://x.example.com" in target.read_text()

    def test_set_then_get_round_trip(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://y.example.com"])
        capsys.readouterr()  # drain
        rc = _run(["get", "QATLAS_SERVER_URL"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == "https://y.example.com"

    def test_set_sensitive_value_masked_in_echo(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        rc = _run(["set", "QATLAS_TOKEN", "qat_VeryLongSensitiveValue1234"])
        assert rc == 0
        out = capsys.readouterr().out
        # Echo masks middle, no full value.
        assert "qat_VeryLongSensitiveValue1234" not in out
        assert "qat_" in out  # prefix kept

    def test_unset_removes_key(self, isolated_env: Path) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://x"])
        target = isolated_env / "home" / ".config" / "qatlas" / ".env"
        assert "QATLAS_SERVER_URL" in target.read_text()
        rc = _run(["unset", "QATLAS_SERVER_URL"])
        assert rc == 0
        assert "QATLAS_SERVER_URL" not in target.read_text()

    def test_unset_missing_key_returns_1(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "OTHER_KEY", "v"])
        rc = _run(["unset", "MISSING_KEY"])
        assert rc == 1
        err = capsys.readouterr().err
        assert "MISSING_KEY" in err

    def test_invalid_key_rejected(self, isolated_env: Path) -> None:
        # set / unset both refuse keys that don't match the env-var regex.
        with pytest.raises(SystemExit):
            _run(["set", "lowercase-key", "v"])
        with pytest.raises(SystemExit):
            _run(["set", "1starts_with_digit", "v"])
        with pytest.raises(SystemExit):
            _run(["set", "has spaces", "v"])

    def test_get_env_var_overrides_file(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://file.example.com"])
        capsys.readouterr()
        # OS env var wins per the documented precedence.
        monkeypatch.setenv("QATLAS_SERVER_URL", "https://env.example.com")
        rc = _run(["get", "QATLAS_SERVER_URL"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == "https://env.example.com"

    def test_get_unknown_key_returns_1(self, isolated_env: Path) -> None:
        assert _run(["get", "NONEXISTENT_KEY"]) == 1

    def test_set_value_with_whitespace_round_trip(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        # Values with whitespace must survive a write + parse cycle.
        # (Tests the dotenv serializer's quoting path.)
        _run(["set", "QATLAS_WIKI_DIR", "/path with spaces/wiki"])
        capsys.readouterr()
        rc = _run(["get", "QATLAS_WIKI_DIR"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == "/path with spaces/wiki"


class TestPath:
    def test_reports_xdg_when_file_exists(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://x"])
        capsys.readouterr()
        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "xdg" in out
        assert ".config/qatlas/.env" in out

    def test_cwd_env_not_reported_as_config(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        # A bare ./.env in cwd MUST be ignored — `path` should report
        # no config found, not point at the cwd file.
        (isolated_env / ".env").write_text("QATLAS_SERVER_URL=https://x\n")
        rc = _run(["path"])
        assert rc == 0
        captured = capsys.readouterr()
        assert "no config file" in captured.out
        # XDG candidate path is shown so the user knows where to put it.
        assert ".config/qatlas/.env" in captured.out

    def test_no_file_returns_friendly_message(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "no config file" in out


class TestShow:
    def test_show_masks_sensitive(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://x.example.com"])
        _run(["set", "QATLAS_TOKEN", "qat_abcdefghijklmnopqrstuv"])
        capsys.readouterr()
        rc = _run(["show"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "QATLAS_SERVER_URL=https://x.example.com" in out
        assert "qat_abcdefghijklmnopqrstuv" not in out  # masked
        # Mask format preserves prefix.
        token_line = [l for l in out.splitlines() if l.startswith("QATLAS_TOKEN=")][0]
        assert "qat_" in token_line

    def test_show_unmask_reveals_value(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_TOKEN", "qat_abcdefghijklmnopqrstuv"])
        capsys.readouterr()
        assert _run(["show", "--unmask"]) == 0
        out = capsys.readouterr().out
        assert "qat_abcdefghijklmnopqrstuv" in out
