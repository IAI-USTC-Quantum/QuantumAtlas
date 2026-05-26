"""Top-level command-line entry point for QuantumAtlas.

This module backs the ``qatlas`` console script declared in
``pyproject.toml``.  It intentionally delegates to the existing module CLIs so
their behavior stays identical to ``python -m atlas.<module>``.
"""

from __future__ import annotations

import runpy
import sys
from dataclasses import dataclass
from typing import Mapping, Sequence

from atlas import __version__


@dataclass(frozen=True)
class Command:
    """A delegated QuantumAtlas CLI command."""

    module: str
    summary: str
    client_friendly: bool = True


COMMANDS: Mapping[str, Command] = {
    "ingest": Command("atlas.client.__main__", "Submit paper ingest tasks over HTTP"),
    "upload": Command(
        "atlas.client.upload",
        "Upload contributed PDF or parsed Markdown to the server",
    ),
    "mineru": Command(
        "atlas.client.mineru",
        "Run MinerU locally with your own token and push the result to the server",
    ),
    "parser": Command("atlas.parser.__main__", "Fetch and parse arXiv papers", False),
    "wiki": Command("atlas.wiki.__main__", "Browse, lint, and sync wiki pages", False),
    "designer": Command("atlas.designer.__main__", "Design circuits from algorithms"),
    "codegen": Command("atlas.codegen.__main__", "Generate backend code from Quantum IR"),
    "validator": Command("atlas.validator.__main__", "Validate quantum circuits"),
    "estimator": Command("atlas.estimator.__main__", "Estimate circuit resources"),
    "extractor": Command("atlas.extractor.__main__", "Extract algorithms with an LLM"),
    "server": Command("atlas.server.__main__", "Run the FastAPI web server", False),
    "service": Command("atlas.server.service", "Install or stage the systemd service", False),
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

    print("\n  Local workspace/server commands:")
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
  qatlas ingest quant-ph/9508027 --parser pymupdf --stop-after parse
  qatlas upload pdf quant-ph/9508027v1 --pdf paper.pdf --metadata meta.json
  qatlas upload markdown 2501.00010v1 --markdown paper.md --source mineru
  qatlas mineru quant-ph/9508027v1 --push-pdf
  qatlas designer <kg_algorithm_id> -o circuit_ir.json
  qatlas codegen circuit_ir.json --backend qiskit -o output.py
  qatlas validator circuit_ir.json --compare-with qft
  qatlas server

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

    return _run_module(
        command.module,
        argv0=f"qatlas {command_name}",
        args=args[1:],
    )


if __name__ == "__main__":
    raise SystemExit(main())
