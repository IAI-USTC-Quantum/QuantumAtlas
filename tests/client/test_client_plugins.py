"""Tests for the qatlas client plugin system (registry + claim/lean gating)."""

from __future__ import annotations

import os
import stat
from pathlib import Path

import pytest

from qatlas.client.claim.plugin import ClaimPlugin, claim_plugin_enabled
from qatlas.client.leanplugin.plugin import LeanPlugin, lean_dir
from qatlas.client.leanplugin.runner import run_lean
from qatlas.client.plugins import registry


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch):
    monkeypatch.delenv("QATLAS_CLAIM_PLUGIN", raising=False)
    monkeypatch.delenv("QATLAS_LEAN_DIR", raising=False)
    # Isolate from any real config.yaml on the host.
    monkeypatch.setattr(
        "qatlas.config.ServerConfig.from_env",
        classmethod(lambda cls: cls()),
    )


def test_claim_plugin_disabled_by_default():
    assert claim_plugin_enabled() is False
    assert ClaimPlugin().available() is False


@pytest.mark.parametrize("val", ["1", "true", "yes", "on", "TRUE"])
def test_claim_plugin_enabled_via_env(monkeypatch, val):
    monkeypatch.setenv("QATLAS_CLAIM_PLUGIN", val)
    assert claim_plugin_enabled() is True
    assert ClaimPlugin().available() is True
    subs = ClaimPlugin().contrib_subcommands()
    assert "claim" in subs and callable(subs["claim"].handler)


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


def test_registry_contrib_includes_claim_when_enabled(monkeypatch):
    assert "claim" not in registry.contrib_subcommands()
    monkeypatch.setenv("QATLAS_CLAIM_PLUGIN", "1")
    assert "claim" in registry.contrib_subcommands()
