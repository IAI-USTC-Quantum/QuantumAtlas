"""``qatlas paper`` — fetch paper assets from the server.

Subcommands::

    qatlas paper get markdown ID_OR_DOI [--output FILE | --to-stdout]
    qatlas paper get pdf      ID_OR_DOI [--output FILE | --to-stdout]
    qatlas paper status       ID_OR_DOI [--kind markdown|pdf]

These wrap the server's paper-access endpoints (only registered when
``QATLAS_PAPER_ACCESS_ENABLED=true``):

    GET /api/papers/{id_or_doi}/markdown[/status]
    GET /api/papers/{id_or_doi}/pdf[/status]

The endpoints follow a long-running-operation contract: cache miss
returns 202 + ``Operation-Location``; we transparently poll until
``state == cached`` (or a terminal failure) then stream the bytes.

ID forms accepted (server-side auto-resolution):

* Versioned arxiv id          ``0811.3171v3`` / ``quant-ph/9508027v2``
* Bare arxiv id (no version)  ``0811.3171`` / ``quant-ph/9508027``
                              → server resolves to latest published vN
* Bare old-style (no category) ``9508027``
                              → server applies ``category=quant-ph`` default
* DOI                          ``10.1103/PhysRevLett.103.150502``
                              → server resolves to canonical arxiv id

Whenever the server applies a default, this CLI prints a one-line
``Note:`` to stderr summarizing what it inferred (read from the
``X-QAtlas-Defaults-Applied`` response header). Use ``--quiet-notes``
to suppress.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from typing import Any, Iterable

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


# Maximum total wall-time we spend polling the LRO status endpoint
# before giving up. Generous because MinerU conversions of dense
# papers can run several minutes; agents that want a tighter bound
# should pass --max-wait.
_DEFAULT_MAX_WAIT_S = 1800.0  # 30 minutes


# How long to back off when the server doesn't give us a Retry-After
# header on a 202 (rare; the server always sends 5 by default).
_DEFAULT_POLL_INTERVAL_S = 5.0


def _print_notes(response: requests.Response, *, quiet: bool) -> None:
    """Emit a stderr note when the server applied any inference defaults.

    Both byte-streaming GETs and JSON responses carry the same info
    via ``X-QAtlas-Defaults-Applied`` header; we read the header
    rather than the body so the same code works for 200 text/markdown
    and 200 application/pdf.
    """
    if quiet:
        return
    defaults = response.headers.get("X-QAtlas-Defaults-Applied", "")
    requested = response.headers.get("X-QAtlas-Requested-Id", "")
    resolved = response.headers.get("X-QAtlas-Resolved-Id", "")
    if not defaults and not resolved:
        return
    bits = []
    if requested and resolved and requested != resolved:
        bits.append(f"{requested} → {resolved}")
    if defaults:
        bits.append(defaults)
    print(f"Note (server applied defaults): {'; '.join(bits)}", file=sys.stderr)


def _render_server_error(kind: str, resp: requests.Response) -> str:
    """Render the server's JSON error body verbatim for the operator.

    The paper-access endpoints return structured JSON on every 4xx/5xx
    (detail / kind / arxiv_id|doi|canonical / retry_after_iso /
    requested_id / resolved_id / defaults_applied). We dump the whole
    object — losing any of those fields swallows useful signal:

    * `kind` (fatal|retryable|daily_limit) tells the caller whether
      to retry at all.
    * `retry_after_iso` says when retry is sane (cooldown / quota
      reset).
    * `requested_id` / `resolved_id` / `defaults_applied` are echoed
      back even on failure so the caller can confirm "yes, you DID
      route my DOI to that arxiv id; the failure is downstream".

    Falls back to the raw body when JSON parsing fails so even an
    HTML error page from a misconfigured reverse proxy is visible.
    """
    parts: list[str] = []
    parts.append(f"{kind} fetch failed: HTTP {resp.status_code} {resp.reason}")
    try:
        body = resp.json()
    except json.JSONDecodeError:
        if resp.text:
            parts.append(resp.text.strip())
        return "\n".join(parts)
    if isinstance(body, dict):
        # Pretty-print so the operator's eye can find `detail` / `kind`
        # quickly; preserve every field for downstream tooling.
        parts.append(json.dumps(body, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        parts.append(json.dumps(body, ensure_ascii=False))
    return "\n".join(parts)


def _retry_after_seconds(response: requests.Response) -> float:
    """Parse the Retry-After header, falling back to default poll interval.

    RFC 7231 §7.1.3 lets it be either seconds or an HTTP-date; we
    only handle seconds because that's what qatlasd emits in
    practice. HTTP-date parsing is left for a follow-up if MinerU
    ever needs it.
    """
    raw = response.headers.get("Retry-After")
    if not raw:
        return _DEFAULT_POLL_INTERVAL_S
    try:
        return max(1.0, float(raw))
    except ValueError:
        return _DEFAULT_POLL_INTERVAL_S


def _format_eta(body: dict[str, Any]) -> str:
    """Render a compact progress line from a JSON status body."""
    state = body.get("state", "?")
    phase = body.get("phase", "")
    parts = [state]
    if phase:
        parts.append(phase)
    if (fetch := body.get("fetch")):
        if (total := fetch.get("bytes_total")) and (got := fetch.get("bytes_received")):
            parts.append(f"fetch={got}/{total}B")
        elif (got := fetch.get("bytes_received")):
            parts.append(f"fetch={got}B")
    if (conv := body.get("convert")):
        if (stage := conv.get("stage")):
            parts.append(f"convert.{stage}")
        if (polls := conv.get("polled_count")):
            parts.append(f"polls={polls}")
    if (queue := body.get("queue")):
        if (pos := queue.get("position")):
            ahead = queue.get("ahead_of_me", 0)
            running = queue.get("running_count", 0)
            mc = queue.get("max_concurrent", 0)
            parts.append(f"queue=#{pos}({ahead}ahead,{running}/{mc}slot)")
        if (eta := queue.get("eta_seconds")):
            parts.append(f"eta={int(eta)}s")
    return " ".join(parts)


def _poll_until_cached(
    args: argparse.Namespace,
    base_url: str,
    status_url: str,
    *,
    cached_predicate,
) -> tuple[int, dict[str, Any]]:
    """Poll the status endpoint until cache-hit or terminal failure.

    Returns (exit_code, last_body). exit_code == 0 means the asset is
    ready to GET; non-zero means we gave up (terminal failure, timeout).

    cached_predicate(body) returns True when the asset we want
    (markdown vs pdf) is ready. Lets us re-use this loop for both.
    """
    deadline = time.monotonic() + args.max_wait
    interval = _DEFAULT_POLL_INTERVAL_S
    last: dict[str, Any] = {}
    while True:
        try:
            resp = requests.get(
                status_url,
                headers={**auth_headers(args), **client_version_headers()},
                verify=request_verify(args),
                timeout=args.request_timeout,
            )
        except requests.RequestException as exc:
            print(f"poll request failed: {exc}", file=sys.stderr)
            return 1, last
        check_response_version(resp, write=False)
        if resp.status_code == 404:
            print(_render_server_error("status poll", resp), file=sys.stderr)
            return 1, last
        if not resp.ok:
            print(_render_server_error("status poll", resp), file=sys.stderr)
            return 1, last
        try:
            last = resp.json()
        except json.JSONDecodeError:
            print(f"status poll: non-JSON response\nHTTP {resp.status_code} {resp.reason}\n{resp.text}", file=sys.stderr)
            return 1, last
        # Honor server-side Retry-After even on 200 status responses.
        interval = _retry_after_seconds(resp)
        if cached_predicate(last):
            return 0, last
        state = last.get("state", "")
        if state in {"failed", "cooldown"}:
            # Body-level terminal state (HTTP itself was 200). Render
            # the JSON body verbatim so kind / detail / retry_after_iso
            # / phase / requested_id / resolved_id / defaults_applied
            # all surface together — picking out fields individually
            # would lose signal the server worked hard to expose.
            print(
                f"job ended in terminal state ({state}):\n"
                + json.dumps(last, ensure_ascii=False, indent=2, sort_keys=True),
                file=sys.stderr,
            )
            return 1, last
        if state == "unavailable":
            print(
                "server-side fetch/convert unavailable:\n"
                + json.dumps(last, ensure_ascii=False, indent=2, sort_keys=True),
                file=sys.stderr,
            )
            return 1, last
        # In-flight: print compact progress line then sleep.
        if not args.quiet_progress:
            print(f"... waiting: {_format_eta(last)}", file=sys.stderr)
        if time.monotonic() > deadline:
            print(
                f"timed out after {args.max_wait}s; last state={state}",
                file=sys.stderr,
            )
            return 1, last
        # Sleep, but never longer than the remaining deadline.
        sleep_for = max(0.1, min(interval, deadline - time.monotonic()))
        time.sleep(sleep_for)


def _stream_to_output(response: requests.Response, output: str | None) -> int:
    """Write a successful 200 body to --output FILE or stdout."""
    if output is None or output == "-":
        out = sys.stdout.buffer
    else:
        out = open(output, "wb")  # noqa: SIM115 — closed below in finally
    try:
        for chunk in response.iter_content(chunk_size=1 << 16):
            if chunk:
                out.write(chunk)
    finally:
        if out is not sys.stdout.buffer:
            out.close()
    return 0


def _do_get(args: argparse.Namespace, kind: str) -> int:
    """Implementation shared by `paper get markdown` and `paper get pdf`."""
    if kind not in {"markdown", "pdf"}:
        raise ValueError(f"unknown kind: {kind!r}")
    base_url = base_url_from_args(args)
    id_or_doi = args.id_or_doi.strip().lstrip("/")
    asset_url = f"{base_url}/api/papers/{id_or_doi}/{kind}"

    # First call: may return 200 (cache hit), 202 (LRO started), 4xx/5xx
    # (terminal failure). For 202 we then poll the status endpoint.
    resp = requests.get(
        asset_url,
        headers={**auth_headers(args), **client_version_headers()},
        verify=request_verify(args),
        timeout=args.request_timeout,
        stream=True,
        allow_redirects=True,
    )
    check_response_version(resp, write=False)

    # 200: we have bytes already.
    if resp.status_code == 200:
        _print_notes(resp, quiet=args.quiet_notes)
        return _stream_to_output(resp, args.output)

    # 202: long-running operation started; close this connection and
    # switch to polling the status endpoint.
    if resp.status_code == 202:
        _print_notes(resp, quiet=args.quiet_notes)
        try:
            initial_body = resp.json()
        except json.JSONDecodeError:
            initial_body = {}
        resp.close()
        if not args.quiet_progress:
            print(
                f"async operation started: {_format_eta(initial_body)}",
                file=sys.stderr,
            )
        status_url = (
            initial_body.get("operation", {}).get("status_url")
            or resp.headers.get("Operation-Location")
            or f"/api/papers/{id_or_doi}/{kind}/status"
        )
        # Operation-Location is path-only by convention; absolutize.
        if status_url.startswith("/"):
            status_url = base_url.rstrip("/") + status_url
        if args.no_wait:
            print_json(initial_body)
            return 0

        def is_ready(body: dict[str, Any]) -> bool:
            # For markdown we want both pdf_ready AND md_ready; for pdf
            # just pdf_ready. State "cached" implies both readiness
            # flags but check explicitly for clarity.
            if kind == "markdown":
                return body.get("state") == "cached" and body.get("md_ready", False)
            return body.get("state") == "cached" and body.get("pdf_ready", False)

        exit_code, last_body = _poll_until_cached(
            args, base_url, status_url, cached_predicate=is_ready
        )
        if exit_code != 0:
            return exit_code

        # Asset is now cached; re-issue the GET to stream bytes.
        final = requests.get(
            asset_url,
            headers={**auth_headers(args), **client_version_headers()},
            verify=request_verify(args),
            timeout=args.request_timeout,
            stream=True,
            allow_redirects=True,
        )
        check_response_version(final, write=False)
        if final.status_code != 200:
            # Surface defaults headers + full JSON body so the agent
            # sees server-side resolution even on the second-call
            # failure path.
            _print_notes(final, quiet=args.quiet_notes)
            print(_render_server_error(f"{kind} (post-cache GET)", final), file=sys.stderr)
            return 1
        return _stream_to_output(final, args.output)

    # Terminal failure on the initial call. Surface defaults headers
    # first (server might have applied DOI/version inference even
    # before failing), then dump body with detail / kind / arxiv_id /
    # retry_after_iso / phase plus echoed requested_id / resolved_id /
    # defaults_applied so the agent can decide retry vs give-up.
    _print_notes(resp, quiet=args.quiet_notes)
    print(_render_server_error(kind, resp), file=sys.stderr)
    return 1


def cmd_get_markdown(args: argparse.Namespace) -> int:
    return _do_get(args, "markdown")


def cmd_get_pdf(args: argparse.Namespace) -> int:
    return _do_get(args, "pdf")


def cmd_status(args: argparse.Namespace) -> int:
    base_url = base_url_from_args(args)
    id_or_doi = args.id_or_doi.strip().lstrip("/")
    kind = args.kind
    url = f"{base_url}/api/papers/{id_or_doi}/{kind}/status"
    resp = requests.get(
        url,
        headers={**auth_headers(args), **client_version_headers()},
        verify=request_verify(args),
        timeout=args.request_timeout,
    )
    check_response_version(resp, write=False)
    _print_notes(resp, quiet=args.quiet_notes)
    if resp.status_code == 404:
        print(_render_server_error("status", resp), file=sys.stderr)
        return 1
    if not resp.ok:
        print(_render_server_error("status", resp), file=sys.stderr)
        return 1
    try:
        body = resp.json()
    except json.JSONDecodeError:
        print(f"non-JSON status response:\nHTTP {resp.status_code} {resp.reason}\n{resp.text}", file=sys.stderr)
        return 1
    print_json(body)
    return 0


def _add_id_arg(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "id_or_doi",
        help=(
            "arxiv id (versioned or bare; new-style or old-style) OR a DOI. "
            "Server auto-fills missing version (latest) and missing category (quant-ph). "
            "Examples: 0811.3171v3, 0811.3171, quant-ph/9508027v2, 9508027, "
            "10.1103/PhysRevLett.103.150502"
        ),
    )


def _add_output_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--output",
        "-o",
        default=None,
        help='Write bytes to FILE. Use "-" or omit for stdout.',
    )
    parser.add_argument(
        "--no-wait",
        action="store_true",
        help="On cache miss return the initial 202 JSON without polling (for scripted async workflows).",
    )
    parser.add_argument(
        "--max-wait",
        type=float,
        default=_DEFAULT_MAX_WAIT_S,
        help=f"Maximum seconds to wait for an in-flight conversion. Default: {int(_DEFAULT_MAX_WAIT_S)}s.",
    )
    parser.add_argument(
        "--quiet-progress",
        action="store_true",
        help="Suppress the per-poll '...waiting: state ...' progress lines on stderr.",
    )
    parser.add_argument(
        "--quiet-notes",
        action="store_true",
        help="Suppress the 'Note (server applied defaults): ...' line on stderr.",
    )


def build_get_markdown_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas paper get markdown",
        description="Fetch a paper's MinerU markdown bytes from the server (triggers silent fetch+convert when missing).",
    )
    _add_id_arg(p)
    _add_output_args(p)
    add_common_http_args(p)
    p.set_defaults(func=cmd_get_markdown)
    return p


def build_get_pdf_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas paper get pdf",
        description="Fetch a paper's PDF bytes from the server (triggers silent fetch when missing).",
    )
    _add_id_arg(p)
    _add_output_args(p)
    add_common_http_args(p)
    p.set_defaults(func=cmd_get_pdf)
    return p


def build_status_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas paper status",
        description="Query the side-effect-free status endpoint for a paper (markdown or pdf).",
    )
    _add_id_arg(p)
    p.add_argument(
        "--kind",
        choices=["markdown", "pdf"],
        default="markdown",
        help='Which status endpoint to hit. Default: markdown.',
    )
    p.add_argument(
        "--quiet-notes",
        action="store_true",
        help="Suppress the 'Note (server applied defaults): ...' line on stderr.",
    )
    add_common_http_args(p)
    p.set_defaults(func=cmd_status)
    return p


def _print_top_help() -> None:
    print(
        """qatlas paper — fetch paper assets from the server

Usage:
  qatlas paper get markdown ID_OR_DOI [--output FILE] [--no-wait]
  qatlas paper get pdf      ID_OR_DOI [--output FILE] [--no-wait]
  qatlas paper status       ID_OR_DOI [--kind markdown|pdf]

ID forms accepted:
  - Versioned arxiv id          0811.3171v3 / quant-ph/9508027v2
  - Bare arxiv id (no version)  0811.3171  (server adds latest vN)
  - Bare old-style (no category) 9508027   (server adds quant-ph/)
  - DOI                          10.1103/PhysRevLett.103.150502

Server-side endpoints must be enabled via QATLAS_PAPER_ACCESS_ENABLED=true.
Defaults applied by the server are surfaced on stderr (use --quiet-notes
to suppress).

Use 'qatlas paper <subcommand> --help' for full options.
"""
    )


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if not argv or argv[0] in {"-h", "--help"}:
        _print_top_help()
        return 0
    subcommand = argv.pop(0)
    if subcommand == "get":
        if not argv or argv[0] in {"-h", "--help"}:
            _print_top_help()
            return 0
        kind = argv.pop(0)
        if kind == "markdown":
            parser = build_get_markdown_parser()
        elif kind == "pdf":
            parser = build_get_pdf_parser()
        else:
            print(f"unknown 'paper get' subcommand: {kind!r}", file=sys.stderr)
            _print_top_help()
            return 2
    elif subcommand == "status":
        parser = build_status_parser()
    else:
        print(f"unknown paper subcommand: {subcommand!r}", file=sys.stderr)
        _print_top_help()
        return 2
    args = parser.parse_args(argv)
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())
