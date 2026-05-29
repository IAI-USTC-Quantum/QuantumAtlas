"""``qatlas upload`` — push contributed PDFs or parsed Markdown to a server.

Subcommands::

    qatlas upload pdf ARXIV_ID --pdf path.pdf [--metadata path.json] [--overwrite]
    qatlas upload markdown ARXIV_ID --markdown path.md [--source mineru] [--overwrite]

The ARXIV_ID must include a version suffix (``vN``) so the stored filename
matches the RAW_DIR layout. Old style (pre-Apr 2007) requires a category
prefix, e.g. ``quant-ph/9508027v1``. New style is ``YYMM.NNNNNvN`` such as
``2501.00010v1``.

The client computes a sha256 of every file before uploading and sends
``?expected_sha256=<hex>`` (and ``?expected_metadata_sha256`` for the
metadata JSON sibling) so the server can detect in-transit corruption
**before** any object-store write. Same hash is what the server stores
as ``x-amz-meta-sha256`` to make subsequent re-uploads of identical
content a 200-OK no-op instead of the legacy 409.
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
    print_json,
    request_verify,
    run_with_request_errors,
)


_SHA256_CHUNK = 1 << 20  # 1 MiB; balances syscalls vs. memory.


def _sha256_hex(path: Path) -> str:
    """Stream a file through sha256, returning the lowercase hex digest.

    We read in 1 MiB chunks so the 100 MiB PDF cap never balloons RAM.
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
    metadata_handle = None
    metadata_sha: str | None = None
    if args.metadata:
        metadata_path = Path(args.metadata).expanduser()
        if not metadata_path.is_file():
            print(f"Metadata JSON not found: {metadata_path}", file=sys.stderr)
            return 1
        metadata_sha = _sha256_hex(metadata_path)
        metadata_handle = metadata_path.open("rb")
        files["metadata"] = (metadata_path.name, metadata_handle, "application/json")

    params: dict[str, str] = {"expected_sha256": pdf_sha}
    if metadata_sha is not None:
        params["expected_metadata_sha256"] = metadata_sha
    if args.overwrite:
        params["overwrite"] = "true"

    base_url = base_url_from_args(args)
    url = f"{base_url}/api/papers/{args.arxiv_id}/upload-pdf"

    try:
        response = requests.post(
            url,
            files=files,
            params=params,
            headers=auth_headers(args),
            timeout=args.request_timeout,
            verify=request_verify(args),
        )
    finally:
        files["pdf"][1].close()
        if metadata_handle is not None:
            metadata_handle.close()

    if not response.ok:
        return _http_error_exit(response)
    print_json(response.json())
    return 0


def cmd_upload_markdown(args: argparse.Namespace) -> int:
    md_path = Path(args.markdown).expanduser()
    if not md_path.is_file():
        print(f"Markdown not found: {md_path}", file=sys.stderr)
        return 1
    md_sha = _sha256_hex(md_path)

    files = {
        "markdown": (md_path.name, md_path.open("rb"), "text/markdown"),
    }
    params: dict[str, str] = {"expected_sha256": md_sha}
    if args.overwrite:
        params["overwrite"] = "true"
    if args.source:
        params["source"] = args.source

    base_url = base_url_from_args(args)
    url = f"{base_url}/api/papers/{args.arxiv_id}/upload-markdown"

    try:
        response = requests.post(
            url,
            files=files,
            params=params,
            headers=auth_headers(args),
            timeout=args.request_timeout,
            verify=request_verify(args),
        )
    finally:
        files["markdown"][1].close()

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
        description="Upload a paper PDF (and optionally its arXiv metadata JSON) to the server.",
    )
    _add_arxiv_arg(parser)
    parser.add_argument("--pdf", required=True, help="Path to the local PDF file")
    parser.add_argument(
        "--metadata",
        help="Optional path to the arXiv metadata JSON (title, authors, abstract, ...)",
    )
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing PDF/metadata if present on the server",
    )
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_upload_pdf)
    return parser


def build_markdown_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas upload markdown",
        description="Upload parsed Markdown (e.g. MinerU output) for a paper to the server.",
    )
    _add_arxiv_arg(parser)
    parser.add_argument("--markdown", required=True, help="Path to the local markdown file")
    parser.add_argument(
        "--source",
        help="Tool that produced the markdown (recorded in the audit log), e.g. 'mineru'",
    )
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing markdown if present on the server",
    )
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_upload_markdown)
    return parser


def _print_top_help() -> None:
    print(
        """qatlas upload — push contributed assets to the server

Usage:
  qatlas upload pdf ARXIV_ID --pdf path.pdf [--metadata path.json] [--overwrite]
  qatlas upload markdown ARXIV_ID --markdown path.md [--source mineru] [--overwrite]

The arXiv ID must include a version suffix (e.g. quant-ph/9508027v1 or 2501.00010v1).
Use "qatlas upload <pdf|markdown> --help" for full options."""
    )


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if not argv or argv[0] in {"-h", "--help"}:
        _print_top_help()
        return 0
    subcommand = argv.pop(0)
    if subcommand == "pdf":
        parser = build_pdf_parser()
    elif subcommand == "markdown":
        parser = build_markdown_parser()
    else:
        print(f"unknown upload subcommand: {subcommand!r}", file=sys.stderr)
        _print_top_help()
        return 2
    args = parser.parse_args(argv)
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())
