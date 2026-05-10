# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Match zoo-cited arXiv IDs to local RAW assets.

Inputs:
- tmp/zoo/zoo-algorithms.json
- tmp/zoo/zoo-references.json
- $RAW_DIR/index.sqlite          (default /mnt/team/QuantumAtlas/raw/index.sqlite)
- $RAW_DIR/{markdown,pdf}/

Outputs:
- tmp/zoo/raw-matches.json       per-arxiv match record (md? pdf? best paper_key + version)
- tmp/zoo/missing-papers.tsv     papers cited by zoo but missing markdown
- tmp/zoo/no-arxiv-refs.tsv      zoo references with no arXiv link at all
"""
from __future__ import annotations

import json
import os
import re
import sqlite3
from collections import Counter
from pathlib import Path

HERE = Path(__file__).resolve().parent
ALG_JSON = HERE / "zoo-algorithms.json"
REF_JSON = HERE / "zoo-references.json"
RAW_DIR = Path(os.environ.get("RAW_DIR", "/mnt/team/QuantumAtlas/raw"))
DB_PATH = RAW_DIR / "index.sqlite"

MATCH_JSON = HERE / "raw-matches.json"
MISSING_TSV = HERE / "missing-papers.tsv"
NO_ARXIV_TSV = HERE / "no-arxiv-refs.tsv"

NUM_ID_RE = re.compile(r"^(?:[a-z\-]+/)?(\d{4})(\d{3,4})$", re.I)
NEW_ID_RE = re.compile(r"^(\d{4})\.(\d{4,5})$")


def normalize_arxiv(arxiv_id: str) -> tuple[str, str | None]:
    """Return (canonical_id, paper_key_stem). paper_key_stem matches sqlite paper_key sans version."""
    s = arxiv_id.strip()
    s_lower = s.lower()
    m = NEW_ID_RE.match(s)
    if m:
        return s, s
    m = NUM_ID_RE.match(s_lower)
    if m:
        ym, seq = m.group(1), m.group(2)
        return s, f"{ym}{seq}"
    return s, None


def best_record(conn: sqlite3.Connection, arxiv_id: str) -> dict | None:
    """Pick best paper_key for an arXiv id (prefer one with markdown, then highest version)."""
    canonical, stem = normalize_arxiv(arxiv_id)
    rows: list[sqlite3.Row] = []
    if "/" in arxiv_id or NEW_ID_RE.match(arxiv_id):
        rows = list(conn.execute(
            "SELECT paper_key, arxiv_id, version, title, authors, pdf_path, markdown_path, json_path "
            "FROM papers WHERE arxiv_id = ?",
            (arxiv_id,),
        ))
    if not rows and stem:
        rows = list(conn.execute(
            "SELECT paper_key, arxiv_id, version, title, authors, pdf_path, markdown_path, json_path "
            "FROM papers WHERE paper_key LIKE ?",
            (f"{stem}v%",),
        ))
    if not rows:
        return None

    def sort_key(r: sqlite3.Row) -> tuple:
        has_md = 1 if r["markdown_path"] else 0
        has_pdf = 1 if r["pdf_path"] else 0
        m = re.search(r"v(\d+)$", r["paper_key"])
        ver = int(m.group(1)) if m else 0
        return (has_md, has_pdf, ver)

    rows.sort(key=sort_key, reverse=True)
    r = rows[0]
    return {
        "paper_key": r["paper_key"],
        "arxiv_id_in_db": r["arxiv_id"],
        "version": r["version"],
        "title": r["title"],
        "authors": r["authors"],
        "pdf_path": r["pdf_path"],
        "markdown_path": r["markdown_path"],
        "json_path": r["json_path"],
    }


def main() -> None:
    if not DB_PATH.exists():
        raise SystemExit(f"index.sqlite not found at {DB_PATH}")
    if not ALG_JSON.exists() or not REF_JSON.exists():
        raise SystemExit("Run fetch_zoo.py first")

    algos = json.loads(ALG_JSON.read_text(encoding="utf-8"))
    refs = json.loads(REF_JSON.read_text(encoding="utf-8"))

    cited_ids: set[str] = set()
    cited_to_algo: dict[str, list[str]] = {}
    for algo in algos:
        for aid in algo["cited_arxiv_ids"]:
            cited_ids.add(aid)
            cited_to_algo.setdefault(aid, []).append(algo["name"])

    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row

    matches: dict[str, dict] = {}
    status_counts: Counter[str] = Counter()
    missing_rows: list[tuple[str, str, str, str]] = []

    for aid in sorted(cited_ids):
        rec = best_record(conn, aid)
        if rec is None:
            status = "not-in-raw"
            matches[aid] = {"status": status, "cited_by": cited_to_algo[aid]}
            missing_rows.append((aid, status, "", "; ".join(cited_to_algo[aid])))
        elif rec["markdown_path"]:
            status = "ok"
            matches[aid] = {
                "status": status,
                "cited_by": cited_to_algo[aid],
                **rec,
            }
        elif rec["pdf_path"]:
            status = "pdf-only"
            matches[aid] = {
                "status": status,
                "cited_by": cited_to_algo[aid],
                **rec,
            }
            missing_rows.append((aid, status, rec["pdf_path"], "; ".join(cited_to_algo[aid])))
        else:
            status = "metadata-only"
            matches[aid] = {
                "status": status,
                "cited_by": cited_to_algo[aid],
                **rec,
            }
            missing_rows.append((aid, status, "", "; ".join(cited_to_algo[aid])))
        status_counts[status] += 1

    no_arxiv_rows: list[tuple[str, int, str]] = []
    for ref_id, ref in refs.items():
        if ref["arxiv_ids"]:
            continue
        no_arxiv_rows.append((ref_id, ref.get("number") or 0, ref.get("citation", "")))

    MATCH_JSON.write_text(json.dumps(matches, indent=2, ensure_ascii=False), encoding="utf-8")

    with MISSING_TSV.open("w", encoding="utf-8") as f:
        f.write("arxiv_id\tstatus\tpdf_path\tcited_by\n")
        for row in missing_rows:
            f.write("\t".join(str(x) for x in row) + "\n")

    with NO_ARXIV_TSV.open("w", encoding="utf-8") as f:
        f.write("ref_id\tref_number\tcitation\n")
        for row in sorted(no_arxiv_rows, key=lambda r: r[1]):
            f.write("\t".join(str(x) for x in row) + "\n")

    print(f"Cited unique arXiv IDs: {len(cited_ids)}")
    for status, n in sorted(status_counts.items()):
        print(f"  {status:14s} {n}")
    print(f"References without any arXiv link: {len(no_arxiv_rows)}")
    print(f"Wrote {MATCH_JSON}")
    print(f"Wrote {MISSING_TSV}")
    print(f"Wrote {NO_ARXIV_TSV}")


if __name__ == "__main__":
    main()
