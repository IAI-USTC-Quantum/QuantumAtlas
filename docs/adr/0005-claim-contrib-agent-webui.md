# Claim drafting lives in a localhost contrib agent + WebUI

The `qatlas contrib claim` command starts a **localhost** WebUI and a Copilot-SDK-driven custom
agent that, with a human in the loop, drafts one or more Claims against a paper (paper-of-record +
near-verbatim NL statement + stated/implicit assumptions + reference IDs the agent resolved
against OpenAlex / arxiv). The command name reflects the **kind of contribution**, not the
per-session count — one session may yield several Claim drafts (matching scout's "extract every
formalization-ready claim" contract). When the user clicks **Confirm claim** (中文: **确认
claim**) on a single drafted Claim in the WebUI, the localhost process files **one gitea issue**
per click on `agony/qatlas-lean` under the user's own gitea token — and that is the end of the
workflow for this iteration. The QA server is not involved (see ADR `0004`).

This pattern is the **first** of a planned family of localhost contrib WebUIs. The terminal
state merges every Lean-side agent (`scout`, `statement`, `enricher`, `issue-raiser`, `solver`,
`repair`, `melchior`, `balthasar`, `casper`) into the same contrib WebUI surface, alongside a
content-only `QuantumAtlas-Theorems` repo that replaces the code-bearing parts of today's
`qatlas-lean` repo. This iteration ships only the Claim-drafting workflow; the rest is named
for forward consistency.

## Why a localhost agent + WebUI, not a CLI one-shot

- A CLI that pipelines `scout → enricher → issue-raiser → POST gitea` in one shot puts the LLM
  in direct contact with gitea, with no human review of the produced Claim. The handoff lists
  Claim quality as a hard requirement ("paper-of-record + reference IDs + NL statement
  near-verbatim + stated/implicit assumptions"); LLMs don't reliably meet that bar one-shot.
- A localhost WebUI gives the human a place to interact with the agent — read, edit, ask for
  another draft, accept references, drop bad ones — without standing up a full web app or
  asking the QA server to run an LLM.

## Why **localhost**, not the QA server

- The QA server does not, and per the handoff should not, run LLM agents. Putting Copilot-SDK
  code in the Go `qatlasd` process would create token management, billing, and concurrency
  problems we don't want.
- Each user holds their own Copilot SDK credentials and their own gitea token locally; the QA
  server never needs to broker either.
- The localhost WebUI is short-lived per session — the user runs `qatlas contrib claim`, the
  browser pops, the workflow runs, the process exits when done.

## The "Confirm" button is a gitea-issue write

- Button text: English **"Confirm claim"**, Chinese **"确认 claim"** (the domain term `claim`
  is preserved across locales, matching the underlying schema/CLI/URL vocabulary).
- Behaviour: the localhost agent renders the drafted Claim into the canonical issue body
  shape, calls gitea's `POST /repos/agony/qatlas-lean/issues` with the user's token, and shows
  the resulting issue URL.
- Once issues are filed, the legacy in-repo `qatlas-lean` Lean-prover agents (solver, magi,
  etc.) pick them up exactly as today — this iteration changes nothing on the prover side.

## QA-side pickup

- A proved Theorem reaches the QA WebUI via the `theorems` builtin plugin's git-pull (ADR
  `0002`): the user clicks "Pull Now" on `/theorems` (or a webhook fires), the plugin runs
  `git pull --ff-only` on its `qatlas-lean` checkout, and `/theorems` reflects new entries.
- There is no QA-side write path for Claims (ADR `0004`); the QA server never sees a Claim
  until it has become a proved Theorem in the upstream repo's `registry.json`.

## Naming

- CLI: `qatlas contrib claim` (singular — the command names the **act of contributing a
  Claim**; one webui session may produce several Claim drafts and file several gitea issues,
  but the command name reflects the contribution kind, not the per-session count). The
  existing `qatlas contrib` group already carries "contributor workflows" (today: PDF /
  MinerU uploads); this slots in as the first agent-driven contributor workflow.
- Button text: English **"Confirm claim"** / Chinese **"确认 claim"** — each click confirms
  exactly one Claim and files exactly one gitea issue (so the button text is singular and
  matches the per-click semantics).
- Terminal-state additions, not built this iteration: a `qatlas contrib theorem`
  subcommand that drives the full proof flow (solver + Magi), and a `qatlas contrib audit`
  that drives audit-existing. Same singular-action naming rule.

## Consequences

- The Python `qatlas` package gains a `qatlas.client.contrib.claim` module: a process that
  starts a localhost HTTP server, opens a browser, drives a Copilot-SDK custom agent (system
  prompt = scout + enricher + issue-raiser combined), uses paper bytes via the QA suspend-and-
  wait endpoints, and posts to gitea on each Confirm.
- The QA WebUI **does not** gain a `/claims` page in this iteration (ADR `0004`). It does
  gain `/theorems` (read-through, ADR `0002`).
- Going from `claim` to terminal-state `theorem`-flow contrib is additive — no rework of the
  Claim flow is forced.
