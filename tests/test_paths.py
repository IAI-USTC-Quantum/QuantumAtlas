"""Tests for qatlas.paths (v0.17.0+: YAML-only, no dotenv).

XDG Base Directory spec compliance + canonical config.yaml path.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Iterator

import pytest

from qatlas import paths


@pytest.fixture
def isolated_env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[Path]:
    """Reset every relevant env var + cd into a clean tmp dir so each
    test sees a deterministic empty world."""
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    (tmp_path / "home").mkdir()
    for var in ("XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.chdir(tmp_path)
    yield tmp_path


class TestXDGDirs:
    def test_default_config_home(self, isolated_env: Path) -> None:
        assert paths.user_config_dir() == Path(os.environ["HOME"]) / ".config" / "qatlas"

    def test_xdg_config_home_override(self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        override = isolated_env / "custom-xdg"
        override.mkdir()
        monkeypatch.setenv("XDG_CONFIG_HOME", str(override))
        assert paths.user_config_dir() == override / "qatlas"

    def test_xdg_config_home_relative_ignored(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
    ) -> None:
        # Spec: relative XDG_*_HOME MUST be ignored. We log a warning.
        monkeypatch.setenv("XDG_CONFIG_HOME", "relative/path")
        with caplog.at_level("WARNING", logger="qatlas.paths"):
            result = paths.user_config_dir()
        assert result == Path(os.environ["HOME"]) / ".config" / "qatlas"
        assert any("non-absolute" in r.message for r in caplog.records)

    def test_state_and_cache_paths(self, isolated_env: Path) -> None:
        assert paths.xdg_state_home() == Path(os.environ["HOME"]) / ".local" / "state"
        assert paths.xdg_cache_home() == Path(os.environ["HOME"]) / ".cache"
        assert paths.user_state_dir() == Path(os.environ["HOME"]) / ".local" / "state" / "qatlas"


class TestUserConfigYamlPath:
    """v0.17.0 simplified — path is fixed under user_config_dir(), no
    QATLAS_CONFIG / QATLAS_DOTENV overrides."""

    def test_default_path(self, isolated_env: Path) -> None:
        expected = paths.user_config_dir() / "config.yaml"
        assert paths.user_config_yaml_path() == expected

    def test_follows_xdg_config_home(self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        override = isolated_env / "alt-xdg"
        override.mkdir()
        monkeypatch.setenv("XDG_CONFIG_HOME", str(override))
        assert paths.user_config_yaml_path() == override / "qatlas" / "config.yaml"

    def test_returned_unconditionally(self, isolated_env: Path) -> None:
        # The path is returned whether or not the file exists — the
        # ServerConfig loader auto-creates it on first read.
        assert not paths.user_config_yaml_path().exists()
        # Calling it again should be idempotent (no creation as side-effect).
        assert not paths.user_config_yaml_path().exists()


class TestAutoCreateConfigYaml:
    """v0.17.0 ensure_default_config_exists() — first-run auto-init."""

    def test_creates_file_on_first_call(self, isolated_env: Path) -> None:
        from qatlas.config import ensure_default_config_exists

        path = paths.user_config_yaml_path()
        assert not path.exists()
        created = ensure_default_config_exists()
        assert created == path
        assert path.is_file()
        assert "QuantumAtlas client config" in path.read_text(encoding="utf-8")

    def test_idempotent_does_not_clobber_existing(self, isolated_env: Path) -> None:
        from qatlas.config import ensure_default_config_exists

        path = paths.user_config_yaml_path()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("server_url: https://custom.example/\n", encoding="utf-8")
        ensure_default_config_exists()
        # Existing content preserved verbatim.
        assert path.read_text(encoding="utf-8") == "server_url: https://custom.example/\n"

    def test_file_mode_is_0600(self, isolated_env: Path) -> None:
        from qatlas.config import ensure_default_config_exists

        path = ensure_default_config_exists()
        assert (path.stat().st_mode & 0o777) == 0o600
