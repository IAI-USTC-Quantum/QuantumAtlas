# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Build the backfill todo list: papers that the Wiki needs but RAW does not have.

Two kinds:
- not-in-raw: arXiv ID exists, but no PDF nor markdown in $RAW_DIR
- no-arxiv:   zoo reference has no arXiv link at all (likely pre-arXiv journal/conf)

Output: tmp/zoo/papers-to-backfill.tsv
"""
from __future__ import annotations

import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
ALG_JSON = HERE / "zoo-algorithms.json"
REF_JSON = HERE / "zoo-references.json"
MATCH_JSON = HERE / "raw-matches.json"
OUT_TSV = HERE / "papers-to-backfill.tsv"


def main() -> None:
    algos = json.loads(ALG_JSON.read_text(encoding="utf-8"))
    refs = json.loads(REF_JSON.read_text(encoding="utf-8"))
    matches = json.loads(MATCH_JSON.read_text(encoding="utf-8"))

    refid_to_algos: dict[str, list[str]] = {}
    arxiv_to_algos: dict[str, list[str]] = {}
    for a in algos:
        for rid in a["ref_ids"]:
            refid_to_algos.setdefault(rid, []).append(a["name"])
        for arxiv in a["cited_arxiv_ids"]:
            arxiv_to_algos.setdefault(arxiv, []).append(a["name"])

    rows: list[dict] = []

    for arxiv, m in matches.items():
        if m.get("status") != "not-in-raw":
            continue
        cit = ""
        for rid, r in refs.items():
            if arxiv in r["arxiv_ids"]:
                cit = r.get("citation", "")
                break
        rows.append({
            "kind": "not-in-raw",
            "arxiv_id": arxiv,
            "ref_id": "",
            "ref_number": "",
            "cited_by": "; ".join(sorted(set(arxiv_to_algos.get(arxiv, [])))),
            "citation": cit,
        })

    for rid, r in refs.items():
        if r["arxiv_ids"]:
            continue
        cited_by = sorted(set(refid_to_algos.get(rid, [])))
        if not cited_by:
            continue
        rows.append({
            "kind": "no-arxiv",
            "arxiv_id": "",
            "ref_id": rid,
            "ref_number": str(r.get("number") or ""),
            "cited_by": "; ".join(cited_by),
            "citation": r.get("citation", ""),
        })

    rows.sort(key=lambda r: (r["kind"], r["cited_by"], r["arxiv_id"] or r["ref_id"]))

    with OUT_TSV.open("w", encoding="utf-8") as f:
        f.write("kind\tarxiv_id\tref_id\tref_number\tcited_by\tcitation\n")
        for r in rows:
            f.write("\t".join(r[k].replace("\t", " ") for k in ("kind","arxiv_id","ref_id","ref_number","cited_by","citation")) + "\n")

    not_in = sum(1 for r in rows if r["kind"] == "not-in-raw")
    no_ax = sum(1 for r in rows if r["kind"] == "no-arxiv")
    print(f"not-in-raw: {not_in}")
    print(f"no-arxiv  : {no_ax}")
    print(f"wrote {OUT_TSV}")


if __name__ == "__main__":
    main()
