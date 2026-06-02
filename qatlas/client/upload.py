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

    base_url = base_url_from_args(args)
    url = f"{base_url}/api/papers/{args.arxiv_id}/upload-pdf"

    try:
        response = requests.post(
            url,
            files=files,
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

    base_url = base_url_from_args(args)
    url = f"{base_url}/api/papers/{args.arxiv_id}/upload-mineru"

    try:
        response = requests.post(
            url,
            files=files,
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
    print_json(response.json())
    return 0


def _add_arxiv_arg(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "arxiv_id",
        help=(
            "arXiv ID with explicit version suffix, e.g. 'quant-ph/9508027v1' or "
            "'2501.00010v1'. The version is required so the stored filename "
            "matches RAW_DIR layout."
        ),
    )


def build_pdf_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas upload pdf",
        description="Upload a paper PDF to the server.",
    )
    _add_arxiv_arg(parser)
    parser.add_argument("--pdf", required=True, help="Path to the local PDF file")
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing PDF if present on the server",
    )
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_upload_pdf)
    return parser


def build_mineru_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas upload mineru",
        description=(
            "Upload a MinerU result zip (full.md + images/*) for a paper. "
            "The server unzips and stores the markdown + every image under "
            "their respective per-kind buckets."
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
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_upload_mineru)
    return parser


def _print_top_help() -> None:
    print(
        """qatlas upload — push contributed assets to the server

Usage:
  qatlas upload pdf ARXIV_ID --pdf path.pdf [--overwrite]
  qatlas upload mineru ARXIV_ID --zip path.zip [--source mineru] [--overwrite]

The arXiv ID must include a version suffix (e.g. quant-ph/9508027v1 or 2501.00010v1).
Use "qatlas upload <pdf|mineru> --help" for full options.

Note: `qatlas upload markdown` was removed in v0.8.0 — use `qatlas upload mineru`
with the raw MinerU result zip instead, so server-side extraction places both
the markdown and its referenced images into their respective buckets."""
    )


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if not argv or argv[0] in {"-h", "--help"}:
        _print_top_help()
        return 0
    subcommand = argv.pop(0)
    if subcommand == "pdf":
        parser = build_pdf_parser()
    elif subcommand == "mineru":
        parser = build_mineru_parser()
    elif subcommand == "markdown":
        print(
            "ERROR: `qatlas upload markdown` was removed in v0.8.0 (breaking change).\n"
            "Use `qatlas upload mineru ARXIV_ID --zip path.zip` to push the full\n"
            "MinerU result bundle (markdown + images). See CHANGELOG for the\n"
            "upgrade path.",
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

