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

    qatlas mineru --watch               # daemon mode: loop forever, sleeping
                                        # --watch-interval (default 300s)
                                        # between batches. SIGINT / SIGTERM
                                        # gracefully release any in-flight
                                        # claim before exiting.

Concurrency::

    Multiple contributors can run ``qatlas mineru`` in parallel; the server
    issues atomic per-paper claims (default 30-minute lease) so two clients
    never burn MinerU quota on the same paper. If a claim is already held by
    someone else the client silently skips and moves to the next candidate.

Push path (since v0.8.0)::

    We send the *entire* MinerU result zip to ``POST upload-mineru`` rather
    than extracting just ``full.md`` ourselves. The server then unzips, writes
    ``full.md`` to the markdown bucket, and writes every ``images/<name>``
    to the images bucket. Pre-v0.8.0 the client did the extraction and only
    pushed the .md, silently dropping every image — a regression that's now
    fixed.
"""

from __future__ import annotations

import argparse
import signal
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
    check_response_version,
    client_version_headers,
    print_json,
    request_verify,
    run_with_request_errors,
)
from qatlas.parser.mineru_client import MinerUClient
from qatlas.config import ServerConfig


# ---------------------------------------------------------------------------
# Cooperative shutdown
# ---------------------------------------------------------------------------
#
# `--watch` mode runs forever; a single Ctrl-C should release any in-flight
# claim (so it doesn't dangle for the rest of the 30-minute TTL) before the
# process exits. A second Ctrl-C aborts immediately. We use a module-level
# flag set by the SIGINT/SIGTERM handler; the work loop checks it between
# papers and after MinerU polls.

_SHUTDOWN_REQUESTED = False


def _install_signal_handlers() -> None:
    def _handler(signum, _frame):
        global _SHUTDOWN_REQUESTED
        if _SHUTDOWN_REQUESTED:
            # Second signal — exit hard right now, don't wait for graceful
            # cleanup. The first signal already triggered claim release.
            _print_err(f"Second {signal.Signals(signum).name} — aborting now.")
            raise SystemExit(130)
        _SHUTDOWN_REQUESTED = True
        _print_err(
            f"Received {signal.Signals(signum).name} — finishing current paper "
            "then exiting. Press again to abort immediately."
        )

    signal.signal(signal.SIGINT, _handler)
    signal.signal(signal.SIGTERM, _handler)


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
            headers={**headers, **client_version_headers()},
            timeout=request_timeout,
            verify=verify,
        )
    except requests.RequestException as exc:
        return None, f"claim request errored: {exc}"
    check_response_version(resp, write=True)
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
            headers={**headers, **client_version_headers()},
            timeout=request_timeout,
            verify=verify,
        )
    except requests.RequestException as exc:
        _print_err(f"warning: could not release claim for {arxiv_id}: {exc}")


def _run_mineru_to_zip(
    *,
    config: ServerConfig,
    pdf_url: str,
    no_cache: bool,
) -> Optional[Path]:
    """Submit pdf_url to MinerU, poll until done, download the entire result zip.

    Returns the path to the downloaded zip on disk, or None on failure. The
    zip is what gets POSTed verbatim to ``upload-mineru``; the server side
    unpacks it and stores ``full.md`` plus every ``images/<name>``.

    Previously this helper extracted only ``full.md`` (via
    ``download_markdown_from_zip``) — that silently dropped images. The new
    flow preserves the whole bundle.
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
        if _SHUTDOWN_REQUESTED:
            _print_err("Shutdown requested mid-poll; abandoning MinerU task.")
            return None
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
    zip_path = workdir / "mineru-result.zip"
    client.download_full_zip(full_zip_url, zip_path)
    _print_err(f"Downloaded MinerU zip -> {zip_path} ({zip_path.stat().st_size} bytes)")
    return zip_path


def _upload_mineru_zip(
    *,
    base_url: str,
    arxiv_id: str,
    zip_path: Path,
    overwrite: bool,
    request_timeout: float,
    verify: bool,
    headers: dict[str, str],
) -> tuple[bool, Any]:
    """POST the whole MinerU zip to /api/papers/{id}/upload-mineru.

    The server extracts ``full.md`` and every ``images/<name>`` and writes
    them to their respective per-kind buckets (markdown / images) under the
    paper's canonical key. Conditional create-only PUT semantics apply per
    object so multiple contributors racing on the same paper still get
    consistent state.
    """
    params: dict[str, str] = {"source": "mineru"}
    if overwrite:
        params["overwrite"] = "true"
    with zip_path.open("rb") as fh:
        files = {"mineru_zip": (zip_path.name, fh, "application/zip")}
        resp = requests.post(
            f"{base_url}/api/papers/{arxiv_id}/upload-mineru",
            files=files,
            params=params,
            headers={**headers, **client_version_headers()},
            timeout=request_timeout,
            verify=verify,
        )
    check_response_version(resp, write=True)
    if not resp.ok:
        return False, _http_error(resp, f"MinerU upload for {arxiv_id}")
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

    zip_path: Optional[Path] = None
    try:
        zip_path = _run_mineru_to_zip(
            config=config,
            pdf_url=pdf_url,
            no_cache=args.no_cache,
        )
        if zip_path is None:
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
            _print_err(f"--no-push set; claim released, zip left at {zip_path}")
            _release_claim(
                base_url,
                arxiv_id,
                claim_id,
                request_timeout=args.request_timeout,
                verify=verify,
                headers=headers,
            )
            return 0

        ok, payload = _upload_mineru_zip(
            base_url=base_url,
            arxiv_id=arxiv_id,
            zip_path=zip_path,
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


def _drain_queue_once(
    args: argparse.Namespace,
    base_url: str,
    config: ServerConfig,
    verify: bool,
    headers: dict[str, str],
) -> tuple[int, int]:
    """Run one queue-drain pass. Returns (processed_count, failure_count).

    Splits out from cmd_mineru so --watch can call it in a loop.
    """
    list_resp = requests.get(
        f"{base_url}/api/papers/needs-mineru",
        params={"limit": args.max},
        headers={**headers, **client_version_headers()},
        timeout=args.request_timeout,
        verify=verify,
    )
    check_response_version(list_resp, write=False)
    if not list_resp.ok:
        _print_err(_http_error(list_resp, "needs-mineru list"))
        return 0, 1
    queue = list_resp.json()
    candidates = queue.get("papers") or []
    if not candidates:
        _print_err(
            "Nothing to do — no PDFs in RAW_DIR are waiting for MinerU. "
            f"(unclaimed={queue.get('total_unclaimed')}, claimed={queue.get('total_claimed')})"
        )
        return 0, 0
    _print_err(
        f"Queue: {len(candidates)} candidate(s) (unclaimed total={queue.get('total_unclaimed')})"
    )

    processed = 0
    failures = 0
    for paper in candidates:
        if _SHUTDOWN_REQUESTED:
            break
        rc = _process_one(args, base_url, config, paper["arxiv_id"], verify, headers)
        processed += 1
        if rc != 0:
            failures += 1
            if not args.continue_on_error:
                break
    return processed, failures


def cmd_mineru(args: argparse.Namespace) -> int:
    config = ServerConfig.from_env()
    if not config.mineru_api_token:
        _print_err("MINERU_API_TOKEN must be set in your local .env to run MinerU client-side.")
        return 1

    base_url = base_url_from_args(args)
    verify = request_verify(args)
    headers = auth_headers(args)

    # Single-paper mode short-circuits the queue path.
    if args.arxiv_id:
        return _process_one(args, base_url, config, args.arxiv_id, verify, headers)

    if args.watch:
        _install_signal_handlers()
        interval = max(int(args.watch_interval), 1)
        _print_err(
            f"--watch enabled; polling needs-mineru every {interval}s. "
            "Send SIGINT/SIGTERM (Ctrl-C) to stop after current paper."
        )
        consecutive_empty = 0
        while not _SHUTDOWN_REQUESTED:
            processed, failures = _drain_queue_once(args, base_url, config, verify, headers)
            if processed == 0:
                consecutive_empty += 1
                if consecutive_empty == 1:
                    _print_err("Queue empty. Will keep polling.")
            else:
                consecutive_empty = 0
                _print_err(
                    f"Batch done: {processed} processed, {failures} failures. "
                    f"Sleeping {interval}s before next poll."
                )
            # Sleep in 1-second slices so SIGINT can wake us up promptly
            # rather than waiting for the full interval.
            for _ in range(interval):
                if _SHUTDOWN_REQUESTED:
                    break
                time.sleep(1)
        _print_err("Watch loop exiting cleanly.")
        return 0

    # Default queue mode: one pass, then exit.
    processed, failures = _drain_queue_once(args, base_url, config, verify, headers)
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
        help="Replace existing markdown / images on the server (rare; claim only succeeds when no md exists).",
    )
    parser.add_argument(
        "--no-push",
        action="store_true",
        help=(
            "Run MinerU but skip uploading; leave the result zip in a temp directory and "
            "release the claim immediately."
        ),
    )
    parser.add_argument(
        "--watch",
        action="store_true",
        help=(
            "Daemon mode: after each queue drain, sleep --watch-interval seconds "
            "and poll again. Implies --continue-on-error. SIGINT/SIGTERM exit "
            "cleanly after the current paper."
        ),
    )
    parser.add_argument(
        "--watch-interval",
        type=int,
        default=300,
        help="Daemon mode poll interval in seconds between batches (default 300 = 5 min).",
    )
    add_common_http_args(parser)
    parser.set_defaults(func=cmd_mineru)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(sys.argv[1:] if argv is None else argv)
    # --watch implies --continue-on-error; otherwise a single 5xx would
    # exit the daemon and defeat the whole point.
    if getattr(args, "watch", False):
        args.continue_on_error = True
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())

