# QUANTUM_ATLAS.md - Wiki Conventions & Workflows

This document defines the conventions, workflows, and schemas for the QuantumAtlas layered knowledge base system.

## Four-Layer Architecture

| Layer | Directory | Purpose | Mutability |
|-------|-----------|---------|------------|
| **Application** | `atlas/`, `tests/`, `scripts/`, `examples/` | Code, templates, schemas, automation | **Tracked** - Reviewed in repo |
| **Wiki** | `wiki/` | Structured knowledge pages and source pages | **Tracked** - Human & LLM maintained |
| **Paper Assets** | `$RAW_DIR` | Canonical PDFs, parsed markdown, metadata JSON, images | **Canonical store** |
| **Derived Runtime** | Neo4j, `data/` | Graph queries, task/share/ingest records | **Derived / operational** |

### Layer Purposes

**Layer 1: Application** (`atlas/`, `tests/`, `scripts/`, `examples/`)
- Holds the long-term QuantumAtlas application code
- Defines parser, extractor, wiki, server, graph, and downstream pipeline behavior
- Remains the only long-term application repo

**Layer 2: Wiki** (`wiki/`)
- Human-readable, LLM-editable knowledge pages
- Markdown format with YAML frontmatter
- `wiki/sources/papers/*.md` is curated tracked knowledge, not parser cache
- This is the source of truth for authored knowledge pages

**Layer 3: Paper Assets** (`RAW_DIR`)
- Stores canonical paper-specific assets: PDFs, parsed markdown, metadata JSON, extracted images
- Keeps bulky or regenerated paper artifacts out of git
- May ingest from Papercrawl / KnowledgeBase during migration, but those are not long-term peer systems

**Layer 4: Graph Database / Runtime** (Neo4j + task/share/ingest state)
- Synced from Wiki and paper assets
- Enables relationship traversal, discovery, and collaboration workflows
- Not a primary source of truth for authored knowledge

---

## Core Insight: Classification vs. Relationship

> **Classification and Relationship are different things.**
> - **Wiki** → "What is it?" (taxonomy, summaries, definitions)
> - **Graph DB** → "What relates to it?" (citations, dependencies, implementations)

---

## Wiki Page Types

### Concepts (`wiki/concepts/`)
Define and explain quantum computing concepts.

**Template Structure:**
```markdown
---
id: concept-{name}
title: Page Title
type: concept
tags: [tag1, tag2]
created_at: YYYY-MM-DD
updated_at: YYYY-MM-DD
status: draft | review | published
related: [concept-other]
---

## Summary

Brief explanation of the concept.

## Definition

Formal or mathematical definition.

## Examples

- Example 1
- Example 2

## See Also

- [[concept-related-1]]
- [[concept-related-2]]
```

### Entities (`wiki/entities/`)
Document specific algorithms, primitives, people, and institutions.

**Subdirectories:**
- `entities/algorithms/` - Algorithm entity pages
- `entities/primitives/` - Quantum primitive pages
- `entities/people/` - Researcher/author pages

**Algorithm Entity Template:**
```markdown
---
id: algo-{name}
title: Algorithm Name
type: entity
category: algorithm
tags: [quantum-algorithm, category]
created_at: YYYY-MM-DD
status: published
related: [prim-qft, paper-arxiv-id]
neo4j_synced: true
---

## Overview

**Problem**: What problem does this algorithm solve?

**Complexity**:
- Time: O(?)
- Space: O(?)
- Gates: O(?)

## Primitives Used

- [[prim-qft]] - Used for phase estimation
- [[prim-qpe]] - Core component

## Algorithm Description

Step-by-step explanation...

## Source

- [[paper-arxiv-9508027]]

## Implementations

*Auto-generated from knowledge graph*
```

**Primitive Entity Template:**
```markdown
---
id: prim-{name}
title: Primitive Name
type: entity
category: primitive
tags: [primitive, category]
created_at: YYYY-MM-DD
status: published
related: [algo-shors]
neo4j_synced: true
---

## Summary

Brief description of the primitive.

## Definition

Mathematical definition...

## Complexity

- **Gate Count**: O(n²)
- **Depth**: O(n)
- **Qubits**: n

## References

- [[paper-arxiv-9508027]]
- [[person-author-name]]

## Prerequisites

- [[prim-qft]] - Required foundation
```

### Sources (`wiki/sources/`)
Wiki-fied representations of source documents.

**Paper Source Template:**
```markdown
---
id: paper-arxiv-{id}
title: Paper Title
type: source
category: paper
tags: [arxiv, quant-ph]
created_at: YYYY-MM-DD
status: published
related: [algo-introduced]
---

## Metadata

- **arXiv ID**: [{arxiv_id}](https://arxiv.org/abs/{arxiv_id})
- **Authors**: Author 1, Author 2
- **Published**: YYYY-MM-DD
- **DOI**: 10.xxxx/xxxxx (if available)

## Abstract

Paper abstract text...

## Key Contributions

1. Contribution 1
2. Contribution 2

## Algorithms Introduced

- [[algo-algorithm-name]]

## Key Insights

Important insights from the paper...

## See Also

- [[paper-cited-paper]]
```

### Comparisons (`wiki/comparisons/`)
Comparative analysis across entities.

**Comparison Template:**
```markdown
---
id: comp-{name}
title: Comparison Title
type: comparison
tags: [comparison, category]
created_at: YYYY-MM-DD
status: published
related: [algo-1, algo-2]
---

## Overview

Brief description of what's being compared.

## Comparison Criteria

| Criterion | [[algo-1]] | [[algo-2]] |
|-----------|------------|------------|
| Complexity | O(n²) | O(n log n) |
| Qubits | n | 2n |
| Depth | O(n) | O(log n) |

## Analysis

Detailed comparison analysis...

## Recommendations

When to use each algorithm...
```

---

## Core Workflows

### 1. Ingest Workflow

```
Paper (arXiv ID)
    │
    ├─► Fetch PDF → RAW_DIR/pdf/{paper_key}.pdf
    │
    ├─► Parse PDF → RAW_DIR/markdown/{paper_key}.md
    │
    ├─► Store Metadata → RAW_DIR/json/{paper_key}.json
    │
    ├─► LLM Extraction → AlgorithmIR
    │
    ├─► Create Wiki Pages:
    │     ├─ wiki/sources/papers/paper-arxiv-{id}.md
    │     ├─ wiki/entities/algorithms/algo-{name}.md
    │     └─ wiki/entities/primitives/prim-{name}.md (if new)
    │
    ├─► Update wiki/index.md
    │
    ├─► Append to wiki/log.md
    │
    └─► Sync to Neo4j (async)
```

The server API stores this workflow as an asynchronous ingest task. Each step has its own
status, message, timestamps, and progress payload, so clients can poll
`GET /api/ingest/{task_id}` without blocking on long downloads or parsing jobs.

The workflow is resumable by stage:

- Fetch only: set `stop_after=fetch`.
- Parse and stop for client-side review: set `stop_after=parse`.
- Resume from local assets: call `POST /api/ingest/{task_id}/continue`, or set `fetch=false`; the server reuses `RAW_DIR/pdf`, `RAW_DIR/json`, and `RAW_DIR/markdown` where available.
- Reparse from an existing PDF: set `stages=["parse"]`, `fetch=false`, and `force_parse=true`.
- Re-download or reparse deliberately: set `force_fetch=true` or `force_parse=true`.
- Use MinerU parsing: set `parser=mineru`; the server submits a public raw URL to MinerU with OCR disabled.
- Use client-side LLM review: call `POST /api/ingest/{task_id}/continue` or `POST /api/ingest/paper/reviewed-extraction` with a
  reviewed `algorithm` or `algorithm_ir`; the server skips its own LLM extractor and only
  writes Wiki pages plus optional Neo4j sync.

### 2. Query Workflow

```
User Query
    │
    ├─► Search wiki/index.md for relevant pages
    │
    ├─► Read matching wiki pages
    │
    ├─► Optional: Traverse Neo4j for relationships
    │
    ├─► Synthesize answer (LLM)
    │
    └─► Optional: Save Q&A as new wiki page
```

### 3. Lint Workflow

```
Wiki Pages
    │
    ├─► Check frontmatter validity
    │     └─ Missing required fields
    │
    ├─► Detect orphan pages
    │     └─ Pages with no incoming links
    │
    ├─► Detect broken links
    │     └─ [[links]] to non-existent pages
    │
    ├─► Check for contradictions
    │     └─ Same algorithm with different complexity
    │
    ├─► Detect missing concepts
    │     └─ Linked but not defined
    │
    └─► Report issues
```

---

## Wiki-Graph Sync Rules

| Wiki Page Type | Neo4j Node Type | Sync Direction | Relationships |
|----------------|-----------------|----------------|---------------|
| `entity/algorithm` | Algorithm | Wiki → Neo4j | `[[prim-*]]` → DEPENDS_ON |
| `entity/primitive` | Primitive | Wiki → Neo4j | prerequisites field |
| `entity/people` | Author | Wiki → Neo4j | `[[paper-*]]` → AUTHORED |
| `source/paper` | Paper | Wiki → Neo4j | `[[algo-*]]` → PUBLISHES |
| `comparison` | (No sync) | - | Query-only |

### Sync Direction

**Wiki is the source of truth for entity data.**
- Entity properties (name, description, complexity) come from Wiki
- Neo4j stores and queries relationships
- Sync is one-way: Wiki → Neo4j

---

## Page Naming Conventions

- Use **kebab-case**: `quantum-fourier-transform.md`
- Include **type prefix** for entities:
  - Algorithms: `algo-{name}.md`
  - Primitives: `prim-{name}.md`
  - People: `person-{name}.md`
- Use **paper source page ID** for papers: `paper-arxiv-{id}.md`
- Use **descriptive names** for comparisons: `comp-{topic}.md`

## Wiki Link Format

```
[[page-id]]                    # Basic link
[[page-id|display text]]       # Link with alias
[[#section]]                   # Section link (same page)
[[page-id#section]]            # Section link (other page)
```

---

## Directory Structure

```
QuantumAtlas/
├── $RAW_DIR/                         # Paper asset root from environment
│   ├── pdf/                          # Original PDFs
│   ├── markdown/                     # Parsed markdown
│   ├── json/                         # Metadata JSON
│   └── images/                       # Extracted images
│
├── wiki/                             # Layer 2: Wiki pages
│   ├── index.md                      # Main index
│   ├── log.md                        # Activity log
│   ├── concepts/                     # Concept definitions
│   ├── entities/
│   │   ├── algorithms/               # Algorithm entities
│   │   ├── primitives/               # Primitive entities
│   │   └── people/                   # People entities
│   ├── sources/
│   │   └── papers/                   # Paper summaries
│   └── comparisons/                  # Comparative analysis
│
├── QUANTUM_ATLAS.md                  # This file
│
└── atlas/wiki/                       # Wiki engine module
    ├── engine.py                     # Core WikiEngine
    ├── page.py                       # WikiPage model
    ├── templates.py                  # Page templates
    ├── ingester.py                   # Ingest workflow
    ├── querier.py                    # Query workflow
    ├── linter.py                     # Lint workflow
    └── sync/                         # Neo4j sync
        └── neo4j_sync.py
```

`RAW_DIR` is the canonical paper asset root. Local development may set
`RAW_DIR=raw`, while production should point it outside the application
checkout. The directory must contain
`pdf/`, `markdown/`, `json/`, and `images/` directly. Share API paths such as
`papers/pdf/{key}.pdf` are virtual share-relative paths; they resolve to
files under `RAW_DIR/pdf/`.

---

## Frontmatter Schema

All wiki pages must include YAML frontmatter:

```yaml
---
id: string                    # Required: Unique page identifier
title: string                 # Required: Page title
type: concept | entity | source | comparison  # Required
category: string              # Optional: Sub-type (algorithm, primitive, etc.)
tags: [string]                # Optional: Tags for classification
created_at: YYYY-MM-DD        # Required: Creation date
updated_at: YYYY-MM-DD        # Optional: Last update date
status: draft | review | published  # Required: Publication status
related: [string]             # Optional: Related page IDs
neo4j_synced: boolean         # Optional: Whether synced to Neo4j
neo4j_id: string              # Optional: Corresponding Neo4j node ID
---
```

---

## Lint Error Codes

| Code | Severity | Description |
|------|----------|-------------|
| W001 | ERROR | Missing required frontmatter field |
| W002 | ERROR | Invalid frontmatter field value |
| W003 | INFO | Orphan page (no incoming links) |
| W004 | WARNING | Broken link (target page does not exist) |
| W005 | WARNING | Missing concept definition |
| W006 | ERROR | Duplicate page ID |
| W007 | INFO | Outdated page (not updated in 30 days) |
| W008 | WARNING | Entity page has no tags |

---

## Migration Notes

### From YAML Primitives to Wiki

Existing YAML primitives in `atlas/knowledge_graph/primitives/` are migrated to:
- `wiki/entities/primitives/prim-{name}.md`
- YAML files remain as backup (read-only)
- Wiki pages become source of truth

### From papers/ to canonical paper assets + wiki

Existing `papers/` content is migrated to:
- `$RAW_DIR/` for canonical PDF, markdown, JSON, image assets
- `wiki/sources/papers/` for tracked wiki-ified paper summaries
