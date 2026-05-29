"""``qatlas markdown`` — fetch a paper's Markdown from the server.

This is the client side of the server's *silent* MinerU conversion behind
``GET /api/papers/{arxiv_id}/markdown``:

  * If the server already has cached Markdown, it returns it immediately
    (HTTP 200, ``text/markdown``).
  * If not — and the server is configured with its own ``MINERU_API_TOKEN`` —
    the first request kicks off a background MinerU conversion and returns
    HTTP 202 with an ``Operation-Location`` header pointing at a
    side-effect-free *status resource* (``.../markdown/status``) plus a
    ``Retry-After`` hint. This command then polls that status resource —
    using capped exponential backoff with jitter, never faster than the
    server's ``Retry-After`` — until it reports ``done``, then fetches the
    Markdown content once and prints it / writes it to ``--output``.

Polling the status resource (rather than re-hitting the content endpoint)
keeps each poll cheap and side-effect-free: the content endpoint may *start*
a conversion, the status endpoint only *observes* one.

This is distinct from ``qatlas mineru``, which runs MinerU *locally* with the
contributor's own token and pushes the result up. ``qatlas markdown`` just
*consumes* Markdown, letting the server do (or have already done) the work.

Examples::

    qatlas markdown 2501.00010v1                 # print to stdout (wait if needed)
    qatlas markdown 2501.00010v1 -o paper.md      # write to a file
    qatlas markdown 2501.00010v1 --no-wait        # don't poll; exit 75 if pending
"""

from __future__ import annotations

import argparse
import random
import sys
import time
from urllib.parse import urljoin

import requests

from qatlas.client._common import (
    add_common_http_args,
    auth_headers,
    base_url_from_args,
    request_verify,
    run_with_request_errors,
)

# Exit code used when --no-wait is set / --timeout is hit and the server is
# still converting. 75 == EX_TEMPFAIL (sysexits.h): "temporary failure".
EXIT_PENDING = 75
# Exit code when MinerU conversion failed on the server, or markdown is
# otherwise unavailable (no PDF, conversion not configured, bad request).
EXIT_FAILED = 1

# Backoff growth factor and jitter fraction for status polling.
_BACKOFF_FACTOR = 1.5
_JITTER_FRACTION = 0.2
# Never sleep less than this between polls, even with negative jitter.
_MIN_SLEEP = 0.5


def _print_err(msg: str) -> None:
    print(msg, file=sys.stderr)


def _markdown_url(base_url: str, arxiv_id: str) -> str:
    return f"{base_url}/api/papers/{arxiv_id}/markdown"


def _default_status_url(base_url: str, arxiv_id: str) -> str:
    return f"{base_url}/api/papers/{arxiv_id}/markdown/status"


def _emit(markdown_text: str, output: str | None) -> None:
    """Write Markdown to --output or stdout."""
    if output:
        with open(output, "w", encoding="utf-8") as fh:
            fh.write(markdown_text)
        _print_err(f"Wrote {len(markdown_text)} chars to {output}")
    else:
        sys.stdout.write(markdown_text)
        if not markdown_text.endswith("\n"):
            sys.stdout.write("\n")


def _describe(payload: dict) -> str:
    state = payload.get("state") or payload.get("status") or "processing"
    started = payload.get("started_at")
    detail = payload.get("detail") or ""
    bits = [f"state={state}"]
    if started:
        bits.append(f"started_at={started}")
    suffix = f" ({detail})" if detail else ""
    return ", ".join(bits) + suffix


def _json_body(resp: requests.Response) -> dict:
    try:
        body = resp.json()
    except ValueError:
        return {}
    return body if isinstance(body, dict) else {}


def _parse_retry_after(resp: requests.Response) -> float | None:
    """Parse a numeric Retry-After header (seconds). HTTP-date form is
    ignored (the server only ever sends integer seconds)."""
    raw = resp.headers.get("Retry-After")
    if not raw:
        return None
    try:
        return max(0.0, float(raw))
    except ValueError:
        return None


def _resolve_status_url(base_url: str, arxiv_id: str, resp: requests.Response) -> str:
    """Prefer the server-supplied Operation-Location, resolved against the
    base URL; fall back to the conventional status path."""
    loc = resp.headers.get("Operation-Location")
    if loc:
        # urljoin handles both absolute URLs and root-relative paths.
        return urljoin(base_url + "/", loc)
    return _default_status_url(base_url, arxiv_id)


def _next_sleep(attempt: int, base: float, cap: float, retry_after: float | None) -> float:
    """Capped exponential backoff with jitter, floored by the server's
    Retry-After hint when present."""
    interval = min(base * (_BACKOFF_FACTOR**attempt), cap)
    if retry_after is not None:
        interval = max(interval, retry_after)
    # Symmetric jitter so concurrent clients don't poll in lockstep.
    interval *= 1.0 + _JITTER_FRACTION * (2.0 * random.random() - 1.0)
    return max(_MIN_SLEEP, interval)


def _fetch_content(base_url: str, arxiv_id: str, *, args: argparse.Namespace) -> requests.Response:
    return requests.get(
        _markdown_url(base_url, arxiv_id),
        headers={**auth_headers(args), "Accept": "text/markdown, application/json"},
        timeout=args.request_timeout,
        verify=request_verify(args),
    )


def _fetch_status(status_url: str, *, args: argparse.Namespace) -> requests.Response:
    return requests.get(
        status_url,
        headers={**auth_headers(args), "Accept": "application/json"},
        timeout=args.request_timeout,
        verify=request_verify(args),
    )


def _emit_content_response(resp: requests.Response, args: argparse.Namespace) -> int:
    """Handle a content-endpoint response that we expect to be 200."""
    if resp.status_code == 200:
        _emit(resp.text, args.output)
        return 0
    payload = _json_body(resp)
    detail = payload.get("error") or payload.get("detail") or resp.text or resp.reason
    _print_err(f"{args.arxiv_id}: HTTP {resp.status_code}: {detail}")
    return EXIT_FAILED


def _handle_content_terminal(resp: requests.Response, args: argparse.Namespace) -> int | None:
    """Map a non-202 content response to an exit code, or None if it's a
    202 (caller should start polling)."""
    if resp.status_code == 202:
        return None
    payload = _json_body(resp)
    if resp.status_code == 200:
        _emit(resp.text, args.output)
        return 0
    if resp.status_code == 502:
        _print_err(
            f"{args.arxiv_id}: server-side MinerU conversion failed: "
            f"{payload.get('error') or payload.get('detail') or resp.text}"
        )
        return EXIT_FAILED
    if resp.status_code == 503:
        _print_err(
            f"{args.arxiv_id}: {payload.get('detail') or 'server-side conversion unavailable'}"
        )
        return EXIT_FAILED
    if resp.status_code == 404:
        _print_err(
            f"{args.arxiv_id}: {payload.get('detail') or 'no PDF on the server; upload it first via `qatlas upload pdf`'}"
        )
        return EXIT_FAILED
    detail = payload.get("detail") or resp.text or resp.reason
    _print_err(f"{args.arxiv_id}: HTTP {resp.status_code}: {detail}")
    return EXIT_FAILED


def _poll_status(
    base_url: str,
    status_url: str,
    *,
    args: argparse.Namespace,
    deadline: float,
    initial_retry_after: float | None,
) -> int:
    """Poll the status resource until done/failed/timeout. Returns exit code."""
    attempt = 0
    retry_after = initial_retry_after
    while True:
        if time.monotonic() >= deadline:
            _print_err(
                f"{args.arxiv_id}: timed out after {args.timeout:g}s waiting for "
                "MinerU conversion; the job keeps running server-side — "
                "re-run this command later to pick up the cached result."
            )
            return EXIT_PENDING

        # Sleep first: we only enter this loop after a 202, so the server is
        # already working and an immediate status poll would race the job.
        time.sleep(_next_sleep(attempt, args.poll_interval, args.max_poll_interval, retry_after))
        attempt += 1

        resp = _fetch_status(status_url, args=args)
        retry_after = _parse_retry_after(resp)
        payload = _json_body(resp)
        status = payload.get("status")

        if status == "done":
            content = _fetch_content(base_url, args.arxiv_id, args=args)
            return _emit_content_response(content, args)
        if status == "failed":
            _print_err(
                f"{args.arxiv_id}: server-side MinerU conversion failed: "
                f"{payload.get('error') or payload.get('detail') or 'unknown error'}"
            )
            return EXIT_FAILED
        if status == "unavailable":
            _print_err(
                f"{args.arxiv_id}: {payload.get('detail') or 'server-side conversion unavailable'}"
            )
            return EXIT_FAILED
        if status == "not_started":
            # The job was evicted (e.g. server restart) before we observed
            # done. Re-trigger via the content endpoint and keep polling.
            trigger = _fetch_content(base_url, args.arxiv_id, args=args)
            terminal = _handle_content_terminal(trigger, args)
            if terminal is not None:
                return terminal
            retry_after = _parse_retry_after(trigger)
            attempt = 0
            continue
        if status == "processing" or resp.status_code == 200:
            # Still working (or an unexpected-but-OK body) — keep polling.
            continue

        detail = payload.get("detail") or resp.text or resp.reason
        _print_err(f"{args.arxiv_id}: status HTTP {resp.status_code}: {detail}")
        return EXIT_FAILED


def _run(args: argparse.Namespace) -> int:
    base_url = base_url_from_args(args)
    arxiv_id = args.arxiv_id

    # First hit the content endpoint: cache hit returns 200 immediately;
    # otherwise it starts the background conversion and returns 202.
    resp = _fetch_content(base_url, arxiv_id, args=args)
    terminal = _handle_content_terminal(resp, args)
    if terminal is not None:
        return terminal

    # 202: conversion in flight.
    payload = _json_body(resp)
    if args.no_wait:
        _print_err(
            f"{arxiv_id}: still converting ({_describe(payload)}); "
            "re-run without --no-wait to wait, or poll again later."
        )
        return EXIT_PENDING

    status_url = _resolve_status_url(base_url, arxiv_id, resp)
    retry_after = _parse_retry_after(resp)
    _print_err(
        f"{arxiv_id}: server is converting via MinerU ({_describe(payload)}); "
        f"polling {status_url} (backoff {args.poll_interval:g}-{args.max_poll_interval:g}s, "
        f"timeout {args.timeout:g}s)..."
    )

    deadline = time.monotonic() + max(0.0, args.timeout)
    return _poll_status(
        base_url, status_url, args=args, deadline=deadline, initial_retry_after=retry_after
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas markdown",
        description=(
            "Fetch a paper's Markdown from the server, triggering and waiting "
            "for server-side MinerU conversion when no cached Markdown exists."
        ),
    )
    parser.add_argument(
        "arxiv_id",
        help="arXiv id with version suffix, e.g. 2501.00010v1",
    )
    parser.add_argument(
        "-o",
        "--output",
        default=None,
        help="Write Markdown to this file instead of stdout.",
    )
    parser.add_argument(
        "--poll-interval",
        type=float,
        default=3.0,
        help=(
            "Initial / minimum seconds between status polls; grows with "
            "exponential backoff up to --max-poll-interval (default: 3)."
        ),
    )
    parser.add_argument(
        "--max-poll-interval",
        type=float,
        default=30.0,
        help="Upper bound for the backoff between status polls (default: 30).",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=1800.0,
        help=(
            "Max seconds to wait for a pending conversion before giving up "
            "(the server keeps working; re-run later). Default: 1800."
        ),
    )
    parser.add_argument(
        "--no-wait",
        action="store_true",
        help=(
            "Don't poll: if the server is still converting, exit 75 "
            "(EX_TEMPFAIL) immediately instead of waiting."
        ),
    )
    add_common_http_args(parser)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return run_with_request_errors(_run, args)


if __name__ == "__main__":
    raise SystemExit(main())
