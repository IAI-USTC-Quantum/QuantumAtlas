"""Tests for qatlas.paths (v0.17.0+: YAML-only, platformdirs-driven).

Verifies that user_config_dir / user_state_dir / user_config_yaml_path
delegate to ``platformdirs`` with ``appauthor=False``, so the right
path is chosen per platform (XDG on Linux, ~/Library/Application
Support on macOS, %APPDATA% on Windows).
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Iterator

import platformdirs
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


class TestUserDirs:
    """Smoke that paths delegate to platformdirs with appauthor=False."""

    def test_user_config_dir_matches_platformdirs(self, isolated_env: Path) -> None:
        expected = Path(platformdirs.user_config_dir("qatlas", appauthor=False))
        assert paths.user_config_dir() == expected

    def test_user_state_dir_matches_platformdirs(self, isolated_env: Path) -> None:
        expected = Path(platformdirs.user_state_dir("qatlas", appauthor=False))
        assert paths.user_state_dir() == expected

    def test_user_cache_dir_matches_platformdirs(self, isolated_env: Path) -> None:
        expected = Path(platformdirs.user_cache_dir("qatlas", appauthor=False))
        assert paths.user_cache_dir() == expected

    def test_user_config_yaml_path_under_user_config_dir(self, isolated_env: Path) -> None:
        assert paths.user_config_yaml_path() == paths.user_config_dir() / "config.yaml"

    def test_linux_default_is_xdg(self, isolated_env: Path) -> None:
        """On the Linux test runners we use, platformdirs returns
        ``~/.config/qatlas`` by default (the freedesktop XDG fallback).
        Skipped on other platforms — they have different defaults
        that this test isn't asserting.
        """
        if not _is_linux():
            pytest.skip("Linux-only assertion (this test runner is not Linux)")
        assert paths.user_config_dir() == Path(os.environ["HOME"]) / ".config" / "qatlas"

    def test_linux_honors_xdg_config_home(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """On Linux, $XDG_CONFIG_HOME overrides the default — this is
        the freedesktop spec, and platformdirs honors it."""
        if not _is_linux():
            pytest.skip("Linux-only assertion (XDG_CONFIG_HOME has no effect elsewhere)")
        override = isolated_env / "custom-xdg"
        override.mkdir()
        monkeypatch.setenv("XDG_CONFIG_HOME", str(override))
        assert paths.user_config_dir() == override / "qatlas"


def _is_linux() -> bool:
    import sys
    return sys.platform.startswith("linux")


class TestUserConfigYamlPath:
    """v0.17.0 simplified — path is derived from platformdirs, no
    QATLAS_CONFIG / QATLAS_DOTENV overrides."""

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
        # On POSIX the file should be 0600; on Windows chmod is mostly
        # ignored and we just assert the file exists.
        if hasattr(os, "geteuid"):
            assert (path.stat().st_mode & 0o777) == 0o600


class TestAuthSharesPathResolution:
    """Defensive: qatlas.client.auth.config_dir() must agree with
    qatlas.paths.user_config_dir() so hosts.yml and config.yaml live
    under the same root on every platform.
    """

    def test_auth_config_dir_equals_user_config_dir(self, isolated_env: Path) -> None:
        from qatlas.client import auth

        assert auth.config_dir() == paths.user_config_dir()

    def test_auth_hosts_file_under_user_config_dir(self, isolated_env: Path) -> None:
        from qatlas.client import auth

        assert auth.hosts_file() == paths.user_config_dir() / "hosts.yml"
