"""``qatlas contrib`` — contributor-side workflows (upload + local MinerU).

This is a thin dispatcher over the two upload backends:

* ``qatlas contrib pdf …``    → ``qatlas.client.upload`` (PDF upload, by
  arXiv ID or DOI)
* ``qatlas contrib mineru``   → ``qatlas.client.mineru`` (local MinerU
  runner: claim → run → upload; queue / single / watch modes)
* ``qatlas contrib mineru <DOI> --zip …`` → ``qatlas.client.upload``
  (direct upload of a pre-made MinerU result zip; DOI-only — arXiv papers
  go through the runner so claim/lease/upload stay one unit)

`qatlas contrib pdf` / `qatlas contrib mineru` are the only contributor
entry points for upload + local-MinerU workflows. It groups what
contributors actually do day-to-day under a single resource-ish noun
(`contrib`); power-user verbs (`config`, `auth`, `wiki`, `designer`, etc.)
stay top-level.

Why a dispatcher (not a flat copy of every subcommand)?

* ``cmd_upload_pdf`` / ``cmd_upload_mineru`` and the MinerU daemon main are
  non-trivial; we don't want two copies that can drift. The dispatcher
  forwards argv straight through to the existing argparse parsers.
* Future batch flags (``--list`` for PDFs, queue tweaks for MinerU) are
  declared HERE and stay confined to the contributor surface.
"""

from __future__ import annotations

import argparse
import sys
from typing import Mapping


def _print_top_help() -> None:
    print(
        """qatlas contrib — contributor workflows

Usage:
  qatlas contrib pdf <ARXIV_ID|DOI> --pdf <path> [--overwrite] [--verify warn|strict]
  qatlas contrib pdf --list <path>                    (planned — not implemented yet)

  qatlas contrib mineru                               # queue mode: claim and process
  qatlas contrib mineru <ARXIV_ID>                    # single mode: claim, run, upload
  qatlas contrib mineru --watch [--watch-interval N]  # daemon loop
  qatlas contrib mineru <DOI> --zip <path> [--verify warn|strict]
                                                      # upload a pre-made MinerU zip (DOI-only)

Common subcommand options pass through to the underlying module
(`qatlas.client.upload` / `qatlas.client.mineru`).  Use
`qatlas contrib pdf --help` or `qatlas contrib mineru --help` for the
full per-subcommand argument set.
""",
        end="",
    )


def _cmd_pdf(argv: list[str]) -> int:
    # Lazy import so `qatlas contrib --help` doesn't pay for the upload
    # module's HTTP / SDK imports.
    from qatlas.client import _common, upload

    if argv and argv[0] == "--list":
        # Placeholder for the planned batch path. We intentionally
        # accept the flag and refuse it explicitly so users land on a
        # clear error instead of an opaque argparse one when the
        # feature ships later. See the parent help text.
        print(
            "qatlas contrib pdf --list is a planned feature, not yet implemented. "
            "For now upload one paper at a time: "
            "`qatlas contrib pdf <ARXIV_ID> --pdf <path>`.",
            file=sys.stderr,
        )
        return 2

    # Build the same argparse parser the PDF uploader uses and dispatch
    # to cmd_upload_pdf, wrapping the call so request / value errors
    # surface as friendly CLI exit codes instead of tracebacks.
    parser = upload.build_pdf_parser()
    parser.prog = "qatlas contrib pdf"
    args = parser.parse_args(argv)
    return _common.run_with_request_errors(args.func, args)


def _cmd_mineru(argv: list[str]) -> int:
    # A `--zip` bundle means "I already have a MinerU result zip; upload it
    # directly" — distinct from the local-runner flow (claim → run MinerU →
    # upload). Direct-zip upload is DOI-only: arXiv papers must go through
    # the runner so claim/lease/upload stay one unit (the arxiv direct-zip
    # path was removed in v0.19.0). Sniff argv for --zip *before* importing
    # either backend so `qatlas contrib mineru --help` (no --zip) stays cheap.
    if any(a == "--zip" or a.startswith("--zip=") for a in argv):
        from qatlas.client import _common, upload

        parser = upload.build_mineru_parser()
        parser.prog = "qatlas contrib mineru"
        args = parser.parse_args(argv)
        if not upload._looks_like_doi(args.arxiv_id):
            print(
                "ERROR: `qatlas contrib mineru <ARXIV_ID> --zip` is not "
                "supported.\n"
                "The arxiv direct-zip path was removed in v0.19.0 because it "
                "raced the\n"
                "claim/lease state of the local MinerU runner. For an arXiv "
                "paper run:\n"
                "    qatlas contrib mineru <ARXIV_ID>\n"
                "    qatlas contrib mineru --watch\n"
                "which claims, runs MinerU, and uploads as one unit.\n\n"
                "The --zip direct-upload form is DOI-only — DOIs aren't in the "
                "needs-mineru\nqueue, so direct-zip is the only contributor "
                "path for DOI papers.",
                file=sys.stderr,
            )
            return 2
        return _common.run_with_request_errors(args.func, args)

    # No --zip: hand off to the local MinerU runner (queue / single / watch).
    # `mineru.main` accepts an argv list (used by tests) and a prog override
    # so --help shows "qatlas contrib mineru" in its usage line.
    from qatlas.client import mineru as _mineru

    return _mineru.main(argv, prog="qatlas contrib mineru")


_SUBCOMMANDS: Mapping[str, callable] = {
    "pdf": _cmd_pdf,
    "mineru": _cmd_mineru,
}


def main(argv: list[str] | None = None) -> int:
    if argv is None:
        argv = sys.argv[1:]
    if not argv or argv[0] in {"-h", "--help"}:
        _print_top_help()
        return 0
    sub, rest = argv[0], argv[1:]
    if sub not in _SUBCOMMANDS:
        print(
            f"qatlas contrib: unknown subcommand {sub!r} "
            f"(valid: {', '.join(sorted(_SUBCOMMANDS))})",
            file=sys.stderr,
        )
        _print_top_help()
        return 2
    return _SUBCOMMANDS[sub](rest)


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
