"""Tests for the top-level QuantumAtlas CLI."""

import runpy
import sys
import tomllib
from pathlib import Path

from atlas import __version__, cli
from atlas.client import __main__ as client_cli


def test_pyproject_console_script_points_to_top_level_cli():
    pyproject = tomllib.loads(Path("pyproject.toml").read_text(encoding="utf-8"))

    assert pyproject["project"]["scripts"]["qatlas"] == "atlas.cli:main"


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


def test_release_workflow_matches_quantum_algorithm_layout():
    """Build/artifact/GitHub Release aligned with qalgo; PyPI job intentionally omitted."""
    workflow = Path(".github/workflows/release.yml").read_text(encoding="utf-8")

    assert "Build and upload release distributions" in workflow
    assert "workflow_dispatch:" in workflow
    assert "release-build:" in workflow
    assert "create-release:" in workflow
    assert "pypi-publish:" not in workflow
    assert "gh-action-pypi-publish" not in workflow
    assert "actions/upload-artifact@v4" in workflow
    assert "actions/download-artifact@v4" in workflow
    assert "softprops/action-gh-release@v1" in workflow
    assert 'VERSION=$(grep -Po \'(?<=version = ")[^"]*\' pyproject.toml)' in workflow
    assert "tag_name: v${{ steps.get_version.outputs.version }}" in workflow


def test_top_level_help(capsys):
    result = cli.main(["--help"])

    captured = capsys.readouterr()
    client_section, local_section = captured.out.split("  Local workspace/server commands:")
    assert result == 0
    assert "QuantumAtlas command line" in captured.out
    assert "Client/operator commands" in captured.out
    assert "Local workspace/server commands" in captured.out
    assert "ingest" in client_section
    assert "codegen" in client_section
    assert "parser" not in client_section
    assert "wiki" not in client_section
    assert "parser" in local_section
    assert "wiki" in local_section
    assert "service" in captured.out


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
            "atlas.codegen.__main__",
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

    result = cli.main(["ingest", "quant-ph/9508027", "--no-extract"])

    assert result == 0
    assert calls == [
        (
            "atlas.client.__main__",
            "__main__",
            ["qatlas ingest", "quant-ph/9508027", "--no-extract"],
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
        ("atlas.codegen.__main__", ["qatlas codegen", "circuit.json"])
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
    monkeypatch.delenv("QUANTUMATLAS_SKIP_DOTENV", raising=False)
    monkeypatch.setattr("atlas.server.config.get_project_root", lambda: tmp_path)

    assert client_cli._default_base_url() == "https://atlas.example"


def test_ingest_client_can_disable_wiki_stage(monkeypatch):
    captured = {}

    class FakeResponse:
        def __init__(self, payload):
            self._payload = payload

        def raise_for_status(self):
            return None

        def json(self):
            return self._payload

    def fake_post(url, json, timeout):
        captured["url"] = url
        captured["json"] = json
        return FakeResponse({"task_id": "task-1", "status": "queued"})

    monkeypatch.setattr(client_cli, "_default_base_url", lambda: "http://server")
    monkeypatch.setattr(client_cli.requests, "post", fake_post)

    result = client_cli.main(["quant-ph/9508027", "--no-wiki", "--no-poll"])

    assert result == 0
    assert captured["url"] == "http://server/api/ingest/paper"
    assert captured["json"]["create_wiki"] is False


def test_ingest_continue_accepts_reviewed_json(tmp_path, monkeypatch):
    reviewed_path = tmp_path / "reviewed.json"
    reviewed_path.write_text(
        '{"id":"reviewed_search","name":"Reviewed Search"}',
        encoding="utf-8",
    )
    captured = {}

    class FakeResponse:
        def raise_for_status(self):
            return None

        def json(self):
            return {"task_id": "task-2", "status": "queued"}

    def fake_post(url, json, timeout):
        captured["url"] = url
        captured["json"] = json
        captured["timeout"] = timeout
        return FakeResponse()

    monkeypatch.setattr(client_cli, "_default_base_url", lambda: "http://server")
    monkeypatch.setattr(client_cli.requests, "post", fake_post)

    result = client_cli.main(
        [
            "continue",
            "task-1",
            "--reviewed-json",
            str(reviewed_path),
            "--reviewed-by",
            "alice",
            "--no-sync-neo4j",
            "--no-poll",
        ]
    )

    assert result == 0
    assert captured["url"] == "http://server/api/ingest/task-1/continue"
    assert captured["json"]["algorithm"] == {"id": "reviewed_search", "name": "Reviewed Search"}
    assert captured["json"]["reviewed_by"] == "alice"
    assert captured["json"]["sync_neo4j"] is False


def test_ingest_reviewed_uses_reviewed_extraction_endpoint(tmp_path, monkeypatch):
    reviewed_path = tmp_path / "reviewed-body.json"
    reviewed_path.write_text(
        '{"algorithm":{"id":"direct_search","name":"Direct Search"},"metadata":{"title":"T"}}',
        encoding="utf-8",
    )
    captured = {}

    class FakeResponse:
        def raise_for_status(self):
            return None

        def json(self):
            return {"task_id": "task-3", "status": "queued"}

    def fake_post(url, json, timeout):
        captured["url"] = url
        captured["json"] = json
        return FakeResponse()

    monkeypatch.setattr(client_cli, "_default_base_url", lambda: "http://server")
    monkeypatch.setattr(client_cli.requests, "post", fake_post)

    result = client_cli.main(
        [
            "reviewed",
            "quant-ph/9508027",
            "--reviewed-json",
            str(reviewed_path),
            "--no-poll",
        ]
    )

    assert result == 0
    assert captured["url"] == "http://server/api/ingest/paper/reviewed-extraction"
    assert captured["json"]["arxiv_id"] == "quant-ph/9508027"
    assert captured["json"]["algorithm"] == {"id": "direct_search", "name": "Direct Search"}
    assert captured["json"]["metadata"] == {"title": "T"}
