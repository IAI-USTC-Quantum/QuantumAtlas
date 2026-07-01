# Claim drafting + issue filing move to the upstream lean repo; QA's client claim plugin is unregistered

_Supersedes ADR `0005`._

Claim drafting and `type/theorem` issue filing **move out of the QuantumAtlas client and into the
upstream lean repo** (`agony/qatlas-lean`). What ADR `0005` shipped as a QA-side localhost plugin
(`qatlas contrib claim` — a Copilot/anthropic/openai-SDK drafter + WebUI under
`qatlas/client/claim/`) is **retired — unregistered from the builtin plugin registry, its files
kept in place as a dormant, re-registerable plugin** (see "How the plugin platform stays generic"
below); qatlas-lean now hosts the equivalent flow as its own
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
  (and, later, MCP — ADR `0007`); it never authors, persists, or maintains Claims/Theorems.
  Unregistering the client claim plugin removes the one *active* place QA client code reached into
  the lean domain (the files remain, but inert until re-registered).

## How the plugin platform stays generic (register-to-activate)

The QA client plugin system is deliberately **generic**, mirroring `qatlasd`'s builtin-plugin model
(ADR `0001`/`0003`): a plugin contributes commands/features **only once it is registered**. So
"retiring" the claim plugin is just **removing it from the builtin registry**
(`qatlas/client/plugins/registry.py` no longer lists `ClaimPlugin`) — it does **not** require
deleting code. The `qatlas/client/claim/` files + `tests/client/claim/` stay as a **valid,
re-registerable-but-inactive** plugin: `config.py` keeps the `claim_plugin_enabled` gate and
`pyproject.toml` keeps the `contrib` extra (fastapi/uvicorn) so the dormant plugin remains
self-consistent and runnable if a future maintainer re-registers it. Nothing in `qatlas contrib`
surfaces it today.

## What changes

- **Unregistered in QA (files kept in place)**: `qatlas/client/plugins/registry.py` no longer lists
  `ClaimPlugin` as a builtin, so `qatlas contrib` no longer surfaces `claim`. The plugin files
  (`qatlas/client/claim/`) + their tests (`tests/client/claim/`) are **kept** as a dormant,
  re-registerable plugin; `qatlas/config.py`'s `claim_plugin_enabled` gate and `pyproject.toml`'s
  `contrib` extra are **kept** so it stays self-consistent. Only the *surfacing* is updated: the
  plugin-system docstrings + the `claim` reference in `qatlas/client/contrib.py` now point at
  qatlas-lean.
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

- **Keep the QA-side claim plugin ACTIVE (ADR `0005` as-is).** Rejected: splits the *maintained*
  Lean-related flow across two repos, keeps an SDK code path live in the QA client, and contradicts
  the "qatlas-lean owns the claim/proof/theorem workflow" boundary `0007` now records.
- **Delete the plugin files outright.** Rejected: the client plugin platform is generic
  (register-to-activate, like qatlasd's builtins), so an *unregistered* plugin is already inert —
  deletion buys nothing over unregistering. Keeping the files as a dormant, re-registerable plugin
  preserves an optional local fallback + a reference implementation at zero active-surface cost.
  Drift risk is bounded: the plugin is unregistered and explicitly marked non-canonical (qatlas-lean
  owns the maintained flow).

## Consequences

- The QA client surface shrinks: `qatlas contrib` loses the `claim` subcommand; the plugin registry
  only ships the `lean` passthrough as a registered built-in. The claim plugin stays in the tree as a
  dormant, re-registerable plugin.
- The **maintained/canonical** issue-body shape (`## Claim` / `## Why it matters` / `## Paper
  reference` / `## References` + a trailing `claim_id:` marker) now lives **once** in
  qatlas-lean's `contrib/claim/issue.py`. QA keeps only a dormant, unregistered copy (its
  `qatlas/client/claim/issue.py`), which is non-canonical and not surfaced.
- Pre-launch, no compatibility is owed; issue history filed by the old QA plugin is already in the
  canonical shape, so qatlas-lean's dedup recognises it unchanged.
