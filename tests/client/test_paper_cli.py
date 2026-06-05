"""Tests for the `qatlas paper` CLI (reader endpoints).

Focus is on the LRO polling loop, the defaults-applied note emission,
and the cache-hit short-circuit. HTTP is stubbed via MagicMock so the
tests run offline and don't need a running qatlasd.
"""

from __future__ import annotations

import argparse
import io
import json
import sys
from typing import Any
from unittest.mock import MagicMock, patch

import pytest

from qatlas.client import paper as cli


def _args(**overrides: Any) -> argparse.Namespace:
    """Build a Namespace carrying every flag the paper module touches."""
    defaults: dict[str, Any] = {
        "id_or_doi": "0811.3171v3",
        "output": "-",
        "no_wait": False,
        "max_wait": 60.0,
        "quiet_progress": True,
        "quiet_notes": False,
        "request_timeout": 10.0,
        "kind": "markdown",
    }
    defaults.update(overrides)
    return argparse.Namespace(**defaults)


def _resp(
    status: int,
    *,
    body: bytes | str | None = None,
    json_body: dict[str, Any] | None = None,
    headers: dict[str, str] | None = None,
) -> MagicMock:
    """Build a MagicMock requests.Response-shaped object.

    Provides everything cli._do_get / cli._poll_until_cached touches:
    .status_code / .ok / .reason / .text / .headers / .json() /
    .iter_content() / .close().
    """
    resp = MagicMock()
    resp.status_code = status
    resp.ok = 200 <= status < 400
    resp.reason = {200: "OK", 202: "Accepted", 404: "Not Found", 500: "Internal Server Error"}.get(
        status, ""
    )
    resp.headers = headers or {}
    if body is None and json_body is not None:
        body = json.dumps(json_body).encode()
    if body is None:
        body = b""
    if isinstance(body, str):
        body = body.encode()
    resp.text = body.decode("utf-8", errors="replace")
    if json_body is not None:
        resp.json = MagicMock(return_value=json_body)
    else:

        def _raise_json():
            raise json.JSONDecodeError("not json", "", 0)

        resp.json = MagicMock(side_effect=_raise_json)
    resp.iter_content = MagicMock(return_value=iter([body]) if body else iter([]))
    resp.close = MagicMock()
    return resp


@pytest.fixture(autouse=True)
def _stub_config(monkeypatch):
    """Pin the base URL so we don't read ~/.config/qatlas/config.yaml."""
    monkeypatch.setattr(cli, "base_url_from_args", lambda args: "http://server.test")
    monkeypatch.setattr(cli, "auth_headers", lambda args: {"Authorization": "Bearer test"})
    monkeypatch.setattr(cli, "client_version_headers", lambda: {})
    monkeypatch.setattr(cli, "request_verify", lambda args: True)
    monkeypatch.setattr(cli, "check_response_version", lambda resp, write: None)


# ---------------------------------------------------------------------------
# _print_notes
# ---------------------------------------------------------------------------


def test_print_notes_emits_when_defaults_header_present(capsys):
    resp = _resp(
        200,
        headers={
            "X-QAtlas-Requested-Id": "9508027",
            "X-QAtlas-Resolved-Id": "quant-ph/9508027v2",
            "X-QAtlas-Defaults-Applied": "version=v2 (latest); category=quant-ph (default)",
        },
    )
    cli._print_notes(resp, quiet=False)
    err = capsys.readouterr().err
    assert "Note (server applied defaults)" in err
    assert "9508027 → quant-ph/9508027v2" in err
    assert "version=v2" in err


def test_print_notes_silent_when_no_headers(capsys):
    resp = _resp(200, headers={})
    cli._print_notes(resp, quiet=False)
    assert capsys.readouterr().err == ""


def test_print_notes_suppressed_by_quiet(capsys):
    resp = _resp(
        200,
        headers={
            "X-QAtlas-Defaults-Applied": "version=v3 (latest)",
            "X-QAtlas-Requested-Id": "0811.3171",
            "X-QAtlas-Resolved-Id": "0811.3171v3",
        },
    )
    cli._print_notes(resp, quiet=True)
    assert capsys.readouterr().err == ""


# ---------------------------------------------------------------------------
# _format_eta
# ---------------------------------------------------------------------------


def test_format_eta_compact_string_with_queue_and_fetch():
    body = {
        "state": "queued",
        "phase": "converting_md",
        "fetch": {"bytes_received": 100, "bytes_total": 500},
        "convert": {"stage": "submitting", "polled_count": 0},
        "queue": {
            "position": 3,
            "ahead_of_me": 2,
            "running_count": 4,
            "max_concurrent": 4,
            "eta_seconds": 240,
        },
    }
    out = cli._format_eta(body)
    assert "queued" in out
    assert "converting_md" in out
    assert "fetch=100/500B" in out
    assert "convert.submitting" in out
    assert "queue=#3(2ahead,4/4slot)" in out
    assert "eta=240s" in out


def test_format_eta_minimal_when_only_state():
    body = {"state": "cached"}
    assert cli._format_eta(body) == "cached"


# ---------------------------------------------------------------------------
# Cache hit (HTTP 200 on first call)
# ---------------------------------------------------------------------------


def test_do_get_cache_hit_streams_bytes(monkeypatch, capsys, tmp_path):
    out_path = tmp_path / "out.md"
    args = _args(output=str(out_path))
    resp_200 = _resp(
        200,
        body=b"# hello world\n",
        headers={"Content-Type": "text/markdown; charset=utf-8"},
    )
    with patch.object(cli.requests, "get", return_value=resp_200) as mock_get:
        rc = cli._do_get(args, "markdown")
    assert rc == 0
    mock_get.assert_called_once()
    url = mock_get.call_args[0][0]
    assert url == "http://server.test/api/papers/0811.3171v3/markdown"
    assert out_path.read_bytes() == b"# hello world\n"


# ---------------------------------------------------------------------------
# LRO 202 → poll → 200
# ---------------------------------------------------------------------------


def test_do_get_lro_polls_until_cached(monkeypatch, tmp_path):
    """First call returns 202 + Operation-Location; status returns
    queued/running for a couple polls then cached; final GET returns
    bytes."""
    out_path = tmp_path / "out.md"
    args = _args(output=str(out_path), quiet_progress=True)
    monkeypatch.setattr(cli.time, "sleep", lambda _: None)
    monkeypatch.setattr(
        cli.time,
        "monotonic",
        lambda counter=iter(range(0, 1000)): next(counter) * 1.0,
    )

    initial_202 = _resp(
        202,
        json_body={
            "state": "queued",
            "phase": "fetching_pdf",
            "pdf_ready": False,
            "md_ready": False,
            "operation": {"status_url": "/api/papers/0811.3171v3/markdown/status"},
        },
        headers={"Operation-Location": "/api/papers/0811.3171v3/markdown/status"},
    )
    poll_running = _resp(
        200,
        json_body={
            "state": "running",
            "phase": "converting_md",
            "pdf_ready": True,
            "md_ready": False,
        },
        headers={"Retry-After": "1"},
    )
    poll_cached = _resp(
        200,
        json_body={
            "state": "cached",
            "phase": "ready",
            "pdf_ready": True,
            "md_ready": True,
            "markdown_url": "/api/papers/0811.3171v3/markdown",
        },
    )
    final_200 = _resp(200, body=b"# converted markdown\n")

    seq = [initial_202, poll_running, poll_cached, final_200]
    with patch.object(cli.requests, "get", side_effect=seq):
        rc = cli._do_get(args, "markdown")
    assert rc == 0
    assert out_path.read_bytes() == b"# converted markdown\n"


def test_do_get_lro_terminal_failure_exits_nonzero(monkeypatch, capsys):
    """Job ends in failed state → CLI bails with rc=1."""
    args = _args(output="-", quiet_progress=True)
    monkeypatch.setattr(cli.time, "sleep", lambda _: None)
    monkeypatch.setattr(
        cli.time,
        "monotonic",
        lambda counter=iter(range(0, 1000)): next(counter) * 1.0,
    )
    initial_202 = _resp(
        202,
        json_body={"state": "queued", "operation": {"status_url": "/api/papers/x/markdown/status"}},
    )
    poll_failed = _resp(
        200,
        json_body={
            "state": "failed",
            "phase": "error_fetching",
            "kind": "fatal",
            "detail": "arxiv: paper not found",
        },
    )
    with patch.object(cli.requests, "get", side_effect=[initial_202, poll_failed]):
        rc = cli._do_get(args, "markdown")
    assert rc == 1
    err = capsys.readouterr().err
    assert "fatal" in err
    assert "arxiv: paper not found" in err


def test_do_get_no_wait_short_circuits_with_initial_body(monkeypatch, capsys):
    """--no-wait returns the 202 JSON and exits 0 without polling."""
    args = _args(no_wait=True, quiet_progress=True)
    initial_202 = _resp(
        202,
        json_body={
            "state": "queued",
            "phase": "fetching_pdf",
            "operation": {"status_url": "/api/papers/x/markdown/status"},
        },
    )
    with patch.object(cli.requests, "get", side_effect=[initial_202]) as mock_get:
        rc = cli._do_get(args, "markdown")
    assert rc == 0
    # Only one HTTP call — no polling.
    assert mock_get.call_count == 1
    out = capsys.readouterr().out
    assert '"state": "queued"' in out


def test_do_get_terminal_4xx_renders_server_detail(monkeypatch, capsys):
    args = _args()
    resp_404 = _resp(
        404,
        json_body={"detail": "DOI not found in OpenAlex", "doi": "10.bad/doi"},
    )
    with patch.object(cli.requests, "get", side_effect=[resp_404]):
        rc = cli._do_get(args, "markdown")
    assert rc == 1
    err = capsys.readouterr().err
    assert "404" in err
    assert "DOI not found in OpenAlex" in err
