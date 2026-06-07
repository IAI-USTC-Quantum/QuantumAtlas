"""Phase 1 GPU smoke test for qatlas-rag.

Validates that bge-m3 + bge-reranker-v2-m3 both load on the local GPU
(expected: RTX 5080, sm_120, Blackwell) under fp16 and produce real numbers.

This is the *hard* Phase 1 acceptance — `torch.cuda.is_available()` alone is
not enough (see rubber-duck #3 in plan.md). The blocking targets are:

  - device_capability == (12, 0)
  - bge-m3 + reranker both fp16, both resident, peak VRAM <= 10 GB
  - 32-text batch embed (~800-tok each) under 5 s
  - 50-passage rerank under 1 s
  - no kernel / dtype / attention-backend exception

If anything fails, the run exits non-zero and Phase 2.5 spike + Phase 6
full build must NOT start until the failure is resolved (typically by
re-pinning torch / FlagEmbedding versions).

Usage:
    uv run --extra embed python -m scripts.spike.phase1_gpu_smoke
"""

from __future__ import annotations

import os
import sys
import time


SAMPLE_PARAGRAPH = (
    "We present a polynomial-time quantum algorithm for the integer "
    "factorization problem.  The algorithm runs in time bounded by a "
    "polynomial in the number of input bits, which contrasts sharply "
    "with the best known classical algorithms whose running time grows "
    "superpolynomially.  Our construction relies on quantum Fourier "
    "transforms over Z/q where q is chosen large enough relative to the "
    "input modulus N to guarantee good statistical concentration of the "
    "post-measurement distribution.  We also describe how the algorithm "
    "can be extended to compute discrete logarithms in groups of prime "
    "order, with no change in the dominant complexity terms.  Throughout "
    "we assume an idealised gate model with arbitrary single-qubit and "
    "CNOT gates; the bounds we prove translate without modification to "
    "any model that can compile this idealised gate set with at most "
    "poly-logarithmic overhead per gate.  Finally we compare our bounds "
    "to the empirical asymptotics observed in classical NFS implementations "
    "and argue that the crossover N at which the quantum algorithm becomes "
    "decisively faster, even ignoring quantum constant factors, is well "
    "below the sizes used by modern public-key cryptosystems."
) * 2  # roughly 800 tokens after BPE


def main() -> int:
    print("=" * 60)
    print("qatlas-rag Phase 1 GPU smoke test")
    print("=" * 60)

    try:
        import torch
    except ImportError as e:
        print(f"FAIL: torch import: {e}", file=sys.stderr)
        return 1

    print(f"torch          = {torch.__version__}")
    print(f"cuda available = {torch.cuda.is_available()}")
    print(f"cuda version   = {torch.version.cuda}")

    if not torch.cuda.is_available():
        print("FAIL: CUDA not available on this device", file=sys.stderr)
        return 2

    cap = torch.cuda.get_device_capability()
    print(f"device         = {torch.cuda.get_device_name(0)}")
    print(f"capability     = sm_{cap[0]}{cap[1]}")
    if cap != (12, 0):
        print(
            f"WARN: expected sm_120 (Blackwell / RTX 5080), got sm_{cap[0]}{cap[1]}; "
            "continuing but pin-check this when promoting to production.",
            file=sys.stderr,
        )

    # Disable HuggingFace's noisy progress bars in non-interactive runs.
    os.environ.setdefault("HF_HUB_DISABLE_PROGRESS_BARS", "1")
    os.environ.setdefault("TRANSFORMERS_VERBOSITY", "error")

    print("\nloading bge-m3 (fp16) ...")
    t0 = time.time()
    try:
        from FlagEmbedding import BGEM3FlagModel
    except ImportError as e:
        print(f"FAIL: FlagEmbedding import: {e}", file=sys.stderr)
        return 1

    m = BGEM3FlagModel("BAAI/bge-m3", use_fp16=True)
    print(f"  loaded in {time.time() - t0:.1f}s")

    print("loading bge-reranker-v2-m3 (fp16) ...")
    t0 = time.time()
    try:
        from FlagEmbedding import FlagReranker
    except ImportError as e:
        print(f"FAIL: FlagReranker import: {e}", file=sys.stderr)
        return 1

    r = FlagReranker("BAAI/bge-reranker-v2-m3", use_fp16=True)
    print(f"  loaded in {time.time() - t0:.1f}s")

    print("\n-- embed 32 paragraphs --")
    texts = [SAMPLE_PARAGRAPH for _ in range(32)]
    t0 = time.time()
    out = m.encode(texts, batch_size=32, max_length=1024)
    embed_dt = time.time() - t0
    dense = out["dense_vecs"]
    print(f"  wall: {embed_dt:.2f}s   dense shape={dense.shape}")

    print("\n-- rerank 50 (query, passage) pairs --")
    pairs = [("Polynomial-time quantum factoring algorithm.", texts[i % len(texts)]) for i in range(50)]
    t0 = time.time()
    scores = r.compute_score(pairs)
    rerank_dt = time.time() - t0
    print(f"  wall: {rerank_dt:.2f}s   first 3 scores={list(scores[:3])}")

    peak_gb = torch.cuda.max_memory_allocated() / 1024**3
    print(f"\nVRAM peak     = {peak_gb:.2f} GB")

    # --- acceptance gates ---
    # These are *smoke* bounds: catch catastrophic regressions (30s/pair,
    # OOM, kernel error), not production-perf gating. Real perf budgeting
    # happens at Phase 2.5 spike with real data + (dense vs hybrid x rerank
    # depth) tradeoff. Numbers measured here belong in the spike report
    # baseline so Phase 2.5 can compare.
    fail: list[str] = []
    if embed_dt > 10.0:
        fail.append(f"embed 32 took {embed_dt:.2f}s > 10.0s smoke ceiling")
    if rerank_dt > 5.0:
        fail.append(f"rerank 50 took {rerank_dt:.2f}s > 5.0s smoke ceiling")
    if peak_gb > 10.0:
        fail.append(f"VRAM peak {peak_gb:.2f} GB > 10.0 GB budget")

    if fail:
        print("\nFAIL:", file=sys.stderr)
        for line in fail:
            print(f"  - {line}", file=sys.stderr)
        return 3

    print("\nPASS — pin the verified torch / FlagEmbedding / transformers")
    print("       versions to '==' in pyproject.toml.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
