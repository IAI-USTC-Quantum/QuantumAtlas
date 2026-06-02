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
        "annotated_tag": True,
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
    assert "qatlasd-${{ matrix.target }}" in release_workflow
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

    # Single SHA256SUMS manifest covers every release asset (binaries +
    # wheel + sdist). Naming follows hashicorp / k8s / debian convention.
    assert "Generate SHA256SUMS" in release_workflow
    assert "dist/SHA256SUMS" in release_workflow

    # SLSA build provenance via Sigstore public-good keyless signing.
    # Each release artifact (binary, wheel, sdist) gets an attestation
    # bound to (workflow, commit, runner, time) — verifiable with
    # `gh attestation verify <file> --repo IAI-USTC-Quantum/QuantumAtlas`.
    # The OIDC + attestations permissions live on create-release.
    assert "actions/attest-build-provenance@v3" in release_workflow
    assert "attestations: write" in release_workflow


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
            "mineru",
            "--stages",
            "parse",
            "--no-poll",
        ]
    )

    assert result == 0
    assert captured["url"] == "http://server/api/ingest/task-1/continue"
    assert captured["json"]["stages"] == ["parse"]
    assert captured["json"]["parser"] == "mineru"
    # ff-only: client must NOT send reviewed-extraction fields
    assert "algorithm" not in captured["json"]
    assert "create_wiki" not in captured["json"]
    assert "sync_neo4j" not in captured["json"]
    assert captured["verify"] is True


def test_ingest_client_defaults_parser_to_mineru(monkeypatch):
    """The `--parser` flag is optional now that 'mineru' is the only choice.

    Open-source builds dropped the local PDF parser, so the client defaults
    to mineru and still sends an explicit `parser` field on the wire (so the
    server-side schema and future additional choices stay consistent).
    """
    captured = {}

    class FakeResponse:
        def raise_for_status(self):
            return None

        def json(self):
            return {"task_id": "task-3", "status": "queued"}

    def fake_post(url, json, timeout, verify):
        captured["json"] = json
        return FakeResponse()

    monkeypatch.setattr(client_cli, "_default_base_url", lambda: "http://server")
    monkeypatch.setattr(client_cli.requests, "post", fake_post)

    result = client_cli.main(["quant-ph/9508027", "--no-poll"])
    assert result == 0
    assert captured["json"]["parser"] == "mineru"


# ---------------------------------------------------------------------------
# qatlas markdown (server-side silent MinerU conversion, client side)
# ---------------------------------------------------------------------------

from qatlas.client import markdown as markdown_cli  # noqa: E402


def test_markdown_command_registered_and_client_friendly(capsys):
    assert "markdown" in cli.COMMANDS
    assert cli.COMMANDS["markdown"].client_friendly is True
    result = cli.main(["--help"])
    captured = capsys.readouterr()
    client_section, _ = captured.out.split("  Local workspace commands:")
    assert result == 0
    assert "markdown" in client_section


class _FakeResp:
    def __init__(self, status_code, *, text="", payload=None, headers=None):
        self.status_code = status_code
        self.text = text
        self.reason = ""
        self._payload = payload
        self.headers = headers or {}

    def json(self):
        if self._payload is None:
            raise ValueError("no json")
        return self._payload


def test_markdown_cache_hit_prints_to_stdout(monkeypatch, capsys):
    def fake_get(url, headers, timeout, verify):
        assert url == "https://server/api/papers/2501.00010v1/markdown"
        return _FakeResp(200, text="# Hello\n")

    monkeypatch.setattr(markdown_cli.requests, "get", fake_get)
    result = markdown_cli.main(["2501.00010v1", "--base-url", "https://server"])
    out = capsys.readouterr().out
    assert result == 0
    assert out == "# Hello\n"


def test_markdown_polls_status_resource_until_ready(monkeypatch, capsys):
    """First GET on the content endpoint returns 202 + Operation-Location;
    the client then polls the *status* resource until it reports done, and
    finally fetches the content once more."""
    state = {"triggered": False, "status_polls": 0, "urls": []}

    def fake_get(url, headers, timeout, verify):
        state["urls"].append(url)
        if url.endswith("/markdown/status"):
            state["status_polls"] += 1
            if state["status_polls"] < 2:
                return _FakeResp(
                    200,
                    payload={"status": "processing", "state": "running"},
                    headers={"Retry-After": "5"},
                )
            return _FakeResp(
                200,
                payload={
                    "status": "done",
                    "markdown_url": "/api/papers/2501.00010v1/markdown",
                },
            )
        # content endpoint
        if not state["triggered"]:
            state["triggered"] = True
            return _FakeResp(
                202,
                payload={"status": "processing", "state": "running"},
                headers={
                    "Operation-Location": "/api/papers/2501.00010v1/markdown/status",
                    "Retry-After": "5",
                },
            )
        return _FakeResp(200, text="# Done\n")

    monkeypatch.setattr(markdown_cli.requests, "get", fake_get)
    monkeypatch.setattr(markdown_cli.time, "sleep", lambda _s: None)

    result = markdown_cli.main(
        ["2501.00010v1", "--base-url", "https://server", "--poll-interval", "0"]
    )
    out = capsys.readouterr().out
    assert result == 0
    assert out == "# Done\n"
    # Polled the status resource (not the content endpoint) while waiting.
    assert state["status_polls"] == 2
    assert "https://server/api/papers/2501.00010v1/markdown/status" in state["urls"]


def test_markdown_respects_retry_after_as_floor(monkeypatch):
    """The server's Retry-After hint floors the client's poll sleep."""
    resp = _FakeResp(202, headers={"Retry-After": "9"})
    assert markdown_cli._parse_retry_after(resp) == 9.0
    # base/cap small, but Retry-After (9s) raises the floor before jitter;
    # jitter is +-20%, so the result is at least 9 * 0.8.
    monkeypatch.setattr(markdown_cli.random, "random", lambda: 0.0)  # max negative jitter
    sleep = markdown_cli._next_sleep(0, base=0.0, cap=1.0, retry_after=9.0)
    assert sleep >= 9.0 * (1 - markdown_cli._JITTER_FRACTION) - 1e-9


def test_markdown_status_url_prefers_operation_location(monkeypatch):
    resp = _FakeResp(
        202, headers={"Operation-Location": "/api/papers/2501.00010v1/markdown/status"}
    )
    url = markdown_cli._resolve_status_url("https://server", "2501.00010v1", resp)
    assert url == "https://server/api/papers/2501.00010v1/markdown/status"
    # Absolute Operation-Location is honoured as-is.
    resp_abs = _FakeResp(202, headers={"Operation-Location": "https://edge/x/status"})
    assert (
        markdown_cli._resolve_status_url("https://server", "2501.00010v1", resp_abs)
        == "https://edge/x/status"
    )
    # No header → conventional path.
    resp_none = _FakeResp(202)
    assert (
        markdown_cli._resolve_status_url("https://server", "2501.00010v1", resp_none)
        == "https://server/api/papers/2501.00010v1/markdown/status"
    )


def test_markdown_no_wait_returns_tempfail(monkeypatch, capsys):
    def fake_get(url, headers, timeout, verify):
        return _FakeResp(
            202,
            payload={"status": "processing", "state": "queued"},
            headers={"Operation-Location": "/api/papers/2501.00010v1/markdown/status"},
        )

    monkeypatch.setattr(markdown_cli.requests, "get", fake_get)
    result = markdown_cli.main(
        ["2501.00010v1", "--base-url", "https://server", "--no-wait"]
    )
    err = capsys.readouterr().err
    assert result == markdown_cli.EXIT_PENDING
    assert "still converting" in err


def test_markdown_failed_conversion_at_content_reports_error(monkeypatch, capsys):
    def fake_get(url, headers, timeout, verify):
        return _FakeResp(502, payload={"status": "failed", "error": "boom"})

    monkeypatch.setattr(markdown_cli.requests, "get", fake_get)
    result = markdown_cli.main(["2501.00010v1", "--base-url", "https://server"])
    err = capsys.readouterr().err
    assert result == markdown_cli.EXIT_FAILED
    assert "boom" in err


def test_markdown_failed_during_polling_reports_error(monkeypatch, capsys):
    """202 to start, then the status resource reports failed."""
    state = {"triggered": False}

    def fake_get(url, headers, timeout, verify):
        if url.endswith("/markdown/status"):
            return _FakeResp(200, payload={"status": "failed", "error": "mineru exploded"})
        state["triggered"] = True
        return _FakeResp(
            202,
            payload={"status": "processing"},
            headers={"Operation-Location": "/api/papers/2501.00010v1/markdown/status"},
        )

    monkeypatch.setattr(markdown_cli.requests, "get", fake_get)
    monkeypatch.setattr(markdown_cli.time, "sleep", lambda _s: None)
    result = markdown_cli.main(["2501.00010v1", "--base-url", "https://server"])
    err = capsys.readouterr().err
    assert result == markdown_cli.EXIT_FAILED
    assert "mineru exploded" in err


def test_markdown_writes_to_output_file(monkeypatch, tmp_path, capsys):
    def fake_get(url, headers, timeout, verify):
        return _FakeResp(200, text="# File\n")

    monkeypatch.setattr(markdown_cli.requests, "get", fake_get)
    out_file = tmp_path / "paper.md"
    result = markdown_cli.main(
        ["2501.00010v1", "--base-url", "https://server", "-o", str(out_file)]
    )
    assert result == 0
    assert out_file.read_text(encoding="utf-8") == "# File\n"
