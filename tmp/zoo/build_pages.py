# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml>=6"]
# ///
"""Phase 2/3: generate algorithm pages and source paper pages.

For each zoo algorithm, write entities/algorithms/algo-<slug>.md.
For each cited arXiv ID, write sources/papers/paper-arxiv-<id>.md.

Existing files are NOT overwritten unless --force is set, so already-curated
pages (such as algo-hhl-linear-systems, paper-arxiv-0811.3171) stay intact.

Algorithm description text is taken verbatim from the zoo HTML (cleaned of \[N\]
ref markers but with arXiv reference numbers kept inline as bracketed footnotes
for traceability). Source page metadata is built from sqlite + zoo reference
citations; markdown bodies are stub or 'first paragraphs of RAW markdown' when
available.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
from datetime import date
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
ALG_JSON = HERE / "zoo-algorithms.json"
REF_JSON = HERE / "zoo-references.json"
MATCH_JSON = HERE / "raw-matches.json"
WIKI_DIR = Path(os.environ.get("WIKI_DIR", str(Path.home() / "QuantumAtlas-Wiki"))).resolve()
RAW_DIR = Path(os.environ.get("RAW_DIR", "/mnt/team/QuantumAtlas/raw"))

CATEGORY_TAG = {
    "algebraic": "algebraic",
    "oracular": "oracular",
    "BQP": "approximation-simulation",
    "ONML": "optimization-numerics-ml",
}

# Pages that have been hand-curated and must never be clobbered by --force.
PROTECTED_PAGES = {
    "entities/algorithms/algo-hhl-linear-systems.md",
    "entities/primitives/prim-qpe.md",
    "entities/primitives/prim-hamiltonian-simulation.md",
    "sources/papers/paper-arxiv-0811.3171.md",
}


def slugify(name: str) -> str:
    s = name.lower()
    s = re.sub(r"['']", "", s)
    s = re.sub(r"[^a-z0-9]+", "-", s)
    s = s.strip("-")
    return s


def algo_id(name: str) -> str:
    return f"algo-{slugify(name)}"


def paper_id(arxiv: str) -> str:
    safe = arxiv.replace("/", "-")
    return f"paper-arxiv-{safe}"


def write_with_frontmatter(path: Path, frontmatter: dict, body: str, force: bool) -> bool:
    rel = path.relative_to(WIKI_DIR).as_posix()
    if rel in PROTECTED_PAGES:
        return False
    if path.exists() and not force:
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    fm = yaml.safe_dump(frontmatter, sort_keys=False, allow_unicode=True).strip()
    path.write_text(f"---\n{fm}\n---\n\n{body.rstrip()}\n", encoding="utf-8")
    return True


def render_algorithm(algo: dict, refs: dict, matches: dict, comparisons: list[str] | None = None) -> tuple[dict, str]:
    aid = algo_id(algo["name"])
    section = algo["section_id"]
    cited = algo["cited_arxiv_ids"]

    related: list[str] = []
    for arxiv in cited:
        related.append(paper_id(arxiv))

    fm: dict = {
        "id": aid,
        "title": algo["name"],
        "type": "entity",
        "category": "algorithm",
        "tags": ["quantum-algorithm", CATEGORY_TAG[section]],
        "created_at": date.today().isoformat(),
        "status": "draft",
        "related": related,
        "external_links": [
            {
                "label": "Quantum Algorithm Zoo entry",
                "url": f"https://quantumalgorithmzoo.org/#{section}",
                "kind": "other",
            },
        ],
        "neo4j_synced": False,
        "neo4j_id": None,
        "source": "quantumalgorithmzoo.org",
        "source_section_id": section,
        "speedup": algo["speedup"],
    }
    if algo["implementations"]:
        for impl in algo["implementations"]:
            fm["external_links"].append({
                "label": impl["label"],
                "url": impl["url"],
                "kind": "code",
            })

    body_lines: list[str] = []
    body_lines.append("## Overview")
    body_lines.append("")
    body_lines.append(f"- **Speedup**: {algo['speedup']}")
    if algo["implementations"]:
        impls_md = ", ".join(f"[{i['label']}]({i['url']})" for i in algo["implementations"])
        body_lines.append(f"- **Listed implementations**: {impls_md}")
    body_lines.append(f"- **Source category**: [[{ {'algebraic':'zoo-algebraic','oracular':'zoo-oracular','BQP':'zoo-bqp','ONML':'zoo-onml'}[section] }|{algo['section_id']} section]]")
    body_lines.append("")
    body_lines.append("## Description")
    body_lines.append("")
    body_lines.append("Verbatim from [Quantum Algorithm Zoo](https://quantumalgorithmzoo.org/), with reference numbers kept as inline footnotes; see References below for the corresponding wiki source pages.")
    body_lines.append("")
    body_lines.append(_clean_description(algo["description"]))
    body_lines.append("")
    body_lines.append("## References")
    body_lines.append("")
    if cited:
        for arxiv in cited:
            m = matches.get(arxiv, {})
            status = m.get("status", "unknown")
            tag = {"ok": "md", "pdf-only": "pdf-only", "not-in-raw": "not-in-raw"}.get(status, status)
            body_lines.append(f"- [[{paper_id(arxiv)}]] (arXiv:{arxiv}) `[{tag}]`")
    else:
        body_lines.append("Zoo entry has no arXiv-linked references; underlying papers are tracked in `tmp/zoo/no-arxiv-refs.tsv` of the application repo.")
    body_lines.append("")
    if comparisons:
        body_lines.append("## Comparisons")
        body_lines.append("")
        for cid in comparisons:
            body_lines.append(f"- [[{cid}]]")
        body_lines.append("")
    body_lines.append("## Curation status")
    body_lines.append("")
    body_lines.append("This page was generated automatically from the Quantum Algorithm Zoo description and the QuantumAtlas RAW corpus. Manual curation should fill in: problem statement, complexity table (time / depth / qubits / oracle calls), algorithm sketch, primitives used (link with `prim-*` ids), and concept dependencies (link with `concept-*` ids).")
    return fm, "\n".join(body_lines).rstrip() + "\n"


def _clean_description(text: str) -> str:
    text = re.sub(r"\s+", " ", text).strip()
    return text


def _short_authors(authors: str | None) -> str | None:
    if not authors:
        return None
    parts = [p.strip() for p in re.split(r"\s+and\s+|,\s*", authors) if p.strip()]
    if len(parts) <= 5:
        return ", ".join(parts)
    return ", ".join(parts[:5]) + f", et al. ({len(parts)} authors)"


def _markdown_excerpt(md_path: Path, max_paragraphs: int = 3, max_chars: int = 2000) -> str | None:
    try:
        text = md_path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    text = re.sub(r"\r\n", "\n", text)
    parts = re.split(r"\n\s*\n", text.strip())
    chosen: list[str] = []
    total = 0
    for p in parts:
        p = p.strip()
        if not p:
            continue
        if p.startswith("#"):
            continue
        if len(p) < 60:
            continue
        chosen.append(p)
        total += len(p)
        if len(chosen) >= max_paragraphs or total >= max_chars:
            break
    if not chosen:
        return None
    excerpt = "\n\n".join(chosen)
    if len(excerpt) > max_chars:
        excerpt = excerpt[:max_chars] + " ..."
    return excerpt


def render_paper(arxiv: str, match: dict, refs: dict, cited_by_algos: list[dict]) -> tuple[dict, str]:
    pid = paper_id(arxiv)
    status = match.get("status", "unknown")

    paper_key = match.get("paper_key")
    title_db = match.get("title")
    authors_db = match.get("authors")

    citing_refs: list[dict] = []
    for rid, r in refs.items():
        if arxiv in r["arxiv_ids"]:
            citing_refs.append({"ref_id": rid, "number": r.get("number"), "citation": r.get("citation", "")})

    citation_full = _citation_summary(citing_refs)
    if title_db:
        title = title_db
    else:
        title = f"arXiv:{arxiv}"
    authors = _short_authors(authors_db)

    related = sorted({algo_id(a["name"]) for a in cited_by_algos})

    arxiv_url = f"https://arxiv.org/abs/{arxiv}"
    pdf_url = f"https://arxiv.org/pdf/{arxiv}.pdf"

    fm: dict = {
        "id": pid,
        "title": title,
        "type": "source",
        "category": "paper",
        "tags": ["paper", "arxiv"],
        "created_at": date.today().isoformat(),
        "status": "draft",
        "related": related,
        "external_links": [
            {"label": "arXiv abstract", "url": arxiv_url, "kind": "paper"},
            {"label": "arXiv PDF", "url": pdf_url, "kind": "pdf"},
        ],
        "source": "arxiv",
        "source_native_id": arxiv,
        "raw_status": status,
    }
    if citation_full:
        fm["zoo_citation"] = citation_full
    if paper_key:
        fm["raw_paper_key"] = paper_key
        if match.get("markdown_path"):
            fm["raw_markdown_path"] = match["markdown_path"]
        if match.get("pdf_path"):
            fm["raw_pdf_path"] = match["pdf_path"]

    body_lines: list[str] = []
    body_lines.append("## Metadata")
    body_lines.append("")
    body_lines.append(f"- **arXiv ID**: [{arxiv}]({arxiv_url})")
    body_lines.append(f"- **Authors**: {authors or 'unknown'}")
    if paper_key:
        body_lines.append(f"- **RAW paper key**: `{paper_key}`")
    body_lines.append(f"- **RAW status**: `{status}`")
    body_lines.append("")
    body_lines.append("## Citing algorithms")
    body_lines.append("")
    if related:
        for r in related:
            body_lines.append(f"- [[{r}]]")
    else:
        body_lines.append("No algorithm pages cite this paper yet.")
    body_lines.append("")

    if status == "ok" and match.get("markdown_path"):
        md_full = RAW_DIR / match["markdown_path"]
        excerpt = _markdown_excerpt(md_full)
        if excerpt:
            body_lines.append("## Excerpt")
            body_lines.append("")
            body_lines.append("First paragraphs from the parsed markdown in `RAW_DIR`. The full document lives outside this wiki.")
            body_lines.append("")
            body_lines.append(excerpt)
            body_lines.append("")

    if citing_refs:
        body_lines.append("## Zoo citation entries")
        body_lines.append("")
        for c in sorted(citing_refs, key=lambda r: r["number"] or 0):
            body_lines.append(f"- `[{c['ref_id']}]` (#{c['number']}) {c['citation']}")
        body_lines.append("")

    if status == "pdf-only":
        body_lines.append("## Curation note")
        body_lines.append("")
        body_lines.append(f"PDF is present in `RAW_DIR` (`{match.get('pdf_path')}`) but markdown has not been parsed. Run the QuantumAtlas parser to populate excerpt and key insights.")
    elif status == "not-in-raw":
        body_lines.append("## Curation note")
        body_lines.append("")
        body_lines.append("This paper is cited by the Quantum Algorithm Zoo but is not present in `RAW_DIR`. Recorded in `tmp/zoo/missing-papers.tsv` for backfill.")

    return fm, "\n".join(body_lines).rstrip() + "\n"


def _citation_summary(citing_refs: list[dict]) -> str | None:
    """Return the cleaned zoo citation string verbatim (arXiv suffix stripped)."""
    if not citing_refs:
        return None
    cit = citing_refs[0]["citation"]
    cit = re.sub(r"\s*\[\s*arXiv:[^\]]+\]\s*$", "", cit).strip()
    cit = re.sub(r"\s+", " ", cit)
    return cit or None


def _title_from_citation(citing_refs: list[dict]) -> str | None:
    """Use full zoo citation as a stand-in title; downstream curation can refine."""
    return _citation_summary(citing_refs)


def _authors_from_citation(citing_refs: list[dict]) -> str | None:
    return None


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--force", action="store_true", help="Overwrite existing pages")
    p.add_argument("--algorithms", action="store_true", help="Generate algorithm pages")
    p.add_argument("--papers", action="store_true", help="Generate source paper pages")
    p.add_argument("--all", action="store_true", help="Generate everything")
    args = p.parse_args()

    if not (args.algorithms or args.papers or args.all):
        args.all = True

    algos = json.loads(ALG_JSON.read_text(encoding="utf-8"))
    refs = json.loads(REF_JSON.read_text(encoding="utf-8"))
    matches = json.loads(MATCH_JSON.read_text(encoding="utf-8"))

    arxiv_to_algos: dict[str, list[dict]] = {}
    for algo in algos:
        for arxiv in algo["cited_arxiv_ids"]:
            arxiv_to_algos.setdefault(arxiv, []).append(algo)

    algo_to_comparisons: dict[str, list[str]] = {}
    comp_dir = WIKI_DIR / "comparisons"
    if comp_dir.exists():
        for cp in sorted(comp_dir.glob("comp-*.md")):
            try:
                text = cp.read_text(encoding="utf-8")
            except OSError:
                continue
            fm_block = ""
            if text.startswith("---"):
                end = text.find("\n---", 3)
                if end > 0:
                    fm_block = text[4:end]
            try:
                meta = yaml.safe_load(fm_block) or {}
            except yaml.YAMLError:
                continue
            comp_id = meta.get("id") or cp.stem
            for tgt in meta.get("related", []) or []:
                if isinstance(tgt, str) and tgt.startswith("algo-"):
                    algo_to_comparisons.setdefault(tgt, []).append(comp_id)

    written = {"algo_new": 0, "algo_skipped": 0, "paper_new": 0, "paper_skipped": 0}

    if args.algorithms or args.all:
        for algo in algos:
            algo_aid = algo_id(algo["name"])
            comp_links = algo_to_comparisons.get(algo_aid, [])
            fm, body = render_algorithm(algo, refs, matches, comp_links)
            if comp_links:
                fm["related"] = list(fm.get("related", [])) + comp_links
            path = WIKI_DIR / "entities" / "algorithms" / f"{fm['id']}.md"
            if write_with_frontmatter(path, fm, body, args.force):
                written["algo_new"] += 1
            else:
                written["algo_skipped"] += 1

    if args.papers or args.all:
        for arxiv, m in matches.items():
            cited_by = arxiv_to_algos.get(arxiv, [])
            fm, body = render_paper(arxiv, m, refs, cited_by)
            path = WIKI_DIR / "sources" / "papers" / f"{fm['id']}.md"
            if write_with_frontmatter(path, fm, body, args.force):
                written["paper_new"] += 1
            else:
                written["paper_skipped"] += 1

    print(f"Algorithm pages: new={written['algo_new']}, skipped (existed)={written['algo_skipped']}")
    print(f"Source paper pages: new={written['paper_new']}, skipped (existed)={written['paper_skipped']}")


if __name__ == "__main__":
    main()
