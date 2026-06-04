"""``qatlas mineru`` — run MinerU parsing locally and push the result to the server.

The contributor flow is arxiv-only: the server hands back an arxiv.org versioned
URL (stable bytes — arxiv never mutates a published version) and we feed that URL
to MinerU. The server **never** redistributes PDFs back to clients.

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

PDF sha256 verification (since v0.9.0)::

    The claim response carries the sha256 the server stored for that paper's
    PDF (read from RustFS object metadata). We download the arxiv URL, hash
    the bytes, compare to the server's hash, then pass the hash back on
    upload-mineru via ``?pdf_sha256=<hex>``. The server cross-checks against
    its own RustFS metadata one more time. A mismatch at either end aborts
    the upload — better than silently uploading markdown derived from a
    different PDF revision.

    Legacy objects (uploaded before v0.7.0) have no sha256 metadata; in that
    case ``claim.pdf_sha256`` is empty and we skip verification (trust the
    contributor + arxiv URL stability).

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
import hashlib
import os
import shutil
import signal
import sys
import tempfile
import time
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, List, Optional
from urllib.parse import urlparse

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
from qatlas.parser.mineru_client import (
    MAX_BATCH_SIZE,
    BatchFile,
    BatchTaskState,
    MinerUClient,
    MinerUDailyLimitError,
    MinerUError,
    MinerUFatalError,
    MinerURetryableError,
)
from qatlas.config import ServerConfig


_ARXIV_PDF_HOSTS = frozenset({"arxiv.org", "export.arxiv.org"})

# Suffix-match hosts the claim handler is allowed to point us at when
# it signs an S3 presigned URL for our edge's RustFS PDF bucket. Three
# common deployment shapes:
#
#   - "raw.quantum-atlas.ai" — RackNerd-style: dedicated raw.* subdomain
#     in front of the bucket
#   - any host ending in ".quantum-atlas.ai" — flexibility for other
#     edges that may pick a different subdomain
#   - direct IP+port (e.g. "47.102.36.175:9000" on Alibaba) — handled
#     dynamically below via the QATLAS_SERVER_URL host since the same
#     box hosts both API and RustFS public endpoint
_S3_PUBLIC_HOST_SUFFIXES = (".quantum-atlas.ai",)
_S3_PUBLIC_HOSTS = frozenset({"raw.quantum-atlas.ai"})

# In-flight terminal states from MinerU's batch result entries.
_TERMINAL_BATCH_STATES = frozenset({"done", "failed"})

# os.EX_TEMPFAIL is the canonical "try again later" exit code (75 on Unix).
# Windows lacks it; fall back to the same numeric value so CI on either
# platform reports identically.
EXIT_DAILY_LIMIT = getattr(os, "EX_TEMPFAIL", 75)


def _is_arxiv_url(url: str) -> bool:
    """True if `url` points at arxiv.org (legacy whitelist)."""
    try:
        host = (urlparse(url).hostname or "").lower()
    except ValueError:
        return False
    return host in _ARXIV_PDF_HOSTS


def _is_acceptable_pdf_url(url: str, server_url: str) -> bool:
    """Defensive client-side check: refuse to feed MinerU any URL not
    coming from a known-safe source.

    Since v0.15.0 the server prefers handing back a RustFS presigned URL
    (per qatlas/client/mineru.py module docstring); arxiv.org is only
    the fallback when the store can't presign. So three sources are
    OK:

      - arxiv.org / export.arxiv.org           (legacy / fallback)
      - raw.* or .quantum-atlas.ai subdomain   (RackNerd-style edge)
      - the same host:port as QATLAS_SERVER_URL (Alibaba-style edge
        that shares one IP/cert with the API and the public RustFS
        endpoint, e.g. https://47.102.36.175:9000)

    Everything else is treated as a misconfigured / hostile server
    response and the claim is released without firing the URL at
    MinerU. Keeps this CLI from being turned into a generic URL-fetch
    tool by a buggy or compromised server.
    """
    try:
        parsed = urlparse(url)
    except ValueError:
        return False
    host = (parsed.hostname or "").lower()
    if not host:
        return False
    if host in _ARXIV_PDF_HOSTS:
        return True
    if host in _S3_PUBLIC_HOSTS:
        return True
    for suffix in _S3_PUBLIC_HOST_SUFFIXES:
        if host.endswith(suffix):
            return True
    # Same host:port as the configured QATLAS_SERVER_URL — covers
    # IP-based edges (Alibaba) where API and RustFS public endpoint
    # share an IP + port (different ports also OK since edge can pin
    # the public endpoint to e.g. :9000).
    try:
        srv = urlparse(server_url)
    except ValueError:
        srv = None
    if srv:
        srv_host = (srv.hostname or "").lower()
        if srv_host and srv_host == host:
            return True
    return False


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


def _hash_arxiv_pdf(
    pdf_url: str,
    *,
    request_timeout: float,
    verify: bool,
) -> Optional[str]:
    """Stream-download the arxiv PDF and return its sha256 (lowercase hex).

    We don't keep the bytes — MinerU will refetch the same URL itself.
    Returning the hash lets us cross-check with the server's stored RustFS
    metadata: if they disagree, something between arxiv and the server
    drifted (server has a different version cached, arxiv served different
    bytes, etc.) and we should bail rather than upload markdown derived
    from a different revision.

    Returns ``None`` on transport error so the caller can decide whether
    to skip the paper or fail the whole batch.
    """
    try:
        with requests.get(
            pdf_url,
            stream=True,
            timeout=request_timeout,
            verify=verify,
            headers=client_version_headers(),
        ) as resp:
            if not resp.ok:
                _print_err(
                    f"PDF download for hash check failed: HTTP {resp.status_code} {resp.reason}"
                )
                return None
            sha = hashlib.sha256()
            for chunk in resp.iter_content(chunk_size=65536):
                if chunk:
                    sha.update(chunk)
            return sha.hexdigest()
    except requests.RequestException as exc:
        _print_err(f"PDF download for hash check errored: {exc}")
        return None


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
    pdf_sha256: Optional[str] = None,
) -> tuple[bool, Any]:
    """POST the whole MinerU zip to /api/papers/{id}/upload-mineru.

    The server extracts ``full.md`` and every ``images/<name>`` and writes
    them to their respective per-kind buckets (markdown / images) under the
    paper's canonical key. Conditional create-only PUT semantics apply per
    object so multiple contributors racing on the same paper still get
    consistent state.

    ``pdf_sha256`` (since v0.9.0) is the sha256 the *client* computed from
    the arxiv PDF it just fetched. The server cross-checks against its own
    RustFS-stored hash; mismatch → 400. Pass ``None`` when the claim
    response had no server-side hash to compare against (legacy objects).
    """
    params: dict[str, str] = {"source": "mineru"}
    if overwrite:
        params["overwrite"] = "true"
    if pdf_sha256:
        params["pdf_sha256"] = pdf_sha256
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
    server_pdf_sha256 = (claim.get("pdf_sha256") or "").lower() or None
    _print_err(
        f"Claim acquired for {arxiv_id} (id={claim_id}); submitting {pdf_url} to MinerU."
    )

    if not _is_acceptable_pdf_url(pdf_url, base_url):
        _print_err(
            f"[skip] {arxiv_id}: server returned untrusted pdf_url {pdf_url!r} — refusing to fetch"
        )
        _release_claim(
            base_url,
            arxiv_id,
            claim_id,
            request_timeout=args.request_timeout,
            verify=verify,
            headers=headers,
        )
        return 1

    client_pdf_sha256: Optional[str] = None
    if server_pdf_sha256:
        client_pdf_sha256 = _hash_arxiv_pdf(
            pdf_url,
            request_timeout=args.request_timeout,
            verify=verify,
        )
        if client_pdf_sha256 is None:
            _print_err(f"[error] {arxiv_id}: could not hash arxiv PDF for verification")
            _release_claim(
                base_url,
                arxiv_id,
                claim_id,
                request_timeout=args.request_timeout,
                verify=verify,
                headers=headers,
            )
            return 1
        if client_pdf_sha256 != server_pdf_sha256:
            _print_err(
                f"[error] {arxiv_id}: PDF sha256 mismatch — arxiv served "
                f"{client_pdf_sha256} but server holds {server_pdf_sha256}; aborting upload"
            )
            _release_claim(
                base_url,
                arxiv_id,
                claim_id,
                request_timeout=args.request_timeout,
                verify=verify,
                headers=headers,
            )
            return 1
    else:
        _print_err(
            f"note: {arxiv_id} has no server-stored sha256 (legacy object); "
            "skipping client-side PDF verification"
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
            pdf_sha256=client_pdf_sha256,
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
    except MinerUDailyLimitError as exc:
        _print_err(
            f"[daily-limit] {arxiv_id}: MinerU quota exhausted ({exc}). "
            f"Releasing claim; exiting {EXIT_DAILY_LIMIT} (EX_TEMPFAIL)."
        )
        _release_claim(
            base_url, arxiv_id, claim_id,
            request_timeout=args.request_timeout,
            verify=verify, headers=headers,
        )
        return EXIT_DAILY_LIMIT
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
) -> "_BatchOutcome":
    """Run one queue-drain pass via the MinerU batch API.

    Pulls up to ``--batch-size`` candidates, claims & verifies each,
    submits the survivors as a single MinerU batch (POST
    /api/v4/extract/task/batch), then polls the batch every
    ``MINERU_POLL_INTERVAL`` seconds. Done entries are downloaded,
    uploaded to the server, and their claims released; failed entries
    have their err_msg classified (so we can detect mid-batch daily-limit
    hits) and their claims released. In-flight entries (waiting / pending
    / running / converting) are simply polled again.

    Returns a :class:`_BatchOutcome` so the caller (watch loop) can react
    to ``daily_limit_hit`` by sleeping until next 00:01 local time.
    """
    batch_size = max(1, min(int(args.batch_size), MAX_BATCH_SIZE))

    list_resp = requests.get(
        f"{base_url}/api/papers/needs-mineru",
        params={"limit": batch_size},
        headers={**headers, **client_version_headers()},
        timeout=args.request_timeout,
        verify=verify,
    )
    check_response_version(list_resp, write=False)
    if not list_resp.ok:
        _print_err(_http_error(list_resp, "needs-mineru list"))
        return _BatchOutcome(processed=0, failures=1, daily_limit_hit=False)
    queue = list_resp.json()
    candidates = queue.get("papers") or []
    if not candidates:
        _print_err(
            "Nothing to do — no PDFs in RAW_DIR are waiting for MinerU. "
            f"(unclaimed={queue.get('total_unclaimed')}, claimed={queue.get('total_claimed')})"
        )
        return _BatchOutcome(processed=0, failures=0, daily_limit_hit=False)
    _print_err(
        f"Queue: {len(candidates)} candidate(s) "
        f"(unclaimed total={queue.get('total_unclaimed')}, batch_size={batch_size})"
    )

    # Phase 1: claim + verify each candidate. Failures here are released
    # immediately and counted, but do NOT abort batch submission for the
    # survivors — quota is too precious to waste on a stuck candidate.
    jobs, prep_failures = _prepare_batch_jobs(
        args, base_url, candidates, verify, headers
    )
    if not jobs:
        _print_err(f"No survivable jobs in this batch ({prep_failures} prep failures).")
        return _BatchOutcome(
            processed=prep_failures,
            failures=prep_failures,
            daily_limit_hit=False,
        )

    # Phase 2: submit the batch. Daily-limit at this point is the cleanest
    # signal we get — release every claim and tell the caller to back off.
    mineru = MinerUClient(
        config.mineru_api_token,
        base_url=config.mineru_api_base_url,
    )
    batch_id: str
    try:
        batch_id = mineru.submit_url_batch(
            [BatchFile(url=j.pdf_url, data_id=j.arxiv_id) for j in jobs],
            model_version=config.mineru_model_version,
            language=config.mineru_language,
            enable_formula=config.mineru_enable_formula,
            enable_table=config.mineru_enable_table,
            is_ocr=config.mineru_is_ocr,
            no_cache=args.no_cache,
        )
    except MinerUDailyLimitError as exc:
        _print_err(
            f"[daily-limit] MinerU rejected batch submission ({exc}). "
            "Releasing all claims; will back off until tomorrow."
        )
        _release_all(args, base_url, jobs, verify, headers)
        _cleanup_workdirs(jobs)
        return _BatchOutcome(
            processed=prep_failures,
            failures=prep_failures + len(jobs),
            daily_limit_hit=True,
        )
    except MinerUFatalError as exc:
        _print_err(f"[fatal] MinerU rejected batch submission: {exc}")
        _release_all(args, base_url, jobs, verify, headers)
        _cleanup_workdirs(jobs)
        return _BatchOutcome(
            processed=prep_failures,
            failures=prep_failures + len(jobs),
            daily_limit_hit=False,
        )
    except (MinerUError, requests.RequestException) as exc:
        _print_err(f"[error] MinerU batch submission failed: {exc}")
        _release_all(args, base_url, jobs, verify, headers)
        _cleanup_workdirs(jobs)
        return _BatchOutcome(
            processed=prep_failures,
            failures=prep_failures + len(jobs),
            daily_limit_hit=False,
        )
    _print_err(f"MinerU batch id: {batch_id} ({len(jobs)} files)")

    # Phase 3: poll until all entries terminal, downloading + uploading
    # done ones as we see them. Mid-batch daily-limit (rare but possible)
    # is detected via classification of failed entries' err_msg.
    poll_interval = max(float(config.mineru_poll_interval), 1.0)
    deadline = time.monotonic() + float(config.mineru_timeout)
    jobs_by_id = {j.arxiv_id: j for j in jobs}
    done_ids: set[str] = set()
    processed = prep_failures
    failures = prep_failures
    daily_limit_hit = False

    while jobs_by_id and time.monotonic() < deadline:
        if _SHUTDOWN_REQUESTED:
            _print_err("Shutdown requested; releasing remaining claims.")
            _release_all(args, base_url, list(jobs_by_id.values()), verify, headers)
            _cleanup_workdirs(list(jobs_by_id.values()))
            jobs_by_id.clear()
            break

        try:
            results = mineru.get_batch(batch_id)
        except MinerUDailyLimitError as exc:
            _print_err(
                f"[daily-limit] MinerU rejected batch poll ({exc}); "
                "in-flight tasks may still complete server-side but we "
                "give up our claims now and back off until tomorrow."
            )
            _release_all(args, base_url, list(jobs_by_id.values()), verify, headers)
            _cleanup_workdirs(list(jobs_by_id.values()))
            failures += len(jobs_by_id)
            jobs_by_id.clear()
            daily_limit_hit = True
            break
        except MinerURetryableError as exc:
            _print_err(f"[retryable] get_batch hiccup, will retry: {exc}")
            time.sleep(poll_interval)
            continue
        except (MinerUError, requests.RequestException) as exc:
            _print_err(f"[error] get_batch failed: {exc}; will retry")
            time.sleep(poll_interval)
            continue

        if not results:
            _print_err("MinerU: no results yet, polling again.")
            time.sleep(poll_interval)
            continue

        new_terminal = 0
        for entry in results:
            if entry.data_id in done_ids:
                continue
            if entry.state not in _TERMINAL_BATCH_STATES:
                continue
            job = jobs_by_id.get(entry.data_id)
            if job is None:
                # MinerU returned an entry for a paper we never submitted —
                # log and ignore. Possible if we cancelled mid-batch and a
                # retry got duplicated, but should never happen in practice.
                _print_err(
                    f"[warn] MinerU reported unknown data_id={entry.data_id!r} (state={entry.state}); ignoring"
                )
                continue
            done_ids.add(entry.data_id)
            new_terminal += 1
            del jobs_by_id[entry.data_id]

            if entry.state == "done":
                ok = _finalise_done_entry(
                    args, base_url, mineru, job, entry, verify, headers
                )
                processed += 1
                if not ok:
                    failures += 1
                _release_claim(
                    base_url,
                    job.arxiv_id,
                    job.claim_id,
                    request_timeout=args.request_timeout,
                    verify=verify,
                    headers=headers,
                )
                _cleanup_workdirs([job])
                continue

            # state == "failed": classify err_msg for daily-limit detection.
            from qatlas.parser.mineru_client import classify_mineru_error

            classified = classify_mineru_error(msg=entry.err_msg or "")
            processed += 1
            failures += 1
            if isinstance(classified, MinerUDailyLimitError):
                _print_err(
                    f"[daily-limit] {job.arxiv_id} failed with quota signal "
                    f"({entry.err_msg!r}); releasing remaining claims and "
                    "backing off until tomorrow."
                )
                daily_limit_hit = True
                _release_claim(
                    base_url,
                    job.arxiv_id,
                    job.claim_id,
                    request_timeout=args.request_timeout,
                    verify=verify,
                    headers=headers,
                )
                _cleanup_workdirs([job])
                # Release everything else still in flight.
                _release_all(args, base_url, list(jobs_by_id.values()), verify, headers)
                _cleanup_workdirs(list(jobs_by_id.values()))
                failures += len(jobs_by_id)
                jobs_by_id.clear()
                break
            _print_err(
                f"[failed] {job.arxiv_id}: {entry.err_msg or 'unknown error'}"
            )
            classified_fatal = isinstance(classified, MinerUFatalError)
            if classified_fatal:
                # Per-paper fatal (e.g. -60005 file too big, -60006 page count
                # exceeded, or any other non-retryable). DO NOT release the
                # claim — let the 30-min server-side lease expire naturally
                # so the same poison PDF doesn't immediately re-appear at
                # the top of needs-mineru and waste a batch slot on every
                # subsequent poll. Bounds the per-day waste to
                # (24 * 60 / 30) = 48 attempts per bad paper.
                _print_err(
                    f"[failed] {job.arxiv_id}: keeping claim until lease "
                    f"expires (~30 min) so the same bad PDF doesn't reappear "
                    f"next poll."
                )
            else:
                _release_claim(
                    base_url,
                    job.arxiv_id,
                    job.claim_id,
                    request_timeout=args.request_timeout,
                    verify=verify,
                    headers=headers,
                )
            _cleanup_workdirs([job])

        if jobs_by_id and not daily_limit_hit:
            still_running = ", ".join(sorted(jobs_by_id.keys())[:5])
            if len(jobs_by_id) > 5:
                still_running += f", ... (+{len(jobs_by_id) - 5} more)"
            _print_err(
                f"Batch progress: {len(done_ids)} done, "
                f"{len(jobs_by_id)} still in flight ({still_running}); "
                f"sleeping {poll_interval:.0f}s."
            )
            time.sleep(poll_interval)
    else:
        # Loop exited via timeout (not break) with work still pending.
        if jobs_by_id:
            _print_err(
                f"Batch timed out after MINERU_TIMEOUT={config.mineru_timeout}s "
                f"with {len(jobs_by_id)} task(s) still in flight; releasing claims."
            )
            _release_all(args, base_url, list(jobs_by_id.values()), verify, headers)
            _cleanup_workdirs(list(jobs_by_id.values()))
            failures += len(jobs_by_id)

    return _BatchOutcome(
        processed=processed, failures=failures, daily_limit_hit=daily_limit_hit
    )


@dataclass
class _BatchOutcome:
    """Summary of one _drain_queue_once pass.

    daily_limit_hit signals the watch loop to sleep until the next local
    00:01 (when MinerU's free quota resets) rather than just waiting
    --watch-interval seconds; one-shot runs surface it via exit code 75
    (EX_TEMPFAIL) so CI can treat it as transient.
    """

    processed: int
    failures: int
    daily_limit_hit: bool


@dataclass
class _BatchJob:
    """One paper claimed and verified, ready to feed into MinerU batch.

    workdir is a per-job tempdir (so concurrent downloads of full_zip
    don't collide on the same file name); it's cleaned up after upload
    via _cleanup_workdirs.
    """

    arxiv_id: str
    claim_id: str
    pdf_url: str
    server_pdf_sha256: Optional[str]
    client_pdf_sha256: Optional[str]
    workdir: Path


def _prepare_batch_jobs(
    args: argparse.Namespace,
    base_url: str,
    candidates: list[dict[str, Any]],
    verify: bool,
    headers: dict[str, str],
) -> tuple[List[_BatchJob], int]:
    """Claim + verify each candidate; return (valid_jobs, prep_failures).

    Any candidate that fails (claim error, non-arxiv URL, PDF hash
    mismatch, claim race) has its claim released and counts as one prep
    failure; survivors are returned in submission order.
    """
    jobs: List[_BatchJob] = []
    failures = 0
    for paper in candidates:
        if _SHUTDOWN_REQUESTED:
            break
        arxiv_id = paper["arxiv_id"]
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
            # 404/409 ("skip") aren't counted as failures; transport errors are.
            if skip and not skip.startswith("skip"):
                failures += 1
            continue
        claim_id = claim["claim_id"]
        pdf_url = claim["pdf_url"]
        server_pdf_sha256 = (claim.get("pdf_sha256") or "").lower() or None

        if not _is_acceptable_pdf_url(pdf_url, base_url):
            _print_err(
                f"[skip] {arxiv_id}: server returned untrusted pdf_url {pdf_url!r}"
            )
            _release_claim(
                base_url, arxiv_id, claim_id,
                request_timeout=args.request_timeout,
                verify=verify, headers=headers,
            )
            failures += 1
            continue

        client_pdf_sha256: Optional[str] = None
        if server_pdf_sha256:
            client_pdf_sha256 = _hash_arxiv_pdf(
                pdf_url,
                request_timeout=args.request_timeout,
                verify=verify,
            )
            if client_pdf_sha256 is None:
                _print_err(f"[error] {arxiv_id}: could not hash arxiv PDF")
                _release_claim(
                    base_url, arxiv_id, claim_id,
                    request_timeout=args.request_timeout,
                    verify=verify, headers=headers,
                )
                failures += 1
                continue
            if client_pdf_sha256 != server_pdf_sha256:
                _print_err(
                    f"[error] {arxiv_id}: PDF sha256 mismatch — arxiv served "
                    f"{client_pdf_sha256} but server holds {server_pdf_sha256}"
                )
                _release_claim(
                    base_url, arxiv_id, claim_id,
                    request_timeout=args.request_timeout,
                    verify=verify, headers=headers,
                )
                failures += 1
                continue
        else:
            _print_err(
                f"note: {arxiv_id} has no server-stored sha256 (legacy); skipping verify"
            )

        workdir = Path(tempfile.mkdtemp(prefix=f"qatlas-mineru-{arxiv_id.replace('/', '_')}-"))
        jobs.append(
            _BatchJob(
                arxiv_id=arxiv_id,
                claim_id=claim_id,
                pdf_url=pdf_url,
                server_pdf_sha256=server_pdf_sha256,
                client_pdf_sha256=client_pdf_sha256,
                workdir=workdir,
            )
        )
    return jobs, failures


def _finalise_done_entry(
    args: argparse.Namespace,
    base_url: str,
    mineru: MinerUClient,
    job: _BatchJob,
    entry: BatchTaskState,
    verify: bool,
    headers: dict[str, str],
) -> bool:
    """Download a done entry's zip and upload it to our server.

    Returns True on full success; False if the download or upload failed.
    Caller still releases the claim either way.
    """
    if not entry.full_zip_url:
        _print_err(f"[error] {job.arxiv_id}: state=done but full_zip_url is empty")
        return False
    if args.no_push:
        _print_err(
            f"[no-push] {job.arxiv_id}: would download {entry.full_zip_url} but --no-push is set"
        )
        return True

    zip_path = job.workdir / "mineru-result.zip"
    try:
        mineru.download_full_zip(entry.full_zip_url, zip_path)
    except (MinerUError, requests.RequestException) as exc:
        _print_err(f"[error] {job.arxiv_id}: download_full_zip failed: {exc}")
        return False
    _print_err(
        f"Downloaded MinerU zip for {job.arxiv_id} -> {zip_path} "
        f"({zip_path.stat().st_size} bytes)"
    )

    ok, payload = _upload_mineru_zip(
        base_url=base_url,
        arxiv_id=job.arxiv_id,
        zip_path=zip_path,
        overwrite=args.overwrite,
        request_timeout=args.request_timeout,
        verify=verify,
        headers=headers,
        pdf_sha256=job.client_pdf_sha256,
    )
    if not ok:
        _print_err(str(payload))
        return False
    print_json(payload)
    return True


def _release_all(
    args: argparse.Namespace,
    base_url: str,
    jobs: list[_BatchJob],
    verify: bool,
    headers: dict[str, str],
) -> None:
    """Best-effort release of every claim in jobs (errors logged only)."""
    for job in jobs:
        _release_claim(
            base_url,
            job.arxiv_id,
            job.claim_id,
            request_timeout=args.request_timeout,
            verify=verify,
            headers=headers,
        )


def _cleanup_workdirs(jobs: list[_BatchJob]) -> None:
    """Remove the per-job tempdir; tolerate already-removed."""
    for job in jobs:
        shutil.rmtree(job.workdir, ignore_errors=True)


def _seconds_until_next_daily_run() -> float:
    """Seconds until the next local 00:01 — when MinerU resets daily quota.

    01 (not 00) gives MinerU a minute of slack to actually reset.
    """
    now = datetime.now()
    tomorrow = (now + timedelta(days=1)).replace(
        hour=0, minute=1, second=0, microsecond=0
    )
    return max(60.0, (tomorrow - now).total_seconds())


def cmd_mineru(args: argparse.Namespace) -> int:
    config = ServerConfig.from_env()
    if not config.mineru_api_token:
        _print_err("MINERU_API_TOKEN must be set in your local .env to run MinerU client-side.")
        return 1

    base_url = base_url_from_args(args)
    verify = request_verify(args)
    headers = auth_headers(args)

    # Single-paper mode short-circuits the queue/batch path: just one
    # paper, one task — no batching, no quota bookkeeping beyond what the
    # single SubmitURLTask path naturally surfaces.
    if args.arxiv_id:
        return _process_one(args, base_url, config, args.arxiv_id, verify, headers)

    if args.watch:
        _install_signal_handlers()
        interval = max(int(args.watch_interval), 1)
        _print_err(
            f"--watch enabled; polling needs-mineru every {interval}s "
            f"(batch_size={args.batch_size}). Send SIGINT/SIGTERM (Ctrl-C) "
            "to stop after current batch."
        )
        consecutive_empty = 0
        while not _SHUTDOWN_REQUESTED:
            outcome = _drain_queue_once(args, base_url, config, verify, headers)
            if outcome.daily_limit_hit:
                # Quota burnt — no point hammering MinerU until reset.
                sleep_s = _seconds_until_next_daily_run()
                wake_at = datetime.now() + timedelta(seconds=sleep_s)
                _print_err(
                    f"[daily-limit] Sleeping {sleep_s / 3600:.1f}h until "
                    f"{wake_at.strftime('%Y-%m-%d %H:%M:%S')} "
                    "(local) for MinerU quota reset."
                )
                _sleep_interruptible(sleep_s)
                consecutive_empty = 0
                continue
            if outcome.processed == 0:
                consecutive_empty += 1
                if consecutive_empty == 1:
                    _print_err("Queue empty. Will keep polling.")
            else:
                consecutive_empty = 0
                _print_err(
                    f"Batch done: {outcome.processed} processed, "
                    f"{outcome.failures} failures. Sleeping {interval}s before next poll."
                )
            _sleep_interruptible(interval)
        _print_err("Watch loop exiting cleanly.")
        return 0

    # Default queue mode: one batch pass, then exit.
    outcome = _drain_queue_once(args, base_url, config, verify, headers)
    if outcome.daily_limit_hit:
        _print_err(
            f"[daily-limit] MinerU quota exhausted; exiting {EXIT_DAILY_LIMIT} "
            "(EX_TEMPFAIL). Re-run after local 00:01."
        )
        return EXIT_DAILY_LIMIT
    return 0 if outcome.failures == 0 else 1


def _sleep_interruptible(seconds: float) -> None:
    """Sleep in 1-second slices so SIGINT/SIGTERM wakes us promptly."""
    end = time.monotonic() + max(0.0, seconds)
    while time.monotonic() < end:
        if _SHUTDOWN_REQUESTED:
            return
        time.sleep(min(1.0, end - time.monotonic()))


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
        "--batch-size",
        type=int,
        default=MAX_BATCH_SIZE,
        help=(
            f"Queue mode only: max papers per MinerU batch (default {MAX_BATCH_SIZE}, "
            f"hard cap {MAX_BATCH_SIZE} = MinerU's per-batch limit). Smaller batches "
            "release per-paper failures sooner but waste round-trips."
        ),
    )
    parser.add_argument(
        "--max",
        type=int,
        default=None,
        dest="max_alias",
        help=(
            "Deprecated alias for --batch-size; kept for back-compat. "
            "If both given, --batch-size wins."
        ),
    )
    parser.add_argument(
        "--continue-on-error",
        action="store_true",
        help=(
            "Queue mode only: keep processing despite per-paper failures. "
            "In batch mode this is implicit (one paper's failure never aborts "
            "the rest of the batch); only daily-limit short-circuits."
        ),
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
    # --max is a deprecated alias for --batch-size. If only --max was
    # given, propagate. If both were given, --batch-size wins (the
    # default factory makes that unambiguous).
    if getattr(args, "max_alias", None) is not None and args.batch_size == MAX_BATCH_SIZE:
        args.batch_size = args.max_alias
    # --watch implies --continue-on-error; otherwise a single 5xx would
    # exit the daemon and defeat the whole point.
    if getattr(args, "watch", False):
        args.continue_on_error = True
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())

