"""HTTP upload helpers behind ``qatlas contrib`` — push contributed PDFs
or MinerU bundles to a server.

This module is a library, not a CLI entry point. The contributor surface
lives in :mod:`qatlas.client.contrib`:

* ``qatlas contrib pdf ARXIV_ID|DOI --pdf path.pdf [--overwrite]``
  → :func:`build_pdf_parser` / :func:`cmd_upload_pdf`
* ``qatlas contrib mineru DOI --zip path.zip [--source ...] [--overwrite]``
  → :func:`build_mineru_parser` / :func:`cmd_upload_mineru`

The ID must include a version suffix (``vN``) so the stored filename
matches the RAW_DIR layout. Old style (pre-Apr 2007) requires a category
prefix, e.g. ``quant-ph/9508027v1``. New style is ``YYMM.NNNNNvN`` such as
``2501.00010v1``. A DOI (``10.<registrant>/<suffix>``) may stand in for the
arXiv ID to contribute a published version.

The client computes a sha256 of the file before uploading and sends
``?expected_sha256=<hex>`` so the server can detect in-transit corruption
**before** any object-store write. Same hash is what the server stores
as ``x-amz-meta-sha256`` to make subsequent re-uploads of identical
content a 200-OK no-op instead of the legacy 409.

The mineru-zip upload expects the **raw MinerU result zip** (exactly as
returned by ``full_zip_url``). Server-side the zip is opened, ``full.md``
lands in the markdown bucket, and every ``images/<name>`` lands in the
images bucket under the same ``<yymm>/<stem>/`` prefix.

Direct-zip MinerU upload is **DOI-only**: arXiv papers go through the local
MinerU runner (``qatlas contrib mineru [ARXIV_ID]``) so claim, run, and
upload stay one unit. DOIs aren't in the needs-mineru queue, so direct-zip
is the only contributor path for DOI-only papers.

Paper metadata (title / authors / abstract / DOI / citations) is always
sourced from upstream (OpenAlex for DOI uploads, arXiv/OpenAlex sync for
arXiv uploads) — the client and server reject any attempt to override it
from contributor input. ``--verify`` only controls *policy* (whether the
server requires a successful OpenAlex lookup before accepting the upload).
"""

from __future__ import annotations

import argparse
import hashlib
import re
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

    base_url = base_url_from_args(args)
    # DOI suffixes can contain '/', '?', '#', etc. — percent-encode
    # the path segment so the server's router sees the full id verbatim.
    url = f"{base_url}/api/papers/{quote(args.arxiv_id, safe='')}/upload-pdf"

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

    base_url = base_url_from_args(args)
    # DOI suffixes can contain '/', '?', '#', etc. — percent-encode
    # the path segment so the server's router sees the full id verbatim.
    url = f"{base_url}/api/papers/{quote(args.arxiv_id, safe='')}/upload-mineru"

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
    """Add ``--verify`` shared by the DOI-capable uploaders.

    DOI uploads enrich the catalog with title/authors/linked-arxiv-id
    fetched from OpenAlex; the contributor cannot override that metadata.
    ``--verify`` only chooses what the server does when OpenAlex cannot
    resolve the DOI: ``warn`` records the failure and proceeds, ``strict``
    rejects with 409 (doi-not-found) or 503 (metadata-unavailable /
    unconfigured). The flag is a no-op for arXiv uploads (the server's
    arXiv path resolves metadata through a separate sync pipeline).
    """
    parser.add_argument(
        "--verify",
        choices=["warn", "strict"],
        default="warn",
        help=(
            "DOI uploads only: 'warn' (default) records when OpenAlex "
            "cannot resolve the DOI but still uploads; 'strict' rejects "
            "(409 doi-not-found, 503 metadata-unavailable). The server "
            "always sources title/authors from OpenAlex; the contributor "
            "cannot override that metadata."
        ),
    )


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
        prog="qatlas contrib pdf",
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
        prog="qatlas contrib mineru",
        description=(
            "Upload a MinerU result zip (full.md + images/*) for a paper "
            "(by arXiv ID or DOI). The server unzips and stores the markdown "
            "+ every image under their respective per-kind buckets. "
            "DOI uploads honour --verify to choose strict vs warn policy "
            "when OpenAlex cannot resolve the DOI; title/authors are always "
            "sourced from OpenAlex (never from the contributor)."
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


# DOI prefix shape per IANA assignments: "10.<4-9 digits>/<non-empty>".
# Mirrors the server-side regex in internal/paperassets/doi.go and
# internal/routes/papers.go (doiPrefixRE) so client+server agree on what
# qualifies as a DOI for routing decisions. The client side only needs
# the prefix check — the server still validates the full DOI shape +
# normalizes URL prefixes via paperassets.ValidateDOI before storing.
_DOI_PREFIX_RE = re.compile(r"^10\.\d{4,9}/")


def _looks_like_doi(value: str) -> bool:
    """Return True if `value` syntactically looks like a bare DOI.

    Used by `qatlas contrib mineru --zip` to gate the surviving DOI
    direct-zip path against the killed arxiv direct-zip path. URL-prefixed DOIs
    (e.g. ``https://doi.org/10.x/y``, ``doi:10.x/y``) are stripped and
    re-checked so a contributor pasting a full link still hits the DOI
    branch.
    """
    if not value:
        return False
    stripped = value.strip().lower()
    for prefix in (
        "https://doi.org/",
        "http://doi.org/",
        "https://dx.doi.org/",
        "http://dx.doi.org/",
        "doi.org/",
        "dx.doi.org/",
        "doi:",
    ):
        if stripped.startswith(prefix):
            stripped = stripped[len(prefix):].lstrip()
            break
    return bool(_DOI_PREFIX_RE.match(stripped))
