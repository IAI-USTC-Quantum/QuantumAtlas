"""Tests for the `qatlas upload` CLI parser + DOI-verify helpers.

The HTTP-bound code paths (cmd_upload_pdf / cmd_upload_mineru) are not
exercised here because they shell out to a live qatlasd; the goal is
to lock down argparse behaviour and the small pure helpers
(_doi_verify_form_data / _emit_verification_header) that both commands
share, so the DOI verify flags stop being a silent no-op on the mineru
subcommand.
"""

from __future__ import annotations

import argparse
import io
import sys
from typing import Any

import pytest

from qatlas.client import upload as cli


# ---------------------------------------------------------------------------
# build_mineru_parser — DOI verify args
# ---------------------------------------------------------------------------


def test_mineru_parser_accepts_doi_verify_args():
    """Regression for PR #19 review fix: the mineru subcommand must accept
    --title / --authors / --verify the same way `upload pdf` does, so the
    contributor's flags don't become a silent no-op."""
    parser = cli.build_mineru_parser()
    ns = parser.parse_args(
        [
            "10.1103/foo",
            "--zip",
            "r.zip",
            "--title",
            "Quantum advantage with shallow circuits",
            "--authors",
            "Bravyi; Gosset; König",
            "--verify",
            "strict",
        ]
    )
    assert ns.arxiv_id == "10.1103/foo"
    assert ns.zip == "r.zip"
    assert ns.title == "Quantum advantage with shallow circuits"
    assert ns.authors == "Bravyi; Gosset; König"
    assert ns.verify == "strict"


def test_mineru_parser_defaults_verify_to_warn():
    """--verify defaults to 'warn' to match the PDF parser, so omitting
    the flag on the mineru subcommand preserves the old behaviour."""
    parser = cli.build_mineru_parser()
    ns = parser.parse_args(["2501.00010v1", "--zip", "r.zip"])
    assert ns.title is None
    assert ns.authors is None
    assert ns.verify == "warn"


def test_mineru_parser_rejects_unknown_verify_value():
    """--verify is a choice — passing anything other than warn/strict
    is a user error and must surface as a normal argparse SystemExit."""
    parser = cli.build_mineru_parser()
    with pytest.raises(SystemExit):
        parser.parse_args(["2501.00010v1", "--zip", "r.zip", "--verify", "lax"])


def test_mineru_parser_help_mentions_doi():
    """The docstring + description must mention DOI support so users
    discovering the flag through --help see the OpenAlex cross-check."""
    parser = cli.build_mineru_parser()
    assert "DOI" in parser.description
    for action in parser._actions:
        if "--title" in (action.option_strings or []):
            assert "DOI" in (action.help or "")
        if "--authors" in (action.option_strings or []):
            assert "DOI" in (action.help or "")
        if "--verify" in (action.option_strings or []):
            assert "DOI" in (action.help or "")


# ---------------------------------------------------------------------------
# build_pdf_parser — unchanged shape (smoke)
# ---------------------------------------------------------------------------


def test_pdf_parser_still_accepts_doi_verify_args():
    """Sanity: the PDF parser keeps its pre-existing DOI flag surface."""
    parser = cli.build_pdf_parser()
    ns = parser.parse_args(
        [
            "10.1103/foo",
            "--pdf",
            "p.pdf",
            "--title",
            "X",
            "--authors",
            "A;B",
            "--verify",
            "strict",
        ]
    )
    assert ns.title == "X"
    assert ns.authors == "A;B"
    assert ns.verify == "strict"


# ---------------------------------------------------------------------------
# _doi_verify_form_data
# ---------------------------------------------------------------------------


def _ns(**overrides: Any) -> argparse.Namespace:
    """Build a minimal Namespace matching the DOI-verify attr names."""
    base: dict[str, Any] = {
        "title": None,
        "authors": None,
    }
    base.update(overrides)
    return argparse.Namespace(**base)


def test_doi_verify_form_data_empty_when_nothing_supplied():
    """No title, no authors → empty dict, which the callers pass through
    as `data or None` to skip the multipart body on arXiv uploads."""
    assert cli._doi_verify_form_data(_ns()) == {}


def test_doi_verify_form_data_title_only():
    assert cli._doi_verify_form_data(_ns(title="T")) == {"title": "T"}


def test_doi_verify_form_data_authors_only():
    assert cli._doi_verify_form_data(_ns(authors="A;B")) == {"authors": "A;B"}


def test_doi_verify_form_data_both():
    out = cli._doi_verify_form_data(_ns(title="T", authors="A;B"))
    assert out == {"title": "T", "authors": "A;B"}


# ---------------------------------------------------------------------------
# _emit_verification_header
# ---------------------------------------------------------------------------


class _FakeResp:
    """Minimal duck-type for requests.Response; only .headers is read."""

    def __init__(self, headers: dict[str, str]) -> None:
        self.headers = headers


def test_emit_verification_header_prints_to_stderr(capsys):
    cli._emit_verification_header(
        _FakeResp({"X-QAtlas-Verification": "matched"})
    )
    err = capsys.readouterr().err
    assert "DOI metadata verification: matched" in err


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
    # We need a real zip file because cmd_upload_mineru would otherwise
    # short-circuit on missing-file before we could observe — but since
    # run_with_request_errors is intercepted, the zip is never actually
    # opened. argparse.parse_args itself doesn't validate file
    # existence either, so any path string is fine here.
    rc = cli.main(
        [
            "mineru",
            "10.1103/PhysRevLett.123.070501",
            "--zip",
            str(tmp_path / "fake.zip"),
            "--title",
            "Quantum advantage with shallow circuits",
            "--authors",
            "Bravyi; Gosset; König",
            "--verify",
            "strict",
        ]
    )
    assert rc == 0
    assert captured["func"] is cli.cmd_upload_mineru
    assert captured["args"].arxiv_id == "10.1103/PhysRevLett.123.070501"
    assert captured["args"].title == "Quantum advantage with shallow circuits"
    assert captured["args"].authors == "Bravyi; Gosset; König"
    assert captured["args"].verify == "strict"


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
    assert captured["args"].arxiv_id == "https://doi.org/10.1103/PhysRevLett.123.070501"


def test_main_pdf_subcommand_still_dispatches(monkeypatch, tmp_path):
    """PDF path is unchanged by the DOI mineru fix — keep the smoke
    test so a future refactor doesn't accidentally re-kill it too."""
    captured: dict[str, Any] = {}

    def _capture(func, args):
        captured["func"] = func
        captured["args"] = args
        return 0

    monkeypatch.setattr(cli, "run_with_request_errors", _capture)
    rc = cli.main(
        [
            "pdf",
            "2501.00010v1",
            "--pdf",
            str(tmp_path / "fake.pdf"),
        ]
    )
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
    assert "OpenAlex" in out
