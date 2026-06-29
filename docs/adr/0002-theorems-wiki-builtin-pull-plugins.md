# The theorems and wiki plugins are builtin pull plugins, not a socket-push daemon

The **theorems plugin** (which pulls in proved Theorems from an upstream Lean-content repo —
today `agony/qatlas-lean`, eventually a dedicated `QuantumAtlas-Theorems` content-only repo)
and the **wiki plugin** (pulling in markdown knowledge pages from `QuantumAtlas-Wiki`) both
integrate as `kind=builtin` plugins that, exactly like graph and rag, expose `/api/theorems/*`
and `/api/wiki/*` as a read-through over a **server-side `git pull --ff-only`** checkout of
the upstream repo: the theorems plugin reads `registry.json` (+ `audit_records/certified.json`
+ evidence dossiers + the Lean source files referenced by `registry.json`); the wiki plugin
reads the markdown wiki cache. The wiki already pulls this way (`POST /api/wiki/sync/pull`);
we generalize the pattern.

We **reject** the socket-push model that an earlier in-tree branch
(`qatlas-lean@timidly/ts-qa-plugin`) was building: a long-running Lean daemon dialing into the
host over jsonrpc-ws, consuming theorem events, and pushing results back via
`verifications/submit`.

## Plugin naming: `theorems`, not `lean`

The plugin id is **`theorems`**, not `lean`. Reasons:

- It names the **content the plugin serves** (already-proved Theorems), not the prover language.
- It aligns with the eventual content-only repo `QuantumAtlas-Theorems` (this transitional
  iteration still pulls the larger `qatlas-lean` repo, but that's an implementation detail —
  plugin id ≠ upstream repo name, just as `graph` ≠ neo4j and `rag` ≠ qdrant).
- It avoids encoding "Lean 4 specifically" into the URL contract — a future Coq prover would
  still publish into this plugin's namespace.

## Why pull, not push

- theorems ↔ QA is a **loosely-coupled cooperation over the durable git artifact**, not a tight
  live RPC loop. The upstream Lean-content repo is the source of truth for proved Theorems;
  QA pulls it.
- Consistent with graph/rag (same-language Go builtin, read-through a backend, `/api/*`).
- The handoff removed the orchestrating daemon: **claim drafting and issue filing are a human
  + a localhost contrib agent webui** (`qatlas contrib claim`), not a host poll/push loop.
  There is no longer a daemon to hold the WS connection.
- One git-pull endpoint per plugin is far less to operate than a WS server + connection lifecycle.

## What the theorems plugin owns in this iteration

- A server-side git checkout of the Lean-content upstream and the `git pull --ff-only` machinery
  via `GitPullPlugin` (mounted at `POST /api/theorems/sync/pull`, `GET /api/theorems/sync/status`).
- An in-memory cache of `registry.json` + `audit_records/certified.json` + relevant Lean source
  texts, refreshed on every successful pull (mirrors `wiki.Cache.Refresh`).
- Read-only HTTP routes under `/api/theorems/*` for the WebUI: list/detail Theorems, the
  dependency-DAG view, axiom/sorry-free flags, source-on-demand, stats.

The theorems plugin **deliberately does not** own a PocketBase collection in this iteration.
Proved Theorems are read-through-from-git, not persisted into PocketBase; Claims (pre-proof NL
statements that the upstream `qatlas-lean` repo files as gitea issues) are out of scope for the
WebUI in this iteration — see ADR `0004-no-claim-collection-this-iteration.md`. When (later) we
need a PocketBase-persisted view, it lands as a separate ADR.

## Reference, don't port

`qatlas-lean@timidly/ts-qa-plugin` is **reference-only**: we absorb its good ideas but build on
QuantumAtlas `main`'s existing plugin platform (registry, manifest, graph/rag, the retained
external transports) — we do not cherry-pick or base on that branch.

## Consequences

- A proved Theorem's verdict is read from `registry.json`'s `audit_status` at pull time, **not**
  POSTed through anything. The push-oriented `verifications` collection + the lean-domain
  hostapi methods that earlier commits added (`b79e622` + `160f806`) are removed (see ADR
  `0004`).
- The `external` (socket/stdio) transports stay in the tree for future third-party plugins, but
  no first-party plugin uses them in this iteration.
- The WebUI gains a `/theorems` page (read-through over the plugin) and a "Pull Now" button +
  webhook trigger that calls `POST /api/theorems/sync/pull` — symmetric with `/wiki`.
