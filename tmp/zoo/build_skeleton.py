# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml>=6"]
# ///
"""Phase 1: rebuild ~/QuantumAtlas-Wiki index.md and category landing pages.

This regenerates only the navigation skeleton; algorithm/paper/primitive/concept
pages are produced in later phases. Existing entity pages are left untouched.

Outputs (in $WIKI_DIR, default ~/QuantumAtlas-Wiki):
- index.md                            (zoo navigation + statistics)
- categories/zoo-algebraic.md
- categories/zoo-oracular.md
- categories/zoo-bqp.md
- categories/zoo-onml.md
- log.md                              (append one entry)
"""
from __future__ import annotations

import json
import os
import re
import sys
from datetime import date
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
ALG_JSON = HERE / "zoo-algorithms.json"
MATCH_JSON = HERE / "raw-matches.json"
WIKI_DIR = Path(os.environ.get("WIKI_DIR", str(Path.home() / "QuantumAtlas-Wiki"))).resolve()

CATEGORY_META = {
    "algebraic": {
        "title": "Algebraic and Number Theoretic Algorithms",
        "slug": "zoo-algebraic",
        "summary": "Quantum algorithms exploiting hidden subgroup structure, period finding, and number-theoretic primitives. Covers Shor-style factoring/discrete-log, Hallgren's algorithms over number fields, and related cryptanalytic speedups.",
    },
    "oracular": {
        "title": "Oracular Algorithms",
        "slug": "zoo-oracular",
        "summary": "Algorithms that interact with a black-box oracle and beat classical query complexity. Includes Grover search, hidden-shift problems, formula evaluation, quantum walks, and span-program based primitives.",
    },
    "BQP": {
        "title": "Approximation and Simulation Algorithms",
        "slug": "zoo-bqp",
        "summary": "Polynomial-time quantum algorithms for problems lying inside BQP, especially Hamiltonian simulation, eigenstate preparation, knot/Tutte invariants, and partition-function approximations.",
    },
    "ONML": {
        "title": "Optimization, Numerics, and Machine Learning",
        "slug": "zoo-onml",
        "summary": "Variational, adiabatic, and amplitude-amplified algorithms for optimization, linear algebra, differential equations, and quantum machine learning.",
    },
}

CATEGORY_ORDER = ["algebraic", "oracular", "BQP", "ONML"]


def slugify(name: str) -> str:
    s = name.lower()
    s = re.sub(r"['']", "", s)
    s = re.sub(r"[^a-z0-9]+", "-", s)
    s = s.strip("-")
    return s


def algo_id(name: str) -> str:
    return f"algo-{slugify(name)}"


def existing_ids(dir_path: Path, prefix: str) -> set[str]:
    if not dir_path.exists():
        return set()
    out: set[str] = set()
    for p in dir_path.glob(f"{prefix}*.md"):
        out.add(p.stem)
    return out


def list_titles(dir_path: Path, prefix: str) -> list[tuple[str, str]]:
    """Return [(id, title), ...] sorted by title for a given page directory."""
    out: list[tuple[str, str]] = []
    if not dir_path.exists():
        return out
    for p in sorted(dir_path.glob(f"{prefix}*.md")):
        title = p.stem
        try:
            text = p.read_text(encoding="utf-8")
            for ln in text.splitlines():
                if ln.startswith("title:"):
                    title = ln.split(":", 1)[1].strip().strip("'\"")
                    break
        except OSError:
            pass
        out.append((p.stem, title))
    return out


def write_with_frontmatter(path: Path, frontmatter: dict, body: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fm = yaml.safe_dump(frontmatter, sort_keys=False, allow_unicode=True).strip()
    path.write_text(f"---\n{fm}\n---\n\n{body.rstrip()}\n", encoding="utf-8")


def render_index(algos: list[dict], existing_algo_ids: set[str], existing_prim_ids: set[str], existing_paper_ids: set[str], existing_concept_ids: set[str], primitives: list[tuple[str, str]] | None = None, concepts: list[tuple[str, str]] | None = None) -> str:
    lines: list[str] = []
    lines.append("---")
    lines.append("id: index")
    lines.append("title: QuantumAtlas Wiki")
    lines.append("type: concept")
    lines.append("tags: [index, navigation]")
    lines.append(f"updated_at: '{date.today().isoformat()}'")
    lines.append("status: published")
    lines.append("---")
    lines.append("")
    lines.append("# QuantumAtlas Wiki")
    lines.append("")
    lines.append("Curated knowledge base of quantum algorithms, primitives, concepts, and source papers. The category structure follows [Quantum Algorithm Zoo](https://quantumalgorithmzoo.org/) and is filled in progressively from the QuantumAtlas RAW corpus.")
    lines.append("")
    lines.append("## Categories")
    lines.append("")
    for sid in CATEGORY_ORDER:
        meta = CATEGORY_META[sid]
        in_cat = [a for a in algos if a["section_id"] == sid]
        done = sum(1 for a in in_cat if algo_id(a["name"]) in existing_algo_ids)
        lines.append(f"- [[{meta['slug']}|{meta['title']}]] - {done}/{len(in_cat)} algorithms drafted")
    lines.append("")
    lines.append("## How to navigate")
    lines.append("")
    lines.append("- **Algorithms** live under `entities/algorithms/` and link out to primitives, concepts, and source papers.")
    lines.append("- **Primitives** (`entities/primitives/`) capture reusable building blocks such as QFT, QPE, amplitude estimation, and Hamiltonian simulation.")
    lines.append("- **Concepts** (`concepts/`) explain underlying ideas (hidden subgroup problem, span programs, quantum walks, ...).")
    lines.append("- **Sources** (`sources/papers/`) are wiki-ified summaries of the cited papers; full PDFs and parsed markdown live in `RAW_DIR`, not in this repo.")
    lines.append("- **Comparisons** (`comparisons/`) put algorithms side by side along complexity, qubit, or depth axes.")
    lines.append("")
    lines.append("Wiki links use the `[[page-id]]` form. Frontmatter and lint rules are documented in [docs/wiki-conventions.md](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/docs/wiki-conventions.md) of the application repository.")
    lines.append("")
    if primitives:
        lines.append("## Featured primitives")
        lines.append("")
        for pid, title in primitives:
            lines.append(f"- [[{pid}|{title}]]")
        lines.append("")
    if concepts:
        lines.append("## Featured concepts")
        lines.append("")
        for cid, title in concepts:
            lines.append(f"- [[{cid}|{title}]]")
        lines.append("")
    lines.append("## Statistics")
    lines.append("")
    total_zoo = len(algos)
    drafted = sum(1 for a in algos if algo_id(a["name"]) in existing_algo_ids)
    lines.append("| Type | Count |")
    lines.append("|------|-------|")
    lines.append(f"| Algorithms (zoo target) | {total_zoo} |")
    lines.append(f"| Algorithms drafted | {drafted} |")
    lines.append(f"| Primitives | {len(existing_prim_ids)} |")
    lines.append(f"| Concepts | {len(existing_concept_ids)} |")
    lines.append(f"| Source papers | {len(existing_paper_ids)} |")
    lines.append("")
    return "\n".join(lines).rstrip() + "\n"


CATEGORY_FEATURED = {
    "algebraic": {
        "primitives": ["prim-qft", "prim-qpe"],
        "concepts": ["concept-hidden-subgroup-problem"],
    },
    "oracular": {
        "primitives": ["prim-grover-oracle", "prim-amplitude-estimation", "prim-quantum-walk"],
        "concepts": ["concept-span-program"],
    },
    "BQP": {
        "primitives": ["prim-hamiltonian-simulation"],
        "concepts": [],
    },
    "ONML": {
        "primitives": [],
        "concepts": ["concept-adiabatic-theorem", "concept-variational-principle"],
    },
}


def render_category(sid: str, algos: list[dict], matches: dict, existing_algo_ids: set[str]) -> tuple[dict, str]:
    meta = CATEGORY_META[sid]
    in_cat = [a for a in algos if a["section_id"] == sid]
    in_cat.sort(key=lambda a: a["name"].lower())

    fm = {
        "id": meta["slug"],
        "title": meta["title"],
        "type": "concept",
        "category": "zoo-section",
        "tags": ["zoo", "category-index", sid.lower()],
        "created_at": date.today().isoformat(),
        "status": "published",
        "source": "quantumalgorithmzoo.org",
        "source_section_id": sid,
    }

    body_lines: list[str] = []
    body_lines.append(f"## {meta['title']}")
    body_lines.append("")
    body_lines.append(meta["summary"])
    body_lines.append("")
    body_lines.append(f"Source section: [Quantum Algorithm Zoo #{sid}](https://quantumalgorithmzoo.org/#{sid})")
    body_lines.append("")
    body_lines.append("## Algorithms")
    body_lines.append("")
    body_lines.append("| Algorithm | Speedup | Cited papers (in RAW) | Status |")
    body_lines.append("|-----------|---------|----------------------|--------|")
    for a in in_cat:
        aid = algo_id(a["name"])
        in_db_ok = sum(1 for x in a["cited_arxiv_ids"] if matches.get(x, {}).get("status") == "ok")
        in_db_pdf = sum(1 for x in a["cited_arxiv_ids"] if matches.get(x, {}).get("status") == "pdf-only")
        not_in = sum(1 for x in a["cited_arxiv_ids"] if matches.get(x, {}).get("status") == "not-in-raw")
        cited_total = len(a["cited_arxiv_ids"])
        link = f"[[{aid}|{a['name']}]]" if aid in existing_algo_ids else f"`{a['name']}` (planned)"
        coverage = f"{in_db_ok} md / {in_db_pdf} pdf-only / {not_in} missing of {cited_total}"
        status = "drafted" if aid in existing_algo_ids else "planned"
        body_lines.append(f"| {link} | {a['speedup']} | {coverage} | {status} |")
    body_lines.append("")
    body_lines.append("Coverage column counts the fate of each cited arXiv ID inside `$RAW_DIR/index.sqlite`: parsed markdown, PDF only, or absent. Papers without arXiv links and zoo references with no arXiv identifier are tracked in `tmp/zoo/no-arxiv-refs.tsv`.")
    body_lines.append("")
    feat = CATEGORY_FEATURED.get(sid, {})
    if feat.get("primitives"):
        body_lines.append("## Related primitives")
        body_lines.append("")
        for pid in feat["primitives"]:
            body_lines.append(f"- [[{pid}]]")
        body_lines.append("")
    if feat.get("concepts"):
        body_lines.append("## Related concepts")
        body_lines.append("")
        for cid in feat["concepts"]:
            body_lines.append(f"- [[{cid}]]")
        body_lines.append("")
    return fm, "\n".join(body_lines).rstrip() + "\n"


def append_log(log_path: Path, msg: str) -> None:
    log_path.parent.mkdir(parents=True, exist_ok=True)
    today = date.today().isoformat()
    line = f"\n{today} - [WIKI] {msg}\n"
    with log_path.open("a", encoding="utf-8") as f:
        f.write(line)


def main() -> None:
    if not ALG_JSON.exists() or not MATCH_JSON.exists():
        print("Run fetch_zoo.py and match_raw.py first", file=sys.stderr)
        sys.exit(1)

    algos = json.loads(ALG_JSON.read_text(encoding="utf-8"))
    matches = json.loads(MATCH_JSON.read_text(encoding="utf-8"))

    existing_algo = existing_ids(WIKI_DIR / "entities" / "algorithms", "algo-")
    existing_prim = existing_ids(WIKI_DIR / "entities" / "primitives", "prim-")
    existing_paper = existing_ids(WIKI_DIR / "sources" / "papers", "paper-")
    existing_concept = existing_ids(WIKI_DIR / "concepts", "")

    primitives_meta = list_titles(WIKI_DIR / "entities" / "primitives", "prim-")
    concepts_meta = list_titles(WIKI_DIR / "concepts", "concept-")

    index_path = WIKI_DIR / "index.md"
    index_path.write_text(render_index(algos, existing_algo, existing_prim, existing_paper, existing_concept, primitives_meta, concepts_meta), encoding="utf-8")
    print(f"wrote {index_path.relative_to(WIKI_DIR)}")

    cat_dir = WIKI_DIR / "categories"
    for sid in CATEGORY_ORDER:
        fm, body = render_category(sid, algos, matches, existing_algo)
        path = cat_dir / f"{CATEGORY_META[sid]['slug']}.md"
        write_with_frontmatter(path, fm, body)
        print(f"wrote {path.relative_to(WIKI_DIR)}")

    append_log(WIKI_DIR / "log.md", "Phase 1: regenerated index.md and 4 zoo category landing pages from quantumalgorithmzoo.org")
    print(f"appended {(WIKI_DIR / 'log.md').relative_to(WIKI_DIR)}")


if __name__ == "__main__":
    main()
