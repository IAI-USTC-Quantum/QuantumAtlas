"""Tests for ingest.parser — section walker and image-ref extraction."""

from __future__ import annotations

from qatlas_rag.ingest.parser import extract_image_refs, split_sections


def test_extract_image_refs_basic() -> None:
    text = "intro ![alt one](images/9508/9508027v1/1.png) middle ![](images/9508/9508027v1/2.png) end"
    new_text, refs = extract_image_refs(text)
    assert refs == [
        "images/9508/9508027v1/1.png",
        "images/9508/9508027v1/2.png",
    ]
    assert "[FIGURE:1]" in new_text and "[FIGURE:2]" in new_text
    assert "images/" not in new_text  # alt text dropped too


def test_extract_image_refs_no_images() -> None:
    new_text, refs = extract_image_refs("plain text without images")
    assert refs == []
    assert new_text == "plain text without images"


def test_split_sections_single_paper() -> None:
    md = """\
# Polynomial-Time Algorithm

By Peter Shor

## Abstract

We give a polynomial-time quantum algorithm for factoring.

## 1. Introduction

Quantum mechanics enables algorithms infeasible classically.

### 1.1 Background

Earlier work on quantum complexity ![figure](images/9508/9508027v1/fig1.png).

## 2. The Algorithm

The algorithm uses the QFT $$\\sum_{k=0}^{q-1} e^{2\\pi i j k / q}$$ to ...

## References

[1] ...
"""
    sections = split_sections(md)
    titles_emitted = [s.path[-1] if s.path else None for s in sections]
    # "1. Introduction" has body BEFORE "1.1 Background" so emits its own section.
    assert "Abstract" in titles_emitted
    assert "1. Introduction" in titles_emitted
    assert "1.1 Background" in titles_emitted
    assert "2. The Algorithm" in titles_emitted
    assert "References" in titles_emitted

    bg = next(s for s in sections if s.path[-1] == "1.1 Background")
    # Image ref attached to the section containing its placeholder.
    assert bg.image_refs == ["images/9508/9508027v1/fig1.png"]
    assert "[FIGURE:1]" in bg.body

    intro = next(s for s in sections if s.path[-1] == "1. Introduction")
    assert intro.path == ["Polynomial-Time Algorithm", "1. Introduction"]
    assert intro.level == 2


def test_split_sections_preamble_only() -> None:
    md = "no header here at all\njust prose."
    sections = split_sections(md)
    assert len(sections) == 1
    assert sections[0].path == ["__preamble__"]
    assert "no header" in sections[0].body


def test_split_sections_nested_path() -> None:
    md = """\
# A
intro to A

## A.1
body of A.1

### A.1.x
deep leaf
"""
    sections = split_sections(md)
    leaf = next(s for s in sections if s.path[-1] == "A.1.x")
    assert leaf.path == ["A", "A.1", "A.1.x"]
    assert leaf.level == 3
