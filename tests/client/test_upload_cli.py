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
