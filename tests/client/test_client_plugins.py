"""Tests for the qatlas client plugin system (registry + lean gating)."""

from __future__ import annotations

import os
import stat
from pathlib import Path

import pytest

from qatlas.client.leanplugin.plugin import LeanPlugin, lean_dir
from qatlas.client.leanplugin.runner import run_lean
from qatlas.client.plugins import registry


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch, tmp_path):
    monkeypatch.delenv("QATLAS_LEAN_DIR", raising=False)
    # Isolate ServerConfig from the host's real ~/.config/qatlas/config.yaml.
    # ServerConfig is a pydantic BaseSettings whose yaml source is added whenever
    # user_config_yaml_path() exists on disk, so patching from_env -> cls() is not
    # enough on its own; point XDG_CONFIG_HOME at an empty dir (the path helper
    # honors it) so no host config.yaml is ever read into a test.
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
    monkeypatch.setattr(
        "qatlas.config.ServerConfig.from_env",
        classmethod(lambda cls: cls()),
    )


def test_lean_plugin_unavailable_without_checkout():
    assert lean_dir() is None
    assert LeanPlugin().available() is False


def _make_lean_checkout(tmp_path: Path, exit_code: int = 0) -> Path:
    launcher = tmp_path / "qatlas-lean"
    launcher.write_text(
        "#!/bin/sh\n"
        'printf "%s" "$*" > "$PWD/argv.txt"\n'
        'pwd > "$PWD/cwd.txt"\n'
        f"exit {exit_code}\n"
    )
    launcher.chmod(launcher.stat().st_mode | stat.S_IXUSR)
    return launcher


def test_lean_plugin_available_with_checkout(tmp_path, monkeypatch):
    _make_lean_checkout(tmp_path)
    monkeypatch.setenv("QATLAS_LEAN_DIR", str(tmp_path))
    assert lean_dir() == str(tmp_path)
    assert LeanPlugin().available() is True
    cmds = LeanPlugin().top_level_commands()
    assert "lean" in cmds


def test_lean_dir_rejects_path_without_launcher(tmp_path, monkeypatch):
    monkeypatch.setenv("QATLAS_LEAN_DIR", str(tmp_path))  # no qatlas-lean file
    assert lean_dir() is None


def test_run_lean_passthrough(tmp_path):
    _make_lean_checkout(tmp_path, exit_code=3)
    rc = run_lean(str(tmp_path), ["status", "--json"])
    assert rc == 3
    assert (tmp_path / "argv.txt").read_text().strip() == "status --json"
    # Ran in the checkout root.
    assert (tmp_path / "cwd.txt").read_text().strip() == os.path.realpath(tmp_path)


def test_run_lean_missing_launcher(tmp_path):
    # Directory exists but no launcher → 127, no crash.
    assert run_lean(str(tmp_path), ["status"]) == 127


def test_registry_top_level_includes_lean_when_configured(tmp_path, monkeypatch):
    _make_lean_checkout(tmp_path)
    monkeypatch.setenv("QATLAS_LEAN_DIR", str(tmp_path))
    assert "lean" in registry.top_level_commands()


def _force_plugins(monkeypatch, names):
    """Pin the config-driven enabled ``plugins`` list for a test."""
    monkeypatch.setattr(
        "qatlas.config.ServerConfig.from_env",
        classmethod(lambda cls: cls(plugins=names)),
    )


def test_claim_off_by_default():
    # claim ships with the package but is NOT in the default enabled set (ADR 0008
    # moved the maintained flow to qatlas-lean); it must not surface by default.
    assert "claim" not in registry.contrib_subcommands()
    assert "claim" not in [p.name for p in registry.active_plugins()]


def test_claim_enabled_via_config(monkeypatch):
    _force_plugins(monkeypatch, ["lean", "claim"])
    assert "claim" in registry.contrib_subcommands()
    assert "claim" in [p.name for p in registry.active_plugins()]


def test_explicit_empty_plugins_disables_all_builtins(monkeypatch):
    _force_plugins(monkeypatch, [])
    names = [p.name for p in registry.active_plugins()]
    assert "lean" not in names and "claim" not in names


def test_unknown_plugin_name_is_ignored(monkeypatch):
    _force_plugins(monkeypatch, ["nope", "claim"])
    # unknown 'nope' is dropped; 'claim' still enabled -> no crash
    assert "claim" in registry.contrib_subcommands()
