"""qatlas-rag CLI entry point.

Only one subcommand is shipped: `spike` runs the Phase 1 GPU smoke test
(loads bge-m3 + reranker, measures latency / VRAM). The query path
(search / ingest) moved into qatlasd (Go) in v0.20.0.
"""

from __future__ import annotations

import argparse


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="qatlas-rag", description="qatlas-rag CLI")
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("spike", help="Run Phase 1 GPU smoke test (bge-m3 + reranker)")

    args = p.parse_args(argv)

    if args.cmd == "spike":
        from scripts.spike.phase1_gpu_smoke import main as spike_main

        return spike_main()
    p.error(f"unknown subcommand: {args.cmd}")
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
