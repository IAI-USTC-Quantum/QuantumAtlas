"""Tests for the top-level QuantumAtlas CLI."""

import runpy
import sys
import tomllib
from pathlib import Path

import pytest

from qatlas import __version__, cli
from qatlas.client import __main__ as client_cli


def test_pyproject_console_script_points_to_top_level_cli():
    pyproject = tomllib.loads(Path("pyproject.toml").read_text(encoding="utf-8"))

    assert pyproject["project"]["scripts"]["qatlas"] == "qatlas.cli:main"


def test_runtime_version_matches_project_metadata():
    pyproject = tomllib.loads(Path("pyproject.toml").read_text(encoding="utf-8"))

    assert __version__ == pyproject["project"]["version"]


def test_commitizen_uses_pyproject_version_and_v_tags():
    pyproject = tomllib.loads(Path("pyproject.toml").read_text(encoding="utf-8"))

    assert pyproject["tool"]["commitizen"] == {
        "name": "cz_conventional_commits",
        "tag_format": "v$version",
        "version_scheme": "pep440",
        "version_provider": "pep621",
        "update_changelog_on_bump": True,
        "major_version_zero": True,
    }


def test_release_workflow_publishes_python_and_go_artifacts():
    """release.yml builds wheels + Go binaries, triggers on tag push, publishes to GitHub + PyPI."""
    release_workflow = Path(".github/workflows/release.yml").read_text(encoding="utf-8")

    # Trigger surface (qalgo-style: user pushes a v*.*.* tag → workflow
    # fires). We deliberately do NOT trigger on branch pushes anymore —
    # that surface fired spurious red badges every time an unrelated
    # commit touched pyproject.toml without bumping the version. See the
    # block comment at the top of release.yml for the full story.
    assert "Tag and publish release" in release_workflow
    assert "'v*.*.*'" in release_workflow
    assert "softprops/action-gh-release@v2" in release_workflow

    # 5-job DAG (renamed from earlier 3-job split: prep+python+binary
    # for the build half, create+pypi for the publish half)
    assert "prep:" in release_workflow
    assert "python-build:" in release_workflow
    assert "binary-build:" in release_workflow
    assert "create-release:" in release_workflow
    assert "pypi-publish:" in release_workflow

    # Go binaries are cross-compiled via the matrix over (OS, arch).
    # Each matrix entry runs on its native runner; linux targets build
    # INSIDE a manylinux_2_28 container (AlmaLinux 8 / glibc 2.28) with
    # -static-libstdc++ so the binary has no CXXABI runtime dep while
    # keeping libc dynamic (dlopen works for DuckDB httpfs).
    assert "- target: linux-amd64" in release_workflow
    assert "- target: linux-arm64" in release_workflow
    assert "- target: darwin-arm64" in release_workflow
    assert "qatlas-server-${{ matrix.target }}" in release_workflow
    assert "manylinux_2_28_x86_64" in release_workflow
    assert "manylinux_2_28_aarch64" in release_workflow
    # -static-libstdc++ bakes libstdc++ into the binary (no CXXABI dep);
    # -static-libgcc does the same for libgcc_s. libc stays dynamic so
    # dlopen works. Verify both are present and the old full-static hack
    # (`-extldflags=-static` or `-extldflags -static`) is NOT.
    assert "static-libstdc++" in release_workflow
    assert "static-libgcc" in release_workflow
    assert "-X main.Version=${{ needs.prep.outputs.version }}" in release_workflow

    # PyPI uses Trusted Publishing (no token, OIDC via environment + id-token)
    assert "pypa/gh-action-pypi-publish@release/v1" in release_workflow
    assert "id-token: write" in release_workflow
    assert "name: pypi" in release_workflow


def test_top_level_help(capsys):
    result = cli.main(["--help"])

    captured = capsys.readouterr()
    client_section, local_section = captured.out.split("  Local workspace commands:")
    assert result == 0
    assert "QuantumAtlas command line" in captured.out
    assert "Client/operator commands" in captured.out
    assert "Local workspace commands" in captured.out
    assert "ingest" in client_section
    assert "codegen" in client_section
    assert "parser" not in client_section
    assert "wiki" not in client_section
    assert "parser" in local_section
    assert "wiki" in local_section


def test_readme_documents_uv_tool_install():
    readme = Path("README.md").read_text(encoding="utf-8")

    assert "uv tool install" in readme
    assert "qatlas --help" in readme


def test_top_level_version(capsys):
    result = cli.main(["--version"])

    captured = capsys.readouterr()
    assert result == 0
    assert captured.out.strip() == f"qatlas {__version__}"


def test_unknown_command_returns_usage_error(capsys):
    result = cli.main(["nope"])

    captured = capsys.readouterr()
    assert result == 2
    assert "unknown command 'nope'" in captured.err
    assert "qatlas --help" in captured.err


def test_dispatches_to_existing_module_cli(monkeypatch):
    calls = []
    original_argv = sys.argv[:]

    def fake_run_module(module_name, run_name=None):
        calls.append((module_name, run_name, sys.argv[:]))

    monkeypatch.setattr(runpy, "run_module", fake_run_module)

    result = cli.main(["codegen", "circuit.json", "--backend", "qiskit"])

    assert result == 0
    assert calls == [
        (
            "qatlas.codegen.__main__",
            "__main__",
            ["qatlas codegen", "circuit.json", "--backend", "qiskit"],
        )
    ]
    assert sys.argv == original_argv


def test_dispatches_ingest_to_http_client(monkeypatch):
    calls = []

    def fake_run_module(module_name, run_name=None):
        calls.append((module_name, run_name, sys.argv[:]))

    monkeypatch.setattr(runpy, "run_module", fake_run_module)

    result = cli.main(["ingest", "quant-ph/9508027", "--no-poll"])

    assert result == 0
    assert calls == [
        (
            "qatlas.client.__main__",
            "__main__",
            ["qatlas ingest", "quant-ph/9508027", "--no-poll"],
        )
    ]


def test_dispatch_normalizes_aliases(monkeypatch):
    calls = []

    def fake_run_module(module_name, run_name=None):
        calls.append((module_name, sys.argv[:]))

    monkeypatch.setattr(runpy, "run_module", fake_run_module)

    result = cli.main(["generate", "circuit.json"])

    assert result == 0
    assert calls == [
        ("qatlas.codegen.__main__", ["qatlas codegen", "circuit.json"])
    ]


def test_child_system_exit_code_is_returned(monkeypatch):
    def fake_run_module(module_name, run_name=None):
        raise SystemExit(7)

    monkeypatch.setattr(runpy, "run_module", fake_run_module)

    assert cli.main(["wiki", "lint"]) == 7


def test_ingest_client_defaults_to_public_base_url(tmp_path, monkeypatch):
    (tmp_path / ".env").write_text(
        "PUBLIC_BASE_URL=https://atlas.example\nSERVER_PORT=9000\n",
        encoding="utf-8",
    )
    monkeypatch.delenv("QATLAS_SKIP_DOTENV", raising=False)
    monkeypatch.delenv("QUANTUMATLAS_SKIP_DOTENV", raising=False)
    monkeypatch.setattr("qatlas.config.get_project_root", lambda: tmp_path)

    assert client_cli._default_base_url() == "https://atlas.example"


def test_ingest_status_can_skip_tls_verification(monkeypatch, capsys):
    captured = {}

    class FakeResponse:
        def raise_for_status(self):
            return None

        def json(self):
            return {"task_id": "task-1", "status": "succeeded"}

    def fake_get(url, timeout, verify):
        captured["url"] = url
        captured["verify"] = verify
        return FakeResponse()

    monkeypatch.setattr(client_cli.requests, "get", fake_get)

    result = client_cli.main(
        [
            "status",
            "task-1",
            "--base-url",
            "https://server",
            "--insecure",
        ]
    )

    captured_output = capsys.readouterr()
    assert result == 0
    assert captured["url"] == "https://server/api/ingest/task-1"
    assert captured["verify"] is False
    assert "TLS certificate verification is disabled" in captured_output.err


def test_ingest_client_uses_continue_endpoint_with_stages(tmp_path, monkeypatch):
    captured = {}

    class FakeResponse:
        def raise_for_status(self):
            return None

        def json(self):
            return {"task_id": "task-2", "status": "queued"}

    def fake_post(url, json, timeout, verify):
        captured["url"] = url
        captured["json"] = json
        captured["timeout"] = timeout
        captured["verify"] = verify
        return FakeResponse()

    monkeypatch.setattr(client_cli, "_default_base_url", lambda: "http://server")
    monkeypatch.setattr(client_cli.requests, "post", fake_post)

    result = client_cli.main(
        [
            "continue",
            "task-1",
            "--parser",
            "pymupdf",
            "--stages",
            "parse",
            "--no-poll",
        ]
    )

    assert result == 0
    assert captured["url"] == "http://server/api/ingest/task-1/continue"
    assert captured["json"]["stages"] == ["parse"]
    assert captured["json"]["parser"] == "pymupdf"
    # ff-only: client must NOT send reviewed-extraction fields
    assert "algorithm" not in captured["json"]
    assert "create_wiki" not in captured["json"]
    assert "sync_neo4j" not in captured["json"]
    assert captured["verify"] is True


def test_ingest_client_refuses_silent_parser_default(monkeypatch, capsys):
    monkeypatch.setattr(client_cli, "_default_base_url", lambda: "http://server")
    with pytest.raises(SystemExit) as excinfo:
        client_cli.main(["quant-ph/9508027", "--no-poll"])
    assert excinfo.value.code != 0
    captured_output = capsys.readouterr()
    assert "--parser" in captured_output.err

