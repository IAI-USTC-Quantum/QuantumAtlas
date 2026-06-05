"""``qatlas contrib`` — contributor-side workflows (upload + local MinerU).

This is a thin dispatcher over the two existing modules:

* ``qatlas contrib pdf …``      → ``qatlas.client.upload`` (PDF subcommand)
* ``qatlas contrib mineru …``   → ``qatlas.client.mineru`` (local MinerU runner)

It groups what contributors actually do day-to-day under a single resource-
ish noun (`contrib`), so the top-level help reads less like "what CLI
verbs exist" and more like "what kind of person are you". Power-user
verbs (`config`, `auth`, `wiki`, `designer`, etc.) stay top-level.

The old `qatlas upload pdf` / `qatlas mineru` entries are preserved with
a deprecation warning that points here; they will be removed in a future
release once contributors have migrated.

Why a dispatcher (not a flat copy of every subcommand)?

* ``cmd_upload_pdf`` and the MinerU daemon main are non-trivial; we don't
  want two copies that can drift. The dispatcher forwards argv straight
  through to the existing argparse parsers.
* Future batch flags (``--list`` for PDFs, queue tweaks for MinerU) are
  declared HERE and stay confined to the new surface — the legacy entry
  points keep their narrow, stable contracts.
"""

from __future__ import annotations

import argparse
import sys
from typing import Mapping


def _print_top_help() -> None:
    print(
        """qatlas contrib — contributor workflows

Usage:
  qatlas contrib pdf <ARXIV_ID> --pdf <path> [--overwrite]
  qatlas contrib pdf --list <path>                    (planned — not implemented yet)

  qatlas contrib mineru                               # queue mode: claim and process
  qatlas contrib mineru <ARXIV_ID>                    # single mode
  qatlas contrib mineru --watch [--watch-interval N]  # daemon loop

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
    from qatlas.client import upload

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

    # Forward as `qatlas upload pdf ...` would have: build the same
    # argparse parser and dispatch to cmd_upload_pdf.
    parser = upload.build_pdf_parser()
    parser.prog = "qatlas contrib pdf"
    args = parser.parse_args(argv)
    return upload.cmd_upload_pdf(args)


def _cmd_mineru(argv: list[str]) -> int:
    # Reuse the MinerU daemon's main() argparse + dispatch end-to-end.
    # `mineru.main` accepts an argv list (used by tests) so we don't
    # need to re-declare any flags here.
    from qatlas.client import mineru as _mineru

    # Patch prog name on the underlying parser so --help shows
    # "qatlas contrib mineru" rather than "qatlas mineru".
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
