# Claim drafting + issue filing move to the upstream lean repo; QA's client claim plugin is retired

_Supersedes ADR `0005`._

Claim drafting and `type/theorem` issue filing **move out of the QuantumAtlas client and into the
upstream lean repo** (`agony/qatlas-lean`). What ADR `0005` shipped as a QA-side localhost plugin
(`qatlas contrib claim` — a Copilot/anthropic/openai-SDK drafter + WebUI under
`qatlas/client/claim/`) is **retired**; qatlas-lean now hosts the equivalent flow as its own
`qatlas-lean contrib claim <paper>` localhost WebUI, driven by **qatlas-lean's CLI flat-agent
runner** (`copilot -p --agent claim-drafter`), **not an SDK**.

QuantumAtlas keeps exactly the roles it already owns: the **literature host** (paper markdown via
`GET /api/papers/{id}/markdown` + reference resolution via `GET /api/papers/lookup`, ADR `0007`) and
the **theorems pull-plugin** consumer of proved Theorems (ADR `0002`). qatlas-lean is now the
*client* of those read endpoints.

## Why move it to lean

- **One repo owns the whole prover-side surface.** Claim authoring → proving (solver) → auditing
  (the three Magi) → the registry/ledger now all live in `qatlas-lean`. A contributor reads one
  repo, and "all Lean-related content" has a single home — the opposite of ADR `0005`'s terminal-
  state sketch (which had QA's contrib WebUI *absorb* every lean agent and split qatlas-lean into a
  content-only `QuantumAtlas-Theorems` repo). That sketch is **abandoned**; this ADR is the new
  terminal state for the boundary.
- **No SDK in the contrib path.** ADR `0005` put a Copilot/anthropic/openai-SDK drafter in the QA
  client. qatlas-lean already runs every agent as a flat `copilot -p --agent <name>` CLI process; the
  claim drafter is just one more (`claim-drafter`). No second LLM-invocation mechanism, no QA-side
  LLM credentials.
- **The mock stays a complete stand-in.** The contrib flow reuses qatlas-lean's own Gitea client, so
  `backend=mock` files into the file-backed mock the daemon already polls and `backend=gitea` files
  live under the contributor's own token. The whole author → solve → audit pipeline is testable
  end-to-end with zero accounts (`tests/integration/claim_contrib.py`).
- **The QA boundary gets simpler, not richer.** QA exposes read-only paper/corpus data over HTTP
  (and, later, MCP — ADR `0007`); it never authors, persists, or maintains Claims/Theorems. Deleting
  the client claim plugin removes the one place QA client code reached into the lean domain.

## What changes

- **Removed from QA**: `qatlas/client/claim/` (the plugin, drafter, WebUI server, gitea filer, issue
  renderer, source/reference readers, models) and its tests `tests/client/claim/`; the `ClaimPlugin`
  registration in `qatlas/client/plugins/registry.py`; the `claim_plugin_enabled` flag in
  `qatlas/config.py`; the `claim` references in `qatlas/client/contrib.py` and the plugin-system
  docstrings.
- **Kept in QA**: the `lean` passthrough plugin (`qatlas/client/leanplugin/` — `qatlas lean
  <subcommand>` still drives a configured qatlas-lean checkout, so `qatlas lean contrib claim …`
  reaches the new flow); the literature read surface (`/api/papers/*`, ADR `0007`); the theorems
  pull-plugin (ADR `0002`).
- **Added in qatlas-lean** (its repo, not this one): `src/qatlas-lean/contrib/claim/` (the ported
  flow, stdlib `http.server`, CLI-agent drafter) + a `claim-drafter` flat agent + a `qa_base_url`
  config knob.

## What does NOT change (decisions reaffirmed, not reversed)

- **ADR `0004` — no QA-side Claim collection / no `/claims` page.** Still true; in fact reinforced —
  QA persists nothing about Claims. Only `0004`'s cross-reference to the drafting *mechanism* is
  updated (it now lives in qatlas-lean, not a QA Copilot-SDK plugin).
- **ADR `0007` — references are `kind:id`, resolved server-side via `/api/papers/lookup`.** The
  contract is identical; only the *caller* changes (qatlas-lean's claim flow instead of QA's contrib
  agent). `0007` already records this prover-as-client boundary.
- **ADR `0002` — the theorems plugin pulls proved Theorems from git.** Unchanged.

## Considered options

- **Keep the QA-side claim plugin (ADR `0005` as-is).** Rejected: splits Lean-related content across
  two repos, keeps an SDK code path in the QA client, and contradicts the "qatlas-lean owns the
  claim/proof/theorem workflow" boundary `0007` now records.
- **Deprecate-in-place (leave the plugin OFF-by-default but in the tree).** Rejected as the end
  state: an unmaintained, untested second implementation of the issue-body contract invites drift
  against the canonical one in qatlas-lean. The OFF-by-default flag was the transitional step; this
  ADR finishes the move by deleting it.

## Consequences

- The QA client surface shrinks: `qatlas contrib` loses the (already OFF-by-default) `claim`
  subcommand; the plugin registry only ships the `lean` passthrough as a built-in.
- The canonical issue-body shape (`## Claim` / `## Why it matters` / `## Paper reference` /
  `## References` + a trailing `claim_id:` marker) is now defined **once**, in
  qatlas-lean's `contrib/claim/issue.py`. QA no longer carries a copy.
- Pre-launch, no compatibility is owed; issue history filed by the old QA plugin is already in the
  canonical shape, so qatlas-lean's dedup recognises it unchanged.
