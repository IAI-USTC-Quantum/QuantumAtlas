# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "beautifulsoup4>=4.12",
#   "requests>=2.31",
# ]
# ///
"""Fetch and parse quantumalgorithmzoo.org into structured JSON.

Outputs:
- tmp/zoo/zoo.html         (raw HTML, idempotent cache)
- tmp/zoo/zoo-algorithms.json
- tmp/zoo/zoo-references.json

Usage:
    uv run --script tmp/zoo/fetch_zoo.py [--no-fetch]
"""
from __future__ import annotations

import argparse
import json
import re
from pathlib import Path

import requests
from bs4 import BeautifulSoup, NavigableString, Tag

ZOO_URL = "https://quantumalgorithmzoo.org/"
HERE = Path(__file__).resolve().parent
HTML_PATH = HERE / "zoo.html"
ALG_JSON = HERE / "zoo-algorithms.json"
REF_JSON = HERE / "zoo-references.json"

ARXIV_RE = re.compile(r"arxiv\.org/(?:abs|pdf)/([a-z\-]+/\d{7}|\d{4}\.\d{4,5})", re.I)
DOI_RE = re.compile(r"doi\.org/([\w./\-]+)", re.I)


def fetch_html(force: bool = False) -> str:
    if HTML_PATH.exists() and not force:
        return HTML_PATH.read_text(encoding="utf-8")
    print(f"Fetching {ZOO_URL}")
    r = requests.get(ZOO_URL, timeout=60)
    r.raise_for_status()
    HTML_PATH.write_text(r.text, encoding="utf-8")
    return r.text


def parse_references(soup: BeautifulSoup) -> dict[str, dict]:
    """Parse <dl> reference list. Returns {ref_html_id: {num, citation, arxiv_id, doi, url}}."""
    refs: dict[str, dict] = {}
    refs_h2 = soup.find("h2", id="references")
    if not refs_h2:
        return refs
    dl = refs_h2.find_next("dl")
    if not dl:
        return refs
    for dt in dl.find_all("dt"):
        ref_id = dt.get("id")
        if not ref_id:
            continue
        try:
            number = int(dt.get_text(strip=True))
        except ValueError:
            number = None
        dd = dt.find_next_sibling("dd")
        if not isinstance(dd, Tag):
            continue
        text = dd.get_text(" ", strip=True)
        text = re.sub(r"\s+", " ", text)
        arxiv_ids: list[str] = []
        doi: str | None = None
        for a in dd.find_all("a"):
            href = a.get("href", "")
            m = ARXIV_RE.search(href)
            if m:
                arxiv_ids.append(m.group(1))
                continue
            md = DOI_RE.search(href)
            if md and not doi:
                doi = md.group(1)
        refs[ref_id] = {
            "number": number,
            "citation": text,
            "arxiv_ids": sorted(set(arxiv_ids)),
            "doi": doi,
        }
    return refs


def _walk_until_next_algorithm(start: Tag):
    """Yield siblings until the next <b>Algorithm:</b> or new <h2>."""
    node = start
    while node is not None:
        node = node.next_sibling
        if node is None:
            break
        if isinstance(node, Tag):
            if node.name == "h2":
                return
            if node.name == "b" and node.get_text(strip=True).rstrip(":").lower() == "algorithm":
                return
        yield node


def _section_text(label_tag: Tag) -> tuple[str, list[Tag]]:
    """Return text + collected tags between this <b>Label:</b> and the next <b>...</b> (or <br><br>)."""
    parts: list = []
    tag_nodes: list[Tag] = []
    node = label_tag
    while True:
        node = node.next_sibling
        if node is None:
            break
        if isinstance(node, Tag):
            if node.name == "b":
                break
            if node.name == "h2":
                break
            tag_nodes.append(node)
            parts.append(node)
        else:
            parts.append(node)
    soup = BeautifulSoup("", "html.parser")
    container = soup.new_tag("div")
    for p in parts:
        if isinstance(p, NavigableString):
            container.append(NavigableString(str(p)))
        else:
            container.append(p.__copy__())
    text = container.get_text(" ", strip=True)
    text = re.sub(r"\s+", " ", text)
    return text, tag_nodes


def parse_algorithms(soup: BeautifulSoup) -> list[dict]:
    """Each algorithm = a <b>Algorithm:</b> block until next <b>Algorithm:</b> or <h2>."""
    algorithms: list[dict] = []

    sections: list[tuple[str, str, Tag]] = []
    current_section_id: str | None = None
    current_section_title: str | None = None
    for tag in soup.find_all(["h2", "b"]):
        if tag.name == "h2":
            sid = tag.get("id", "")
            if sid in {"acknowledgments", "references"}:
                current_section_id = None
                current_section_title = None
                continue
            current_section_id = sid
            current_section_title = tag.get_text(strip=True)
            continue
        if not current_section_id:
            continue
        label = tag.get_text(strip=True).rstrip(":").lower()
        if label == "algorithm":
            sections.append((current_section_id, current_section_title or "", tag))

    for section_id, section_title, alg_tag in sections:
        name, _ = _section_text(alg_tag)
        speedup = ""
        implementations: list[dict] = []
        description_text = ""
        description_ref_ids: list[str] = []
        cur = alg_tag
        while True:
            nxt = cur.find_next_sibling("b")
            if nxt is None:
                break
            label = nxt.get_text(strip=True).rstrip(":").lower()
            if label == "algorithm":
                break
            if label == "speedup":
                speedup, _ = _section_text(nxt)
            elif label == "implementation":
                _txt, tag_nodes = _section_text(nxt)
                for t in tag_nodes:
                    if isinstance(t, Tag):
                        for a in t.find_all("a"):
                            implementations.append({
                                "label": a.get_text(strip=True),
                                "url": a.get("href", ""),
                            })
                        if t.name == "a":
                            implementations.append({
                                "label": t.get_text(strip=True),
                                "url": t.get("href", ""),
                            })
            elif label == "description":
                description_text, tag_nodes = _section_text(nxt)
                for t in tag_nodes:
                    if isinstance(t, Tag):
                        for a in t.find_all("a"):
                            href = a.get("href", "")
                            if href.startswith("#"):
                                description_ref_ids.append(href[1:])
                        if t.name == "a":
                            href = t.get("href", "")
                            if href.startswith("#"):
                                description_ref_ids.append(href[1:])
            cur = nxt

        algorithms.append({
            "section_id": section_id,
            "section_title": section_title,
            "name": name,
            "speedup": speedup,
            "implementations": implementations,
            "description": description_text,
            "ref_ids": sorted(set(description_ref_ids)),
        })

    return algorithms


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--no-fetch", action="store_true", help="Reuse cached zoo.html")
    args = p.parse_args()

    html = fetch_html(force=not args.no_fetch and not HTML_PATH.exists())
    if args.no_fetch and not HTML_PATH.exists():
        raise SystemExit("zoo.html missing; rerun without --no-fetch")
    if not args.no_fetch and HTML_PATH.exists():
        html = HTML_PATH.read_text(encoding="utf-8")

    soup = BeautifulSoup(html, "html.parser")

    refs = parse_references(soup)
    algos = parse_algorithms(soup)

    for algo in algos:
        arxiv_ids: list[str] = []
        for rid in algo["ref_ids"]:
            ref = refs.get(rid)
            if not ref:
                continue
            arxiv_ids.extend(ref["arxiv_ids"])
        algo["cited_arxiv_ids"] = sorted(set(arxiv_ids))

    REF_JSON.write_text(json.dumps(refs, indent=2, ensure_ascii=False), encoding="utf-8")
    ALG_JSON.write_text(json.dumps(algos, indent=2, ensure_ascii=False), encoding="utf-8")

    sections = sorted({(a["section_id"], a["section_title"]) for a in algos})
    print(f"Sections: {len(sections)}")
    for sid, title in sections:
        count = sum(1 for a in algos if a["section_id"] == sid)
        print(f"  - {sid:12s} {count:3d} {title}")
    print(f"Algorithms: {len(algos)}")
    print(f"References: {len(refs)}")
    total_arxiv = sum(len(a["cited_arxiv_ids"]) for a in algos)
    unique_arxiv = len({x for a in algos for x in a["cited_arxiv_ids"]})
    print(f"Cited arXiv IDs: {total_arxiv} (unique {unique_arxiv})")
    print(f"Wrote {ALG_JSON}")
    print(f"Wrote {REF_JSON}")


if __name__ == "__main__":
    main()
