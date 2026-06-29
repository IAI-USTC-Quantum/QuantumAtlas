# A Claim's `references` are namespaced `kind:id` pointers, resolved server-side via `/api/papers/lookup`

A Claim gains a **`references`** field: the bibliographic works it cites, stored as **namespaced,
unversioned `kind:id` strings** — `arxiv:2208.06941`, `openalex:W4406693713`,
`doi:10.22331/q-2023-03-20-955`. These IDs appear both in the Claim's `claims.json` and in the
gitea issue body the localhost contrib agent files (ADR `0005`), which **locks the format** (the
hard-to-reverse cost is rewriting `agony/qatlas-lean` issue history once issues carry them).

Resolution (`kind:id` → title / authors / year) is **server-side and exact**: a new
`GET /api/papers/lookup?ids=arxiv:…,openalex:…,doi:…` batch endpoint answers from the **local
OpenAlex corpus** (ADR `0006`), returning per-ref `{ref, title, authors, year, hosted, resolved}`.
Free-text **fuzzy** literature search is a deliberately **separate, deferred** capability.

## Why namespaced `kind:id`, unversioned

- **Self-describing prefix** → the resolver dispatches straight to the right backend
  (`openalex:`/`doi:`/`arxiv:`) instead of inferring from shape; it is also human-readable in the
  issue body. Matches the lean OpenAlex MCP's own input modes (`openalex_lookup(arxiv_id|doi|openalex_id)`).
- **Unversioned**: a Reference cites a *work*, not a snapshot, so `arxiv:2208.06941` (no `vN`) —
  deliberately different from the paper-of-record `paper.id`, which QA's asset pipeline versions.
- A **Reference** (this bibliographic pointer) is distinct from a lean **reference lemma** (an
  in-corpus Lean reuse-shortlist entry — `reference_lemmas`) and from a **proof citation** (the
  proof-prose citation with a `citation_string`). The glossary records the distinction.

## Why server-side lookup, not client-side public-API resolution

ADR `0006` holds the **full** OpenAlex corpus locally precisely so "every `referenced_works` id
resolves locally — citation traversal never falls back to the public API." Resolving references
client-side against the public API is exactly the redundancy `0006` was built to eliminate. So:

- Lookup is a **host-core Paper/corpus capability** (it fills the spirit of the existing
  `papers/getCitedRefs` stub, `hostapi/core.go:89`), **not** a claim-domain endpoint — it does not
  violate ADR `0003`. It reads the OpenAlex corpus by ID; it knows nothing about Claims.
- Reading it is consistent with ADR `0005`: the contrib agent already **reads** paper bytes from QA
  (the suspend-and-wait endpoints); reference lookup is one more read. ADR `0005`'s "QA server is
  not involved" governs the **write** path (filing the gitea issue stays gitea-direct), not reads.
- **Graceful degradation** (mirrors ADR `0006`): when the corpus is unreachable the qatlas client
  may fall back to the public OpenAlex API for that session.

## Why `/api/papers/lookup` (the literature umbrella), not `/api/works` or `/api/openalex`

- The endpoint is a **path-only special case** under the existing `/api/papers/{path...}`
  catch-all, joining `/api/papers/stats` and `/api/papers/needs-mineru`; `/api/papers/{id}/markdown`
  and the rest of the per-id asset surface are untouched.
- The lookup spans the **broader literature** (the OpenAlex corpus is mostly works QA does **not**
  host) and flags each hit with `hosted`. A corpus hit that QA does not yet host can later trigger
  a background PDF download + MinerU convert and **become** a hosted Paper — so a single
  `/api/papers/` umbrella over "find literature" + "fetch a hosted Paper's assets" is coherent.
- The glossary still keeps **Paper catalog** and **OpenAlex corpus** as distinct *data stores*
  (ADR `0006`); the `/api/papers/` URL is only the literature *umbrella*. `works` / `openalex` were
  rejected as the resource noun: `works` reads as the entity not the operation, and `openalex`
  would weld the vendor name into the URL.

## Why exact-only this iteration; fuzzy search deferred

References are extracted from the **source paper**, which generally prints arXiv ids / DOIs, so an
**exact batch lookup by id** is efficient (corpus by-id SQL) and sufficient now. Resolving an
**id-less free-text citation** to the right work (fuzzy matching) is a harder, separate problem;
it is split out as a future `/api/papers/<search>` capability and **not built this iteration**.

## Why file-with-warning, not block, on an unresolved Reference

References **enrich** a Claim; they are not a correctness gate. OpenAlex coverage gaps (a brand-new
preprint, a book with no DOI, a typo'd id) must not block filing an otherwise-good Claim. The
contrib WebUI shows an unresolved Reference with a warning; the human drops / edits / keeps it. A
kept-unresolved Reference is stored as its raw `kind:id` with `"resolved": false`.

## Considered options

- **Bare ID (kind inferred)** / **full URI** — rejected (see "Why namespaced `kind:id`").
- **Client-side public-API resolution** — rejected: redundant against ADR `0006`'s local corpus;
  spreads load onto each user's OpenAlex key for data we already hold.
- **A new `/api/works` or `/api/openalex` resource** — rejected in favour of the `/api/papers/`
  literature umbrella + a `hosted` flag.
- **Block Confirm until every Reference resolves** — rejected: brittle against corpus coverage gaps.

## Consequences

- `GET /api/papers/lookup` lands as a path-only special case under `/api/papers/{path...}`. The
  `papers/getCitedRefs` stub's role is clarified as the paper-centric "what works does paper X
  cite" query, whose result items are themselves resolvable via `lookup`.
- `claims.json` and the gitea issue body gain a `references` list of `kind:id` (+ a `resolved`
  flag); the format is locked by issue history (no backwards-compat is owed pre-launch, but issue
  history is the practical lock).
- **Prover-side read view deferred (was the round-2 "G" item).** The prover consumes references
  over HTTP — `/api/papers/lookup` plus the existing paper-content endpoints — **not** a direct
  `paper_works` / corpus DB view. A direct prover DB read view is explicitly deferred; the trigger
  to revisit is when the prover needs live catalog state that HTTP cannot expose. This is why the
  original handoff's "design the prover's PG read schema this pass" is intentionally absent from
  ADRs `0001`–`0006`.
- **Fuzzy free-text literature search is a known future capability**, not built this iteration.
- lean drops its own OpenAlex MCP (see the lean implementation handoff); reference resolution now
  flows through the qatlas client → `/api/papers/lookup`.
