# Claim drafting + issue filing move to the upstream lean repo; QA's client claim plugin is unregistered

_Supersedes ADR `0005`._

Claim drafting and `type/theorem` issue filing **move out of the QuantumAtlas client and into the
upstream lean repo** (`agony/qatlas-lean`). What ADR `0005` shipped as a QA-side localhost plugin
(`qatlas contrib claim` — a Copilot/anthropic/openai-SDK drafter + WebUI under
`qatlas/client/claim/`) is **retired — kept as a first-party builtin plugin that ships with the
package but is off by default, enabled only via the config-driven `plugins` list** (see "How the
plugin platform stays generic" below); qatlas-lean now hosts the equivalent flow as its own
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
  (and, later, MCP — ADR `0007`); it never authors, persists, or maintains Claims/Theorems. Leaving
  the client claim plugin off by default (absent from the config `plugins` list) keeps QA client code
  out of the lean domain by default; the files remain but are inert until a user opts in via config.

## How the plugin platform stays generic (config-driven enablement)

The QA client plugin system is deliberately **generic** and **config-driven**, mirroring `qatlasd`'s
builtin-plugin model (ADR `0001`/`0003`): which first-party plugins are *enabled* is decided by the
user's config file — `config.yaml`'s `plugins:` list — **not hardcoded in code**. After
`pip install quantum-atlas`, a user turns a builtin on/off by editing that list; no code change is
needed. First-party plugins (`lean`, `claim`) ship with the package as a catalog in
`qatlas/client/plugins/registry.py`; a builtin contributes CLI commands only when its name is in the
enabled `plugins` list (default: `lean` only) **and** its `available()` env-check passes.
Third-party plugins register via `qatlas.plugins` entry points. So "retiring" the claim plugin is
just **leaving it out of the default enabled set** — its files stay as a shipped, opt-in plugin, and
`pyproject.toml`'s `contrib` extra (fastapi/uvicorn) is kept so it is runnable when enabled.
(`qatlasd` is Go with a different config path, but follows the same config-driven principle — see the
lean-side handoff.)

## What changes

- **Off by default in QA (config-driven, files kept)**: the client plugin roster is now config-driven
  — `qatlas/config.py` gains a `plugins` list and `qatlas/client/plugins/registry.py` enables only
  the builtins named in it (default `lean`). `claim` is **not** in the default set, so `qatlas
  contrib` no longer surfaces it; a user re-enables it with `plugins: [lean, claim]`. The plugin
  files (`qatlas/client/claim/`) + their tests (`tests/client/claim/`) are **kept** as a shipped,
  opt-in plugin; `pyproject.toml`'s `contrib` extra (fastapi/uvicorn) is **kept** so it runs when
  enabled. The old bespoke `claim_plugin_enabled` flag is dropped in favor of the generic `plugins`
  list. Only the *surfacing* docstrings + the `claim` reference in `qatlas/client/contrib.py` are
  updated to point at qatlas-lean.
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
- **Delete the plugin files outright.** Rejected: enablement is config-driven (a builtin is active
  only when named in the config `plugins` list), so a plugin that is simply *not in the default set*
  is already inert — deletion buys nothing over leaving it off by default. Keeping the files as a
  shipped, opt-in plugin preserves an optional local fallback + a reference implementation at zero
  active-surface cost. Drift risk is bounded: the plugin is off by default and explicitly marked
  non-canonical (qatlas-lean owns the maintained flow).
- **A per-plugin boolean flag (the old `claim_plugin_enabled`).** Rejected in favor of one generic
  `plugins` list: a bespoke on/off field per plugin does not generalize, whereas a single list scales
  to any builtin and reads as "the enabled set" in one place.

## Consequences

- The QA client surface shrinks: `qatlas contrib` loses the `claim` subcommand by default (the
  default enabled set is `lean` only). `claim` still ships with the package and is one config edit
  (`plugins: [lean, claim]`) away from returning.
- The **maintained/canonical** issue-body shape (`## Claim` / `## Why it matters` / `## Paper
  reference` / `## References` + a trailing `claim_id:` marker) now lives **once** in
  qatlas-lean's `contrib/claim/issue.py`. QA keeps only a dormant, unregistered copy (its
  `qatlas/client/claim/issue.py`), which is non-canonical and not surfaced.
- Pre-launch, no compatibility is owed; issue history filed by the old QA plugin is already in the
  canonical shape, so qatlas-lean's dedup recognises it unchanged.
