"""Client-side QuantumAtlas HTTP commands."""

from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path
from typing import Any

import requests

from atlas.server.config import ServerConfig


TERMINAL_STATUSES = {"succeeded", "failed", "partial", "cancelled"}


def _default_base_url() -> str:
    config = ServerConfig.from_env()
    public_base_url = config.get_public_base_url()
    if public_base_url:
        return public_base_url
    host = "127.0.0.1" if config.host in {"0.0.0.0", "::"} else config.host
    return f"http://{host}:{config.port}"


def _print_json(payload: dict[str, Any]) -> None:
    print(json.dumps(payload, ensure_ascii=False, indent=2))


def _base_url_from_args(args: argparse.Namespace) -> str:
    return args.base_url.rstrip("/") if args.base_url else _default_base_url()


def _request_verify(args: argparse.Namespace) -> bool:
    if not getattr(args, "insecure", False):
        return True
    if not getattr(args, "_insecure_warning_shown", False):
        requests.packages.urllib3.disable_warnings(  # type: ignore[attr-defined]
            category=requests.packages.urllib3.exceptions.InsecureRequestWarning
        )
        print("Warning: TLS certificate verification is disabled.", file=sys.stderr)
        args._insecure_warning_shown = True
    return False


def _load_json_object(path: str) -> dict[str, Any]:
    payload = json.loads(Path(path).read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"expected JSON object in {path}")
    return payload


def _apply_reviewed_payload(body: dict[str, Any], args: argparse.Namespace) -> None:
    if args.reviewed_json:
        reviewed = _load_json_object(args.reviewed_json)
        if any(key in reviewed for key in ("algorithm", "algorithm_ir", "metadata")):
            body.update(reviewed)
        else:
            body["algorithm"] = reviewed
    if args.metadata_json:
        body["metadata"] = _load_json_object(args.metadata_json)
    if args.reviewed_by:
        body["reviewed_by"] = args.reviewed_by
    if args.source:
        body["source"] = args.source
    if args.notes:
        body["notes"] = args.notes


def _poll_task(base_url: str, task_id: str, args: argparse.Namespace) -> int:
    deadline = time.monotonic() + args.timeout
    task: dict[str, Any] = {"task_id": task_id, "status": "queued"}
    while time.monotonic() < deadline:
        task_response = requests.get(
            f"{base_url}/api/ingest/{task_id}",
            timeout=args.request_timeout,
            verify=_request_verify(args),
        )
        task_response.raise_for_status()
        task = task_response.json()
        if task.get("status") in TERMINAL_STATUSES:
            _print_json(task)
            return 0 if task.get("status") == "succeeded" else 1
        time.sleep(args.poll_interval)

    print(f"Timed out waiting for ingest task {task_id}", file=sys.stderr)
    _print_json(task)
    return 1


def cmd_ingest(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    body: dict[str, Any] = {
        "arxiv_id": args.arxiv_id,
        "extract": args.extract,
        "create_wiki": args.create_wiki,
        "sync_neo4j": args.sync_neo4j,
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
        verify=_request_verify(args),
    )
    response.raise_for_status()
    queued = response.json()
    task_id = queued["task_id"]

    if args.no_poll:
        _print_json(queued)
        return 0

    return _poll_task(base_url, task_id, args)


def cmd_continue(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    body: dict[str, Any] = {
        "create_wiki": args.create_wiki,
        "sync_neo4j": args.sync_neo4j,
    }
    if args.stop_after:
        body["stop_after"] = args.stop_after
    if args.stages:
        body["stages"] = args.stages.split(",")
    _apply_reviewed_payload(body, args)

    response = requests.post(
        f"{base_url}/api/ingest/{args.task_id}/continue",
        json=body,
        timeout=args.request_timeout,
        verify=_request_verify(args),
    )
    response.raise_for_status()
    queued = response.json()
    if args.no_poll:
        _print_json(queued)
        return 0
    return _poll_task(base_url, queued["task_id"], args)


def cmd_reviewed(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    body: dict[str, Any] = {
        "arxiv_id": args.arxiv_id,
        "create_wiki": args.create_wiki,
        "sync_neo4j": args.sync_neo4j,
    }
    _apply_reviewed_payload(body, args)

    response = requests.post(
        f"{base_url}/api/ingest/paper/reviewed-extraction",
        json=body,
        timeout=args.request_timeout,
        verify=_request_verify(args),
    )
    response.raise_for_status()
    queued = response.json()
    if args.no_poll:
        _print_json(queued)
        return 0
    return _poll_task(base_url, queued["task_id"], args)


def cmd_status(args: argparse.Namespace) -> int:
    base_url = _base_url_from_args(args)
    response = requests.get(
        f"{base_url}/api/ingest/{args.task_id}",
        timeout=args.request_timeout,
        verify=_request_verify(args),
    )
    response.raise_for_status()
    _print_json(response.json())
    return 0


def _add_common_http_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--base-url",
        help="Server base URL; defaults to PUBLIC_BASE_URL, then .env host/port",
    )
    parser.add_argument("--request-timeout", type=float, default=30.0)
    parser.add_argument(
        "--insecure",
        action="store_true",
        help="Skip TLS certificate verification for self-signed HTTPS endpoints",
    )


def _add_poll_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--no-poll", action="store_true", help="Only print queued task")
    parser.add_argument("--poll-interval", type=float, default=1.0)
    parser.add_argument("--timeout", type=float, default=600.0)


def _add_wiki_sync_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--wiki", dest="create_wiki", action="store_true", default=True)
    parser.add_argument("--no-wiki", dest="create_wiki", action="store_false")
    parser.add_argument("--sync-neo4j", dest="sync_neo4j", action="store_true", default=True)
    parser.add_argument("--no-sync-neo4j", dest="sync_neo4j", action="store_false")


def _add_reviewed_args(parser: argparse.ArgumentParser, *, required: bool = False) -> None:
    parser.add_argument(
        "--reviewed-json",
        required=required,
        help="JSON file containing an algorithm object or a reviewed extraction request body",
    )
    parser.add_argument("--metadata-json", help="Optional JSON metadata file")
    parser.add_argument("--reviewed-by")
    parser.add_argument("--source")
    parser.add_argument("--notes")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Submit a QuantumAtlas paper ingest task",
        epilog=(
            "Continuation commands: qatlas ingest continue TASK_ID --reviewed-json reviewed.json; "
            "qatlas ingest reviewed ARXIV_ID --reviewed-json reviewed.json"
        ),
    )
    parser.add_argument("arxiv_id", help="arXiv paper ID, e.g. quant-ph/9508027")
    _add_common_http_args(parser)
    parser.add_argument("--parser", choices=["pymupdf", "mineru"], default="pymupdf")
    parser.add_argument("--extract", dest="extract", action="store_true", default=True)
    parser.add_argument("--no-extract", dest="extract", action="store_false")
    _add_wiki_sync_args(parser)
    parser.add_argument("--stop-after", choices=["fetch", "parse", "extract", "wiki", "neo4j"])
    parser.add_argument("--stages", help="Comma-separated exact stages to run")
    parser.add_argument("--force-fetch", action="store_true")
    parser.add_argument("--force-parse", action="store_true")
    parser.add_argument("--mineru-no-cache", action="store_true")
    _add_poll_args(parser)
    parser.set_defaults(func=cmd_ingest)
    return parser


def build_continue_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Continue an ingest task")
    parser.add_argument("task_id", help="Existing ingest task ID")
    _add_common_http_args(parser)
    _add_wiki_sync_args(parser)
    parser.add_argument("--stop-after", choices=["fetch", "parse", "extract", "wiki", "neo4j"])
    parser.add_argument("--stages", help="Comma-separated exact stages to run")
    _add_reviewed_args(parser)
    _add_poll_args(parser)
    parser.set_defaults(func=cmd_continue)
    return parser


def build_reviewed_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Submit a reviewed extraction as a new ingest")
    parser.add_argument("arxiv_id", help="arXiv paper ID, e.g. quant-ph/9508027")
    _add_common_http_args(parser)
    _add_wiki_sync_args(parser)
    _add_reviewed_args(parser, required=True)
    _add_poll_args(parser)
    parser.set_defaults(func=cmd_reviewed)
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
        "reviewed": build_reviewed_parser,
        "status": build_status_parser,
    }
    if argv and argv[0] in command_parsers:
        parser = command_parsers[argv.pop(0)]()
    else:
        parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except ValueError as exc:
        print(f"Invalid input: {exc}", file=sys.stderr)
        return 2
    except requests.RequestException as exc:
        print(f"Request failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
