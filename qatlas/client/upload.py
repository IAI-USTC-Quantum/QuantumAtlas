"""``qatlas upload`` — push contributed PDFs or MinerU bundles to a server.

Subcommands::

    qatlas upload pdf ARXIV_ID --pdf path.pdf [--overwrite]
    qatlas upload mineru ARXIV_ID --zip path.zip [--source mineru] [--overwrite]

The ARXIV_ID must include a version suffix (``vN``) so the stored filename
matches the RAW_DIR layout. Old style (pre-Apr 2007) requires a category
prefix, e.g. ``quant-ph/9508027v1``. New style is ``YYMM.NNNNNvN`` such as
``2501.00010v1``.

The client computes a sha256 of the file before uploading and sends
``?expected_sha256=<hex>`` so the server can detect in-transit corruption
**before** any object-store write. Same hash is what the server stores
as ``x-amz-meta-sha256`` to make subsequent re-uploads of identical
content a 200-OK no-op instead of the legacy 409.

The ``upload mineru`` subcommand expects the **raw MinerU result zip**
(exactly as returned by ``full_zip_url``). Server-side, the zip is opened,
``full.md`` lands in the markdown bucket, and every ``images/<name>`` lands
in the images bucket under the same ``<yymm>/<stem>/`` prefix. This
replaces v0.7.x's ``upload markdown`` (which only accepted a single .md
file and silently dropped images).

Paper metadata (title / authors / abstract / DOI / citations) is sourced
upstream from OpenAlex into the Neo4j catalog as of v0.7.0 — the upload
endpoint no longer accepts a ``metadata`` JSON sibling.
"""

from __future__ import annotations

import argparse
import hashlib
import sys
from pathlib import Path
from typing import Any
from urllib.parse import quote

import requests

from qatlas.client._common import (
    add_common_http_args,
    auth_headers,
    base_url_from_args,
    check_response_version,
    client_version_headers,
    print_json,
    request_verify,
    run_with_request_errors,
)


_SHA256_CHUNK = 1 << 20  # 1 MiB; balances syscalls vs. memory.


def _sha256_hex(path: Path) -> str:
    """Stream a file through sha256, returning the lowercase hex digest.

    We read in 1 MiB chunks so the 100 MiB PDF cap (and 200 MiB mineru-zip
    cap) never balloon RAM.
    """
    h = hashlib.sha256()
    with path.open("rb") as fh:
        while True:
            chunk = fh.read(_SHA256_CHUNK)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()


def _http_error_exit(response: requests.Response) -> int:
    body = response.text or response.reason or ""
    print(
        f"Upload failed: HTTP {response.status_code} {response.reason}\n{body}",
        file=sys.stderr,
    )
    return 1


def cmd_upload_pdf(args: argparse.Namespace) -> int:
    pdf_path = Path(args.pdf).expanduser()
    if not pdf_path.is_file():
        print(f"PDF not found: {pdf_path}", file=sys.stderr)
        return 1
    pdf_sha = _sha256_hex(pdf_path)

    files: dict[str, tuple[str, Any, str]] = {
        "pdf": (pdf_path.name, pdf_path.open("rb"), "application/pdf"),
    }

    params: dict[str, str] = {"expected_sha256": pdf_sha}
    if args.overwrite:
        params["overwrite"] = "true"
    if getattr(args, "verify", None) == "strict":
        params["verify"] = "strict"

    data = _doi_verify_form_data(args)

    base_url = base_url_from_args(args)
    # DOI suffixes can contain '/', '?', '#', etc. — percent-encode
    # the path segment so the server's router sees the full id verbatim.
    url = f"{base_url}/api/papers/{quote(args.arxiv_id, safe='')}/upload-pdf"

    try:
        response = requests.post(
            url,
            files=files,
            data=data or None,
            params=params,
            headers={**auth_headers(args), **client_version_headers()},
            timeout=args.request_timeout,
            verify=request_verify(args),
        )
    finally:
        files["pdf"][1].close()

    check_response_version(response, write=True)

    if not response.ok:
        return _http_error_exit(response)
    _emit_verification_header(response)
    print_json(response.json())
    return 0


def cmd_upload_mineru(args: argparse.Namespace) -> int:
    zip_path = Path(args.zip).expanduser()
    if not zip_path.is_file():
        print(f"MinerU zip not found: {zip_path}", file=sys.stderr)
        return 1
    # Cheap sanity check: zip magic bytes. Saves a round-trip when the
    # user accidentally passed a .md or .pdf.
    with zip_path.open("rb") as fh:
        head = fh.read(4)
    if not head.startswith(b"PK"):
        print(
            f"Not a zip archive (missing PK signature): {zip_path}",
            file=sys.stderr,
        )
        return 1
    zip_sha = _sha256_hex(zip_path)

    files = {
        "mineru_zip": (zip_path.name, zip_path.open("rb"), "application/zip"),
    }
    params: dict[str, str] = {"expected_sha256": zip_sha}
    if args.overwrite:
        params["overwrite"] = "true"
    if args.source:
        params["source"] = args.source
    if getattr(args, "verify", None) == "strict":
        params["verify"] = "strict"

    data = _doi_verify_form_data(args)

    base_url = base_url_from_args(args)
    # DOI suffixes can contain '/', '?', '#', etc. — percent-encode
    # the path segment so the server's router sees the full id verbatim.
    url = f"{base_url}/api/papers/{quote(args.arxiv_id, safe='')}/upload-mineru"

    try:
        response = requests.post(
            url,
            files=files,
            data=data or None,
            params=params,
            headers={**auth_headers(args), **client_version_headers()},
            timeout=args.request_timeout,
            verify=request_verify(args),
        )
    finally:
        files["mineru_zip"][1].close()

    check_response_version(response, write=True)

    if not response.ok:
        return _http_error_exit(response)
    _emit_verification_header(response)
    print_json(response.json())
    return 0


def _add_arxiv_arg(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "arxiv_id",
        metavar="ID",
        help=(
            "arXiv ID with explicit version suffix (e.g. 'quant-ph/9508027v1' or "
            "'2501.00010v1'), OR a DOI (e.g. '10.1103/PhysRevLett.123.070501') to "
            "contribute a published version. The arXiv version is required so the "
            "stored filename matches RAW_DIR layout; DOIs are stored under a "
            "separate 'doi/' namespace."
        ),
    )


def _add_doi_verify_args(parser: argparse.ArgumentParser) -> None:
    """Add --title / --authors / --verify shared by the DOI-capable uploaders.

    These flags are ignored on arXiv uploads (the server's arXiv path doesn't
    cross-check upstream metadata) and honoured on the DOI path (where the
    server cross-checks them against OpenAlex).
    """
    parser.add_argument(
        "--title",
        help=(
            "DOI uploads only: expected paper title. The server verifies it "
            "against the DOI's OpenAlex metadata and records the outcome."
        ),
    )
    parser.add_argument(
        "--authors",
        help=(
            "DOI uploads only: expected authors, semicolon-separated "
            "(e.g. 'Harrow; Hassidim; Lloyd'). Verified against OpenAlex."
        ),
    )
    parser.add_argument(
        "--verify",
        choices=["warn", "strict"],
        default="warn",
        help=(
            "DOI uploads only: 'warn' (default) records a metadata mismatch but "
            "still uploads; 'strict' rejects a mismatch / unknown DOI with 409."
        ),
    )


def _doi_verify_form_data(args: argparse.Namespace) -> dict[str, str]:
    """Build the multipart form fields (title / authors) for the DOI path.

    Returns an empty dict when the caller supplied neither; callers should
    pass `data=data or None` so the multipart body is omitted entirely for
    arXiv uploads.
    """
    data: dict[str, str] = {}
    if getattr(args, "title", None):
        data["title"] = args.title
    if getattr(args, "authors", None):
        data["authors"] = args.authors
    return data


def _emit_verification_header(response: requests.Response) -> None:
    """Print the X-QAtlas-Verification header (if present) to stderr.

    The server emits this on the DOI path to tell the caller what the
    metadata cross-check decided (matched / mismatch / unknown-DOI). It's
    useful to surface because the JSON body only carries the upload status;
    the verification result is purely a response-header signal.
    """
    verification = response.headers.get("X-QAtlas-Verification")
    if verification:
        print(f"DOI metadata verification: {verification}", file=sys.stderr)


def build_pdf_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas upload pdf",
        description="Upload a paper PDF to the server (by arXiv ID or DOI).",
    )
    _add_arxiv_arg(parser)
    parser.add_argument("--pdf", required=True, help="Path to the local PDF file")
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing PDF if present on the server",
    )
    _add_doi_verify_args(parser)
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_upload_pdf)
    return parser


def build_mineru_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas upload mineru",
        description=(
            "Upload a MinerU result zip (full.md + images/*) for a paper "
            "(by arXiv ID or DOI). The server unzips and stores the markdown "
            "+ every image under their respective per-kind buckets. "
            "DOI uploads honour --title / --authors / --verify for OpenAlex "
            "metadata cross-checking (arXiv uploads ignore them)."
        ),
    )
    _add_arxiv_arg(parser)
    parser.add_argument(
        "--zip",
        required=True,
        help=(
            "Path to the local MinerU result zip (the file MinerU's "
            "full_zip_url points at, downloaded verbatim)."
        ),
    )
    parser.add_argument(
        "--source",
        help=(
            "Tool / pipeline that produced the bundle (recorded in the "
            "audit log), e.g. 'mineru-client-v0.8'."
        ),
    )
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing markdown / images if present on the server",
    )
    _add_doi_verify_args(parser)
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_upload_mineru)
    return parser


def _print_top_help() -> None:
    print(
        """qatlas upload — push contributed assets to the server

⚠️  DEPRECATED in v0.19.0 — use `qatlas contrib pdf` instead.
The `qatlas upload` entry point will be removed in a future release.

Usage:
  qatlas upload pdf ARXIV_ID --pdf path.pdf [--overwrite]
      → equivalent to `qatlas contrib pdf ARXIV_ID --pdf path.pdf`

The arXiv ID must include a version suffix (e.g. quant-ph/9508027v1 or 2501.00010v1).
Use "qatlas upload pdf --help" for full options.

Notes:
  * `qatlas upload markdown` was removed in v0.8.0 — use `qatlas contrib mineru`
    to run MinerU locally (which uploads markdown + images automatically).
  * `qatlas upload mineru` (the direct-zip upload subcommand) was removed in
    v0.19.0 — all MinerU pushes must now go through `qatlas contrib mineru`
    so the same path always handles claim/lease/upload as one unit."""
    )


def _emit_deprecation_warning(new_cmd: str) -> None:
    """Tell the user the entry point moved, but don't abort. We emit
    to stderr so scripts that pipe stdout to a file (e.g. `qatlas upload
    pdf … | jq ...`) still see the warning interactively.
    """
    print(
        f"⚠️  `qatlas upload` is deprecated since v0.19.0; use `{new_cmd}` instead. "
        "This entry point will be removed in a future release.",
        file=sys.stderr,
    )


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if not argv or argv[0] in {"-h", "--help"}:
        _print_top_help()
        return 0
    subcommand = argv.pop(0)
    if subcommand == "pdf":
        _emit_deprecation_warning("qatlas contrib pdf")
        parser = build_pdf_parser()
    elif subcommand == "mineru":
        print(
            "ERROR: `qatlas upload mineru` was removed in v0.19.0.\n"
            "The direct-zip upload path was a parallel surface to the contributor\n"
            "MinerU runner and led to inconsistent claim/lease state. All MinerU\n"
            "uploads now go through:\n"
            "    qatlas contrib mineru [ARXIV_ID]\n"
            "    qatlas contrib mineru --watch\n"
            "which handles claim, MinerU run, and upload as one unit.",
            file=sys.stderr,
        )
        return 2
    elif subcommand == "markdown":
        print(
            "ERROR: `qatlas upload markdown` was removed in v0.8.0 (breaking change).\n"
            "Use `qatlas contrib mineru [ARXIV_ID]` to run MinerU locally — it uploads\n"
            "the markdown and images automatically.",
            file=sys.stderr,
        )
        return 2
    else:
        print(f"unknown upload subcommand: {subcommand!r}", file=sys.stderr)
        _print_top_help()
        return 2
    args = parser.parse_args(argv)
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())

