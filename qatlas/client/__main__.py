"""Client-side QuantumAtlas HTTP commands.

The server is bound to fetch + parse (ff-only wiki policy); reviewed-extraction
endpoints and the extract/wiki/neo4j stages have been removed.
"""

from __future__ import annotations

import argparse
import sys
import time
from typing import Any

import requests

from qatlas.client._common import (
    add_common_http_args,
    default_base_url,
    print_json,
    request_verify,
    run_with_request_errors,
)


TERMINAL_STATUSES = {"succeeded", "failed", "partial", "cancelled"}


def _default_base_url() -> str:
    """Resolve default server base URL; kept as a module-local wrapper so tests
    can monkey-patch ``qatlas.client.__main__._default_base_url``."""
    return default_base_url()


def _base_url_from_args(args: argparse.Namespace) -> str:
    """Honor an explicit --base-url, otherwise call the module-local default."""
    return args.base_url.rstrip("/") if args.base_url else _default_base_url()


# Legacy aliases kept for any callers that imported these private names.
_print_json = print_json
_request_verify = request_verify


def _poll_task(base_url: str, task_id: str, args: argparse.Namespace) -> int:
    deadline = time.monotonic() + args.timeout
    task: dict[str, Any] = {"task_id": task_id, "status": "queued"}
    while time.monotonic() < deadline:
        task_response = requests.get(
            f"{base_url}/api/ingest/{task_id}",
            timeout=args.request_timeout,
            verify=request_verify(args),
        )
        task_response.raise_for_status()
        task = task_response.json()
        if task.get("status") in TERMINAL_STATUSES:
            print_json(task)
            return 0 if task.get("status") == "succeeded" else 1
        time.sleep(args.poll_interval)

    print(f"Timed out waiting for ingest task {task_id}", file=sys.stderr)
    print_json(task)
    return 1


def cmd_ingest(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    body: dict[str, Any] = {
        "arxiv_id": args.arxiv_id,
        "parser": args.parser,
    }
    if args.stop_after:
        body["stop_after"] = args.stop_after
    if args.stages:
        body["stages"] = args.stages.split(",")
    if args.force_fetch:
        body["force_fetch"] = True
    if args.force_parse:
        body["force_parse"] = True
    if args.mineru_no_cache:
        body["mineru_no_cache"] = True

    response = requests.post(
        f"{base_url}/api/ingest/paper",
        json=body,
        timeout=args.request_timeout,
        verify=request_verify(args),
    )
    response.raise_for_status()
    queued = response.json()
    task_id = queued["task_id"]

    if args.no_poll:
        print_json(queued)
        return 0

    return _poll_task(base_url, task_id, args)


def cmd_continue(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    body: dict[str, Any] = {"parser": args.parser}
    if args.stop_after:
        body["stop_after"] = args.stop_after
    if args.stages:
        body["stages"] = args.stages.split(",")
    if args.force_fetch:
        body["force_fetch"] = True
    if args.force_parse:
        body["force_parse"] = True
    if args.mineru_no_cache:
        body["mineru_no_cache"] = True

    response = requests.post(
        f"{base_url}/api/ingest/{args.task_id}/continue",
        json=body,
        timeout=args.request_timeout,
        verify=request_verify(args),
    )
    response.raise_for_status()
    queued = response.json()
    if args.no_poll:
        print_json(queued)
        return 0
    return _poll_task(base_url, queued["task_id"], args)


def cmd_status(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    response = requests.get(
        f"{base_url}/api/ingest/{args.task_id}",
        timeout=args.request_timeout,
        verify=request_verify(args),
    )
    response.raise_for_status()
    print_json(response.json())
    return 0


def _add_common_http_args(parser: argparse.ArgumentParser) -> None:
    add_common_http_args(parser)


def _add_poll_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--no-poll", action="store_true", help="Only print queued task")
    parser.add_argument("--poll-interval", type=float, default=1.0)
    parser.add_argument("--timeout", type=float, default=600.0)


def _add_stage_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--parser",
        choices=["pymupdf", "mineru"],
        required=True,
        help="Explicitly choose the PDF parser. No silent default — you must opt in.",
    )
    parser.add_argument("--stop-after", choices=["fetch", "parse"])
    parser.add_argument("--stages", help="Comma-separated exact stages to run (fetch,parse)")
    parser.add_argument("--force-fetch", action="store_true")
    parser.add_argument("--force-parse", action="store_true")
    parser.add_argument("--mineru-no-cache", action="store_true")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Submit a QuantumAtlas paper ingest task (fetch + parse only)",
        epilog="Continuation: qatlas ingest continue TASK_ID [--stages parse]",
    )
    parser.add_argument("arxiv_id", help="arXiv paper ID, e.g. quant-ph/9508027")
    _add_common_http_args(parser)
    _add_stage_args(parser)
    _add_poll_args(parser)
    parser.set_defaults(func=cmd_ingest)
    return parser


def build_continue_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Continue an ingest task")
    parser.add_argument("task_id", help="Existing ingest task ID")
    _add_common_http_args(parser)
    _add_stage_args(parser)
    _add_poll_args(parser)
    parser.set_defaults(func=cmd_continue)
    return parser


def build_status_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Show ingest task status")
    parser.add_argument("task_id", help="Ingest task ID")
    _add_common_http_args(parser)
    parser.set_defaults(func=cmd_status)
    return parser


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    command_parsers = {
        "continue": build_continue_parser,
        "status": build_status_parser,
    }
    if argv and argv[0] in command_parsers:
        parser = command_parsers[argv.pop(0)]()
    else:
        parser = build_parser()
    args = parser.parse_args(argv)
    return run_with_request_errors(args.func, args)


if __name__ == "__main__":
    raise SystemExit(main())

