"""Tests for qatlas.paths — XDG dotenv path resolution.

Covers the three precedence rules (QATLAS_DOTENV override, XDG file,
cwd_legacy fallback) plus the spec-required "ignore non-absolute
XDG_CONFIG_HOME" rule.
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
    for var in ("XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "QATLAS_DOTENV"):
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


class TestResolveDotenv:
    def test_no_files_returns_none(self, isolated_env: Path) -> None:
        assert paths.resolve_dotenv_path() == (None, None)

    def test_xdg_wins_over_cwd(self, isolated_env: Path) -> None:
        # Both exist; XDG wins.
        (isolated_env / ".env").write_text("CWD=yes\n")
        xdg = paths.user_dotenv_path()
        xdg.parent.mkdir(parents=True)
        xdg.write_text("XDG=yes\n")
        path, source = paths.resolve_dotenv_path()
        assert path == xdg
        assert source == "xdg"

    def test_cwd_fallback_when_xdg_missing(self, isolated_env: Path) -> None:
        (isolated_env / ".env").write_text("CWD=yes\n")
        path, source = paths.resolve_dotenv_path()
        assert path == (isolated_env / ".env").resolve()
        assert source == "cwd_legacy"

    def test_qatlas_dotenv_override_beats_everything(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # All three layers populated; explicit override wins.
        custom = isolated_env / "explicit" / "deploy.env"
        custom.parent.mkdir()
        custom.write_text("CUSTOM=yes\n")
        (isolated_env / ".env").write_text("CWD=yes\n")
        xdg = paths.user_dotenv_path()
        xdg.parent.mkdir(parents=True)
        xdg.write_text("XDG=yes\n")
        monkeypatch.setenv("QATLAS_DOTENV", str(custom))

        path, source = paths.resolve_dotenv_path()
        assert path == custom.resolve()
        assert source == "env_override"

    def test_qatlas_dotenv_override_honoured_even_when_missing(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # Explicit path that doesn't exist — caller wants fail-loud, not
        # silent XDG fallback.
        monkeypatch.setenv("QATLAS_DOTENV", str(isolated_env / "nope.env"))
        # Even with an XDG file present, the override wins.
        xdg = paths.user_dotenv_path()
        xdg.parent.mkdir(parents=True)
        xdg.write_text("XDG=yes\n")
        path, source = paths.resolve_dotenv_path()
        assert source == "env_override"
        assert path == (isolated_env / "nope.env").resolve()
