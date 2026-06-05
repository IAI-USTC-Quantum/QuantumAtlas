"""qatlas-rag CLI entry point.

Subcommands:
- spike    Run the Phase 1 GPU smoke test (loads bge-m3 + reranker, measures latency/VRAM)
- ingest   (Phase 3+) List RustFS, diff manifest, chunk + embed + upsert
- search   (Phase 5+) One-shot query against Qdrant via sidecar logic

The actual implementations are wired up phase by phase; v0.1.0 only ships `spike`.
"""

from __future__ import annotations

import argparse
import sys


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="qatlas-rag", description="qatlas-rag CLI")
    sub = p.add_subparsers(dest="cmd", required=True)

    sub.add_parser("spike", help="Run Phase 1 GPU smoke test (bge-m3 + reranker)")
    sub.add_parser("ingest", help="(Phase 3+) Run S3 → Qdrant ingest")
    sub.add_parser("search", help="(Phase 5+) One-shot search against Qdrant")

    args = p.parse_args(argv)

    if args.cmd == "spike":
        from scripts.spike.phase1_gpu_smoke import main as spike_main

        return spike_main()
    print(f"command {args.cmd!r} not implemented yet (planned for a later phase)", file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
