"""``qatlas mineru`` — run MinerU parsing locally and push the result to the server.

Designed so the server's ``RAW_DIR`` is the single source of truth for which
papers are eligible. Contributors never feed arbitrary PDF URLs into MinerU:
the PDF must already be on the server (uploaded via ``qatlas upload pdf`` or
fetched by ``qatlas ingest``), and the share URL the server hands back is what
gets passed to MinerU.

Modes::

    qatlas mineru                       # queue mode: pick up to --max papers
                                        # that have PDF but no markdown yet,
                                        # claim each, process, upload, release.

    qatlas mineru quant-ph/9508027v1    # single mode: claim and process one
                                        # specific paper.

Concurrency::

    Multiple contributors can run ``qatlas mineru`` in parallel; the server
    issues atomic per-paper claims (default 30-minute lease) so two clients
    never burn MinerU quota on the same paper. If a claim is already held by
    someone else the client silently skips and moves to the next candidate.
"""

from __future__ import annotations

import argparse
import sys
import tempfile
import time
from pathlib import Path
from typing import Any, Optional

import requests

from qatlas.client._common import (
    add_common_http_args,
    auth_headers,
    base_url_from_args,
    print_json,
    request_verify,
    run_with_request_errors,
)
from qatlas.parser.mineru_client import MinerUClient
from qatlas.config import ServerConfig


def _print_err(msg: str) -> None:
    print(msg, file=sys.stderr)


def _http_error(response: requests.Response, what: str) -> str:
    body = response.text or response.reason or ""
    return f"{what} failed: HTTP {response.status_code} {response.reason}\n{body}"


def _claim_one(
    base_url: str,
    arxiv_id: str,
    *,
    request_timeout: float,
    verify: bool,
    headers: dict[str, str],
    ttl_seconds: Optional[int],
) -> tuple[Optional[dict[str, Any]], Optional[str]]:
    """Try to claim one paper. Returns (claim_payload, skip_reason).

    On 201 returns the claim payload and ``skip_reason=None``.
    On 404 (no PDF) / 409 (already claimed or already has markdown) returns
    ``(None, "<reason text>")`` so the caller can skip silently.
    On any other error returns ``(None, "<error text>")`` — caller decides
    whether to continue with the next candidate.
    """
    params: dict[str, Any] = {}
    if ttl_seconds is not None:
        params["ttl_seconds"] = ttl_seconds
    try:
        resp = requests.post(
            f"{base_url}/api/papers/{arxiv_id}/mineru-claim",
            params=params or None,
            headers=headers,
            timeout=request_timeout,
            verify=verify,
        )
    except requests.RequestException as exc:
        return None, f"claim request errored: {exc}"
    if resp.status_code == 201:
        return resp.json(), None
    if resp.status_code in (404, 409):
        try:
            detail = resp.json().get("detail")
        except ValueError:
            detail = resp.text
        return None, f"skip (HTTP {resp.status_code}): {detail}"
    return None, _http_error(resp, f"Claim {arxiv_id}")


def _release_claim(
    base_url: str,
    arxiv_id: str,
    claim_id: str,
    *,
    request_timeout: float,
    verify: bool,
    headers: dict[str, str],
) -> None:
    try:
        requests.delete(
            f"{base_url}/api/papers/{arxiv_id}/mineru-claim/{claim_id}",
            headers=headers,
            timeout=request_timeout,
            verify=verify,
        )
    except requests.RequestException as exc:
        _print_err(f"warning: could not release claim for {arxiv_id}: {exc}")


def _run_mineru_to_markdown(
    *,
    config: ServerConfig,
    pdf_url: str,
    no_cache: bool,
) -> Optional[Path]:
    """Submit pdf_url to MinerU, poll until done, download full.md to a tempfile.

    Returns the path to the downloaded markdown, or None on failure.
    """
    client = MinerUClient(
        config.mineru_api_token,
        base_url=config.mineru_api_base_url,
    )
    mineru_task_id = client.submit_url_task(
        url=pdf_url,
        model_version=config.mineru_model_version,
        language=config.mineru_language,
        enable_formula=config.mineru_enable_formula,
        enable_table=config.mineru_enable_table,
        is_ocr=config.mineru_is_ocr,
        no_cache=no_cache,
    )
    _print_err(f"MinerU task id: {mineru_task_id}")

    poll_interval = max(float(config.mineru_poll_interval), 1.0)
    deadline = time.monotonic() + float(config.mineru_timeout)
    full_zip_url: Optional[str] = None
    while time.monotonic() < deadline:
        state_payload = client.get_task(mineru_task_id)
        state = state_payload.get("state")
        _print_err(f"MinerU state: {state}")
        if state == "done":
            full_zip_url = state_payload.get("full_zip_url")
            if not full_zip_url:
                _print_err("MinerU task finished but did not return full_zip_url")
                return None
            break
        if state == "failed":
            err = state_payload.get("err_msg") or state_payload
            _print_err(f"MinerU task failed: {err}")
            return None
        time.sleep(poll_interval)
    else:
        _print_err(f"MinerU task did not finish within MINERU_TIMEOUT={config.mineru_timeout}s")
        return None

    workdir = Path(tempfile.mkdtemp(prefix="qatlas-mineru-"))
    md_path = workdir / "full.md"
    client.download_markdown_from_zip(full_zip_url, md_path)
    _print_err(f"Downloaded MinerU markdown -> {md_path}")
    return md_path


def _upload_markdown(
    *,
    base_url: str,
    arxiv_id: str,
    md_path: Path,
    overwrite: bool,
    request_timeout: float,
    verify: bool,
    headers: dict[str, str],
) -> tuple[bool, Any]:
    params: dict[str, str] = {"source": "mineru"}
    if overwrite:
        params["overwrite"] = "true"
    with md_path.open("rb") as fh:
        files = {"markdown": (md_path.name, fh, "text/markdown")}
        resp = requests.post(
            f"{base_url}/api/papers/{arxiv_id}/upload-markdown",
            files=files,
            params=params,
            headers=headers,
            timeout=request_timeout,
            verify=verify,
        )
    if not resp.ok:
        return False, _http_error(resp, f"Markdown upload for {arxiv_id}")
    return True, resp.json()


def _process_one(
    args: argparse.Namespace,
    base_url: str,
    config: ServerConfig,
    arxiv_id: str,
    verify: bool,
    headers: dict[str, str],
) -> int:
    """Process exactly one arxiv_id. Returns 0 on success/skip, 1 on hard error."""
    _print_err(f"--- {arxiv_id} ---")
    claim, skip = _claim_one(
        base_url,
        arxiv_id,
        request_timeout=args.request_timeout,
        verify=verify,
        headers=headers,
        ttl_seconds=args.ttl_seconds,
    )
    if claim is None:
        _print_err(f"[skip] {arxiv_id}: {skip}")
        # Claim "skip" (409 / 404) is not a hard failure — caller continues.
        return 0 if skip and skip.startswith("skip") else 1

    claim_id = claim["claim_id"]
    pdf_url = claim["pdf_url"]
    _print_err(
        f"Claim acquired for {arxiv_id} (id={claim_id}); submitting {pdf_url} to MinerU."
    )

    md_path: Optional[Path] = None
    try:
        md_path = _run_mineru_to_markdown(
            config=config,
            pdf_url=pdf_url,
            no_cache=args.no_cache,
        )
        if md_path is None:
            _release_claim(
                base_url,
                arxiv_id,
                claim_id,
                request_timeout=args.request_timeout,
                verify=verify,
                headers=headers,
            )
            return 1

        if args.no_push:
            _print_err(f"--no-push set; claim released, markdown left at {md_path}")
            _release_claim(
                base_url,
                arxiv_id,
                claim_id,
                request_timeout=args.request_timeout,
                verify=verify,
                headers=headers,
            )
            return 0

        ok, payload = _upload_markdown(
            base_url=base_url,
            arxiv_id=arxiv_id,
            md_path=md_path,
            overwrite=args.overwrite,
            request_timeout=args.request_timeout,
            verify=verify,
            headers=headers,
        )
        if not ok:
            _print_err(payload)
            _release_claim(
                base_url,
                arxiv_id,
                claim_id,
                request_timeout=args.request_timeout,
                verify=verify,
                headers=headers,
            )
            return 1
        print_json(payload)
        return 0
    except Exception:
        _release_claim(
            base_url,
            arxiv_id,
            claim_id,
            request_timeout=args.request_timeout,
            verify=verify,
            headers=headers,
        )
        raise


def cmd_mineru(args: argparse.Namespace) -> int:
    config = ServerConfig.from_env()
    if not config.mineru_api_token:
        _print_err("MINERU_API_TOKEN must be set in your local .env to run MinerU client-side.")
        return 1

    base_url = base_url_from_args(args)
    verify = request_verify(args)
    headers = auth_headers(args)

    if args.arxiv_id:
        return _process_one(args, base_url, config, args.arxiv_id, verify, headers)

    # Queue mode: iterate the server's needs-mineru list.
    list_resp = requests.get(
        f"{base_url}/api/papers/needs-mineru",
        params={"limit": args.max},
        headers=headers,
        timeout=args.request_timeout,
        verify=verify,
    )
    if not list_resp.ok:
        _print_err(_http_error(list_resp, "needs-mineru list"))
        return 1
    queue = list_resp.json()
    candidates = queue.get("papers") or []
    if not candidates:
        _print_err(
            "Nothing to do — no PDFs in RAW_DIR are waiting for MinerU. "
            f"(unclaimed={queue.get('total_unclaimed')}, claimed={queue.get('total_claimed')})"
        )
        return 0
    _print_err(
        f"Queue mode: {len(candidates)} candidate(s) (unclaimed total={queue.get('total_unclaimed')})"
    )

    failures = 0
    for paper in candidates:
        rc = _process_one(args, base_url, config, paper["arxiv_id"], verify, headers)
        if rc != 0:
            failures += 1
            if not args.continue_on_error:
                return rc
    return 0 if failures == 0 else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas mineru",
        description=(
            "Run MinerU parsing locally with your own MINERU_API_TOKEN against "
            "PDFs that already live in the QuantumAtlas server's RAW_DIR. "
            "Without an arxiv_id, walks the server's needs-mineru queue."
        ),
    )
    parser.add_argument(
        "arxiv_id",
        nargs="?",
        help=(
            "Optional. arXiv ID with explicit version (e.g. 'quant-ph/9508027v1', "
            "'2501.00010v1'). When omitted, queue mode iterates the server's "
            "list of unprocessed papers."
        ),
    )
    parser.add_argument(
        "--max",
        type=int,
        default=10,
        help="Queue mode only: maximum number of papers to process in one run (default 10).",
    )
    parser.add_argument(
        "--continue-on-error",
        action="store_true",
        help="Queue mode only: keep processing the next paper even if one fails.",
    )
    parser.add_argument(
        "--ttl-seconds",
        type=int,
        default=None,
        help=(
            "Claim lease duration in seconds (server default 1800, max 7200). "
            "Use longer leases for big papers or slow MinerU queues."
        ),
    )
    parser.add_argument(
        "--no-cache",
        action="store_true",
        help="Ask MinerU to bypass its server-side cache for this task.",
    )
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing markdown on the server (rare; claim only succeeds when none exists).",
    )
    parser.add_argument(
        "--no-push",
        action="store_true",
        help=(
            "Run MinerU but skip uploading; leave the markdown in a temp directory and "
            "release the claim immediately."
        ),
    )
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_mineru)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(sys.argv[1:] if argv is None else argv)
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())
