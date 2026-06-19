"""Tests for the `qatlas upload` CLI parser + verification helpers.

The HTTP-bound code paths (cmd_upload_pdf / cmd_upload_mineru) are not
exercised here because they shell out to a live qatlasd; the goal is to
lock down argparse behaviour and the small pure helper
(_emit_verification_header) that both commands share.

Design contract under test (PR #19 follow-up): the contributor cannot
override paper metadata — title / authors / linked-arxiv-id are always
fetched from OpenAlex server-side. The CLI exposes ONLY ``--verify`` to
choose strict/warn policy when OpenAlex cannot resolve the DOI. Any
attempt to add ``--title`` / ``--authors`` flags would resurrect the
override path and is treated as a regression.
"""

from __future__ import annotations

import io
import sys
from typing import Any

import pytest

from qatlas.client import upload as cli


# ---------------------------------------------------------------------------
# build_mineru_parser — verify args
# ---------------------------------------------------------------------------


def test_mineru_parser_accepts_verify_flag():
    """The mineru subcommand must accept --verify to pick strict/warn
    policy. The contributor cannot supply title/authors; OpenAlex is
    the sole metadata source."""
    parser = cli.build_mineru_parser()
    ns = parser.parse_args(["10.1103/foo", "--zip", "r.zip", "--verify", "strict"])
    assert ns.arxiv_id == "10.1103/foo"
    assert ns.zip == "r.zip"
    assert ns.verify == "strict"


def test_mineru_parser_defaults_verify_to_warn():
    """--verify defaults to 'warn' so a contributor who omits the flag
    sees the same lenient behaviour as before — strict is opt-in."""
    parser = cli.build_mineru_parser()
    ns = parser.parse_args(["2501.00010v1", "--zip", "r.zip"])
    assert ns.verify == "warn"


def test_mineru_parser_rejects_unknown_verify_value():
    """--verify is a choice — passing anything other than warn/strict
    is a user error and must surface as a normal argparse SystemExit."""
    parser = cli.build_mineru_parser()
    with pytest.raises(SystemExit):
        parser.parse_args(["2501.00010v1", "--zip", "r.zip", "--verify", "lax"])


def test_mineru_parser_does_not_expose_title_or_authors():
    """Regression guard: --title / --authors were a metadata-override
    surface that the design rejected. They must NOT be re-added."""
    parser = cli.build_mineru_parser()
    options = {opt for action in parser._actions for opt in action.option_strings}
    assert "--title" not in options, "contributor must not override paper title"
    assert "--authors" not in options, "contributor must not override paper authors"


def test_mineru_parser_help_mentions_doi():
    """The description must mention DOI support so users discovering
    --verify through --help see what it controls."""
    parser = cli.build_mineru_parser()
    assert "DOI" in parser.description
    for action in parser._actions:
        if "--verify" in (action.option_strings or []):
            assert "DOI" in (action.help or "")


# ---------------------------------------------------------------------------
# build_pdf_parser — verify-only shape
# ---------------------------------------------------------------------------


def test_pdf_parser_accepts_verify_flag():
    """Sanity: the PDF parser keeps --verify (the only DOI-policy lever)."""
    parser = cli.build_pdf_parser()
    ns = parser.parse_args(["10.1103/foo", "--pdf", "p.pdf", "--verify", "strict"])
    assert ns.verify == "strict"


def test_pdf_parser_does_not_expose_title_or_authors():
    """Same regression guard as the mineru parser — no override surface."""
    parser = cli.build_pdf_parser()
    options = {opt for action in parser._actions for opt in action.option_strings}
    assert "--title" not in options, "contributor must not override paper title"
    assert "--authors" not in options, "contributor must not override paper authors"


# ---------------------------------------------------------------------------
# _emit_verification_header
# ---------------------------------------------------------------------------


class _FakeResp:
    """Minimal duck-type for requests.Response; only .headers is read."""

    def __init__(self, headers: dict[str, str]) -> None:
        self.headers = headers


def test_emit_verification_header_prints_to_stderr(capsys):
    cli._emit_verification_header(_FakeResp({"X-QAtlas-Verification": "verified"}))
    err = capsys.readouterr().err
    assert "DOI metadata verification: verified" in err


def test_emit_verification_header_silent_when_absent(capsys):
    cli._emit_verification_header(_FakeResp({}))
    assert capsys.readouterr().err == ""


def test_emit_verification_header_silent_when_header_not_set(capsys):
    """Other headers (no X-QAtlas-Verification) must not leak as 'None'."""
    cli._emit_verification_header(_FakeResp({"Content-Type": "application/json"}))
    assert capsys.readouterr().err == ""


# ---------------------------------------------------------------------------
# main() dispatcher — DOI mineru routing (PR #19 review fix)
# ---------------------------------------------------------------------------


def test_looks_like_doi_recognizes_bare_dois():
    """The DOI-shape gate must accept canonical bare DOIs and strip
    URL prefixes (https://doi.org/, doi:, etc.) before re-checking."""
    assert cli._looks_like_doi("10.1103/PhysRevLett.123.070501")
    assert cli._looks_like_doi("10.7717/peerj.4375")
    assert cli._looks_like_doi("10.1234/foo/bar")  # nested-slash DOIs are valid
    assert cli._looks_like_doi("https://doi.org/10.1103/x")
    assert cli._looks_like_doi("doi:10.1103/x")
    assert cli._looks_like_doi("DOI:10.1103/X")  # case-insensitive
    assert cli._looks_like_doi("  10.1103/x  ")  # whitespace tolerated


def test_looks_like_doi_rejects_arxiv_and_garbage():
    assert not cli._looks_like_doi("")
    assert not cli._looks_like_doi("2501.00010v1")
    assert not cli._looks_like_doi("quant-ph/9508027v1")
    assert not cli._looks_like_doi("11.1103/x")  # wrong directory indicator
    assert not cli._looks_like_doi("10.x/missing-digits")
    assert not cli._looks_like_doi("not-a-doi-at-all")


def test_main_kills_arxiv_mineru_with_exit_2(capsys):
    """The arxiv direct-zip path was killed in v0.19.0 to stop racing
    `qatlas contrib mineru`'s claim/lease state. Must still error
    even though we're now allowing the DOI path through main()."""
    rc = cli.main(["mineru", "2501.00010v1", "--zip", "/dev/null"])
    err = capsys.readouterr().err
    assert rc == 2
    assert "removed in v0.19.0" in err
    assert "qatlas contrib mineru" in err
    # Make sure the DOI form is still advertised so a user with a DOI
    # zip doesn't bounce off the error and give up.
    assert "DOI form" in err


def test_main_routes_doi_mineru_through_dispatcher(monkeypatch, tmp_path):
    """DOI-shaped positional must reach `cmd_upload_mineru`. We
    intercept run_with_request_errors so we don't need a live server,
    and observe the args that would have been used."""
    captured: dict[str, Any] = {}

    def _capture(func, args):
        captured["func"] = func
        captured["args"] = args
        return 0

    monkeypatch.setattr(cli, "run_with_request_errors", _capture)
    rc = cli.main(
        [
            "mineru",
            "10.1103/PhysRevLett.123.070501",
            "--zip",
            str(tmp_path / "fake.zip"),
            "--verify",
            "strict",
        ]
    )
    assert rc == 0
    assert captured["func"] is cli.cmd_upload_mineru
    assert captured["args"].arxiv_id == "10.1103/PhysRevLett.123.070501"
    assert captured["args"].verify == "strict"
    # The contributor never supplies title/authors; argparse must not
    # have those attributes at all.
    assert not hasattr(captured["args"], "title")
    assert not hasattr(captured["args"], "authors")


def test_main_routes_doi_url_prefix_through_dispatcher(monkeypatch, tmp_path):
    """`https://doi.org/<doi>` form must also reach the dispatcher
    (the server normalizes; the client just gates on shape)."""
    captured: dict[str, Any] = {}

    def _capture(func, args):
        captured["func"] = func
        captured["args"] = args
        return 0

    monkeypatch.setattr(cli, "run_with_request_errors", _capture)
    rc = cli.main(
        [
            "mineru",
            "https://doi.org/10.1103/PhysRevLett.123.070501",
            "--zip",
            str(tmp_path / "fake.zip"),
        ]
    )
    assert rc == 0
    assert captured["func"] is cli.cmd_upload_mineru
    # The client passes the raw value through; URL normalization
    # happens server-side via paperassets.NormalizeDOI.
    assert (
        captured["args"].arxiv_id == "https://doi.org/10.1103/PhysRevLett.123.070501"
    )


def test_main_pdf_subcommand_still_dispatches(monkeypatch, tmp_path):
    """PDF path is unchanged by the metadata-override fix — keep the
    smoke test so a future refactor doesn't accidentally re-kill it too."""
    captured: dict[str, Any] = {}

    def _capture(func, args):
        captured["func"] = func
        captured["args"] = args
        return 0

    monkeypatch.setattr(cli, "run_with_request_errors", _capture)
    rc = cli.main(["pdf", "2501.00010v1", "--pdf", str(tmp_path / "fake.pdf")])
    assert rc == 0
    assert captured["func"] is cli.cmd_upload_pdf
    assert captured["args"].arxiv_id == "2501.00010v1"


def test_main_top_help_mentions_doi_mineru(capsys):
    """The top-level help must surface `qatlas upload mineru DOI` so
    contributors discovering the kill-error for arxiv don't conclude
    the whole subcommand is dead."""
    rc = cli.main([])
    out = capsys.readouterr().out
    assert rc == 0
    assert "upload mineru DOI" in out


def test_main_top_help_does_not_advertise_title_or_authors(capsys):
    """Regression guard: the deprecation banner must not advertise the
    removed --title / --authors flags either, so users can't be tempted
    to re-add them and silently get ignored."""
    rc = cli.main([])
    out = capsys.readouterr().out
    assert rc == 0
    assert "--title" not in out
    assert "--authors" not in out
