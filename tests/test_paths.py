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

    def test_xdg_used_when_present_cwd_ignored(self, isolated_env: Path) -> None:
        # cwd .env is intentionally NOT picked up (see
        # test_cwd_env_no_longer_picked_up); only XDG counts.
        (isolated_env / ".env").write_text("CWD=yes\n")
        xdg = paths.user_dotenv_path()
        xdg.parent.mkdir(parents=True)
        xdg.write_text("XDG=yes\n")
        path, source = paths.resolve_dotenv_path()
        assert path == xdg
        assert source == "xdg"

    def test_cwd_env_no_longer_picked_up(self, isolated_env: Path) -> None:
        # As of v0.15.0a5 the cwd ./.env fallback was dropped. A bare
        # .env in the working directory MUST NOT be loaded — user-level
        # CLIs shouldn't silently honour cwd config files (matches
        # gh/docker/kubectl/aws pattern).
        (isolated_env / ".env").write_text("CWD=yes\n")
        assert paths.resolve_dotenv_path() == (None, None)

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


class TestBootstrapEnv:
    """bootstrap_env() bridges pydantic-settings → os.environ so direct
    os.getenv readers (resolve_token, wiki engine, llm_interface) all see
    the values pydantic-settings loaded."""

    def test_populates_os_environ_from_xdg_file(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from qatlas.config import bootstrap_env

        xdg = paths.user_dotenv_path()
        xdg.parent.mkdir(parents=True)
        xdg.write_text(
            "QATLAS_TOKEN=qat_from_xdg\n"
            "QATLAS_SERVER_URL=https://from.xdg/\n"
        )
        # Ensure clean slate before bootstrap.
        monkeypatch.delenv("QATLAS_TOKEN", raising=False)
        monkeypatch.delenv("QATLAS_SERVER_URL", raising=False)

        bootstrap_env()

        assert os.environ.get("QATLAS_TOKEN") == "qat_from_xdg"
        assert os.environ.get("QATLAS_SERVER_URL") == "https://from.xdg/"

    def test_does_not_override_existing_env_var(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from qatlas.config import bootstrap_env

        xdg = paths.user_dotenv_path()
        xdg.parent.mkdir(parents=True)
        xdg.write_text("QATLAS_TOKEN=qat_from_xdg\n")
        # Pre-existing env var must win (env var > file in precedence).
        monkeypatch.setenv("QATLAS_TOKEN", "qat_from_env")

        bootstrap_env()

        assert os.environ["QATLAS_TOKEN"] == "qat_from_env"

    def test_no_file_no_change(self, isolated_env: Path) -> None:
        from qatlas.config import bootstrap_env
        # No .env anywhere → noop, no error.
        bootstrap_env()  # should not raise

    def test_explicit_path_argument(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from qatlas.config import bootstrap_env

        custom = isolated_env / "custom.env"
        custom.write_text("QATLAS_TOKEN=qat_custom\n")
        monkeypatch.delenv("QATLAS_TOKEN", raising=False)

        bootstrap_env(custom)

        assert os.environ["QATLAS_TOKEN"] == "qat_custom"
