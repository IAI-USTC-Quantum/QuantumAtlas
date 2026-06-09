"""Top-level command-line entry point for QuantumAtlas.

This module backs the ``qatlas`` console script declared in
``pyproject.toml``.  It intentionally delegates to the existing module CLIs so
their behavior stays identical to ``python -m qatlas.<module>``.
"""

from __future__ import annotations

import runpy
import sys
from dataclasses import dataclass
from typing import Mapping, Sequence

from qatlas import __version__


@dataclass(frozen=True)
class Command:
    """A delegated QuantumAtlas CLI command."""

    module: str
    summary: str
    client_friendly: bool = True


COMMANDS: Mapping[str, Command] = {
    "config": Command(
        "qatlas.client.config",
        "Manage the user-level config file (~/.config/qatlas/config.yaml)",
    ),
    "auth": Command(
        "qatlas.client.auth",
        "Manage saved PATs / session tokens per host (login, status, token, logout)",
    ),
    "paper": Command(
        "qatlas.client.paper",
        "Fetch paper PDF / markdown from the server (silent fetch + LRO polling for cache misses)",
    ),
    "contrib": Command(
        "qatlas.client.contrib",
        "Contributor workflows: upload PDFs (contrib pdf) or run local MinerU and push (contrib mineru)",
    ),
    "ingest": Command("qatlas.client.__main__", "Submit paper ingest tasks over HTTP"),
    "parser": Command("qatlas.parser.__main__", "Fetch and parse arXiv papers", False),
    "wiki": Command("qatlas.wiki.__main__", "Browse, lint, and search wiki pages", False),
    "designer": Command("qatlas.designer.__main__", "Design circuits from algorithms"),
    "codegen": Command("qatlas.codegen.__main__", "Generate backend code from Quantum IR"),
    "validator": Command("qatlas.validator.__main__", "Validate quantum circuits"),
    "estimator": Command("qatlas.estimator.__main__", "Estimate circuit resources"),
    "extractor": Command("qatlas.extractor.__main__", "Extract algorithms with an LLM"),
}

ALIASES: Mapping[str, str] = {
    "parse": "parser",
    "design": "designer",
    "generate": "codegen",
    "validate": "validator",
    "estimate": "estimator",
    "extract": "extractor",
}


def _print_help() -> None:
    """Print top-level CLI help."""

    print(
        """QuantumAtlas command line

Usage:
  qatlas <command> [args...]
  qatlas --version
  qatlas --help

Commands:"""
    )

    print("  Client/operator commands:")
    for name, command in COMMANDS.items():
        if not command.client_friendly:
            continue
        print(f"    {name:<10} {command.summary}")

    print("\n  Local workspace commands:")
    for name, command in COMMANDS.items():
        if command.client_friendly:
            continue
        print(f"    {name:<10} {command.summary}")

    print(
        """
Aliases:
  parse -> parser, design -> designer, generate -> codegen
  validate -> validator, estimate -> estimator, extract -> extractor

Examples:
  qatlas ingest quant-ph/9508027 --stop-after parse
  qatlas contrib pdf quant-ph/9508027v1 --pdf paper.pdf
  qatlas contrib mineru 2501.00010v1
  qatlas contrib mineru --watch
  qatlas designer <kg_algorithm_id> -o circuit_ir.json
  qatlas codegen circuit_ir.json --backend qiskit -o output.py
  qatlas validator circuit_ir.json --compare-with qft

Use "qatlas <command> --help" for command-specific options."""
    )


def _print_usage_error(message: str) -> None:
    print(f"Error: {message}", file=sys.stderr)
    print("Run 'qatlas --help' to see available commands.", file=sys.stderr)


def _exit_code(code: object) -> int:
    """Normalize a child ``SystemExit.code`` value to an integer exit code."""

    if code is None:
        return 0
    if isinstance(code, int):
        return code
    print(code, file=sys.stderr)
    return 1


def _run_module(module: str, argv0: str, args: Sequence[str]) -> int:
    """Run a child module as if it had been invoked with ``python -m``."""

    original_argv = sys.argv[:]
    sys.argv = [argv0, *args]
    try:
        try:
            runpy.run_module(module, run_name="__main__")
        except SystemExit as exc:
            return _exit_code(exc.code)
        return 0
    finally:
        sys.argv = original_argv


def main(argv: Sequence[str] | None = None) -> int:
    """Run the QuantumAtlas CLI."""

    args = list(sys.argv[1:] if argv is None else argv)

    if not args or args[0] in {"-h", "--help"}:
        _print_help()
        return 0

    if args[0] in {"-V", "--version"}:
        print(f"qatlas {__version__}")
        return 0

    requested_command = args[0].replace("_", "-")
    command_name = ALIASES.get(requested_command, requested_command)
    command = COMMANDS.get(command_name)

    if command is None:
        _print_usage_error(f"unknown command '{args[0]}'")
        return 2

    # v0.17.0+: client config lives exclusively in
    # ~/.config/qatlas/config.yaml. Ensure it exists on first run so
    # the user can immediately edit it; idempotent on subsequent runs.
    #
    # Exception: skip for `qatlas config` itself — its `path` / `show`
    # subcommands intentionally tolerate a missing file and would
    # display misleading "auto-created on first read" behaviour
    # otherwise.
    if command_name != "config":
        try:
            from qatlas.config import ensure_default_config_exists
            ensure_default_config_exists()
        except Exception:
            # Defensive: never block a subcommand on config-file IO;
            # the embedded defaults work for any read-only command.
            pass

    return _run_module(
        command.module,
        argv0=f"qatlas {command_name}",
        args=args[1:],
    )


if __name__ == "__main__":
    raise SystemExit(main())
