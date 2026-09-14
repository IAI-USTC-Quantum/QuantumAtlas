#!/usr/bin/env python3
"""Check completed Sphinx sites: content, links, downloads, Chinese search, split.

This does not render content, make network requests, or participate in Sphinx.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
from urllib.parse import unquote, urlsplit

from bs4 import BeautifulSoup
from sphinx.search import js_index
from docs_sources import ROOT, IGNORE, component_root, load_lock, validate


def require(condition, message):
    if not condition:
        raise ValueError(message)


def source_names(source: Path):
    return {
        str(p.relative_to(source).with_suffix(""))
        for p in source.rglob("*")
        if p.suffix in (".rst", ".md")
        and not any(part in IGNORE for part in p.relative_to(source).parts)
    }


def check_site(site: Path, expected: set[str], terms: dict[str, str]):
    require(site.is_dir(), f"Missing site: {site}")
    index = js_index.loads((site / "searchindex.js").read_text(encoding="utf-8"))
    require(set(index["docnames"]) == expected,
            f"Wrong indexed page set in {site}: missing={sorted(expected - set(index['docnames']))}, unexpected={sorted(set(index['docnames']) - expected)}")
    for term, name in terms.items():
        hits = set()
        for field in ("terms", "titleterms"):
            value = index[field].get(term, [])
            hits.update([value] if isinstance(value, int) else value)
        require(index["docnames"].index(name) in hits, f"Chinese search misses {term!r} in {name}")
    for resource in ("_static/styles/furo.css", "_static/searchtools.js", "_static/language_data.js"):
        require((site / resource).is_file(), f"Missing Furo/search resource: {resource}")
    require("ChineseStemmer" not in (site / "_static/language_data.js").read_text(), "Sphinx ChineseStemmer regression")
    pages = {p.resolve(): BeautifulSoup(p.read_text(encoding="utf-8"), "html.parser") for p in site.rglob("*.html")}
    checked = 0
    unverified = set()
    for path, page in pages.items():
        require(page.title is not None and "QuantumAtlas" in page.title.get_text(), f"Wrong HTML identity: {path}")
        for node in page.find_all(["a", "link", "script", "img", "iframe"]):
            link = node.get("href") or node.get("src")
            if not link:
                continue
            url = urlsplit(link)
            if url.scheme or url.netloc or url.path.startswith("/"):
                unverified.add(link)
                continue  # Existing deployment routes / external URLs are not fetched here.
            target = (path.parent / unquote(url.path)).resolve() if url.path else path
            if target.is_dir():
                target /= "index.html"
            require(target.is_relative_to(site.resolve()), f"Escaping link: {path}: {link}")
            require(target.is_file(), f"Broken local link: {path}: {link}")
            if url.fragment and target.suffix == ".html":
                require(target in pages and pages[target].find(id=unquote(url.fragment)) is not None,
                        f"Broken anchor: {path}: {link}")
            checked += 1
    return {"indexed_pages": len(expected), "html_pages": len(pages), "local_links_checked": checked,
            "unverified_external_or_server_routes": sorted(unverified)}


def check(output: Path, release: bool = False):
    validate()
    all_names = source_names(ROOT / "docsite")
    all_names = {name for name in all_names if not name.startswith("_collections/")}
    dev_names = {name for name in all_names if name.startswith("dev/")}
    doc_names = all_names - dev_names
    for item in load_lock():
        names = source_names(component_root() / item["name"] / "docs")
        doc_names.update(f"_collections/{item['name']}/{name}" for name in names)
    result = {
        "doc": check_site(output / "doc", doc_names, {
            "配置": "_collections/qatlas-cli/overview",
            "评分": "_collections/qatlas-search/scorers",
            "检索": "_collections/qatlas-rag/overview",
        }),
        "devdoc": check_site(output / "devdoc", dev_names, {"开发": "dev/index"}),
    }
    require(not (output / "doc/dev").exists(), "Admin document subtree leaked into /doc")
    require(not (output / "devdoc/guide").exists() and not (output / "devdoc/manual").exists()
            and not (output / "devdoc/_collections").exists(), "User/component documents leaked into /devdoc")
    for filename in ("classic.json", "recent.json", "annual-rate.json"):
        source = component_root() / "qatlas-search/docs/examples/scorers" / filename
        matches = list((output / "doc/_downloads").glob(f"*/{filename}"))
        require(len(matches) == 1 and matches[0].read_bytes() == source.read_bytes(), f"Bad JSON example: {filename}")
    schema = list((output / "doc/_downloads").glob("*/swagger.json"))
    require(len(schema) == 1 and schema[0].read_bytes() == (ROOT / "internal/apidocs/swagger.json").read_bytes(), "OpenAPI download differs from source")
    manifests = [json.loads((output / site / "_static/docs-build.json").read_text()) for site in ("doc", "devdoc")]
    require(manifests[0] == manifests[1], "User/admin sites have different source manifests")
    require([{k: c[k] for k in ("name", "repository", "sha", "docs_dir")} for c in manifests[0]["components"]] == load_lock(), "Manifest component identities do not match lock")
    if release:
        require(manifests[0]["host_dirty"] is False, "Refusing to publish documents from a modified source tree")
    for item in load_lock():
        require(not (ROOT / "docsite/_collections" / item["name"]).exists(), "Collections did not clean its temporary targets")
    result["ok"] = True
    result["host_sha"] = manifests[0]["host_sha"]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path, help="Directory containing doc/ and devdoc/")
    parser.add_argument("--release", action="store_true", help="Require clean committed source provenance")
    args = parser.parse_args()
    print(json.dumps(check(args.output.resolve(), args.release), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
