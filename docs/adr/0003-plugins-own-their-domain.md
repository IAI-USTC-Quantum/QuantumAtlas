# Plugins own their domain end-to-end; the host core stays domain-agnostic

The host core (the plugin-agnostic spine of qatlasd) must not carry any single Plugin's domain
words or domain tables. Each Plugin **owns its domain end-to-end**: its vocabulary, its
PocketBase collections (migrations registered by the plugin's own Go package), its REST routes
under `/api/<plugin_id>/*`, its hostapi methods under the plugin's namespace (e.g.
`lean.verifications.submit`), and its in-memory caches.

## Why

The earlier wiring (commits `b79e622` + `160f806`) put lean-domain artifacts directly in the host
core: `internal/theorems/`, `internal/verifications/`, `internal/routes/theorems.go`,
`internal/routes/verifications.go`, plus `hostapi/core.go` exposing `theorems/get`,
`theorems/create`, and `verifications/submit` as generic host methods. That's a domain leak — the
host has no business knowing what a "theorem" or "verification" is; those are concepts of the
lean Plugin. Same leak with `pages/get` (wiki domain).

Keeping the leak forces every new Plugin to carve up host-core code, makes plugin extraction
impossible without API breaks, and prevents external (third-party) plugins from owning their own
schemas the way builtins can.

## What moves

- **Into the theorems builtin plugin** (`internal/theoremsplugin/` or similar): the `git pull`
  + read-through machinery, the in-memory caches of `registry.json` and the dependency graph,
  and the `/api/theorems/*` routes. This plugin **does not** own a PocketBase collection in
  this iteration (see ADR `0004`); proved Theorems are read-through from git.
- **Into the wiki builtin plugin**: `hostapi/core.go`'s `pages/get` (re-namespaced under
  `wiki.*`). The existing `internal/wiki/` package becomes the wiki plugin's implementation
  package.
- **Out of the tree entirely** (this iteration): `internal/theorems/`,
  `internal/routes/theorems.go`, `internal/verifications/`,
  `internal/routes/verifications.go`, and the lean-domain methods on `hostapi/core.go`
  (`theorems/get`, `theorems/create`, `verifications/submit`) — see ADR `0004` for the rationale.
- **Stays in host core**: plugin registry/manifest/lifecycle, the Paper catalog
  (`internal/papers/`, `paper_works` PG table — shared by every Plugin), auth/PAT, events bus,
  PocketBase, and the genuinely domain-neutral hostapi methods (`papers/getMarkdown`,
  `papers/getMeta`, `papers/getCitedRefs`, `events/publish`).

## The mechanism: one builtin-plugin registration hook

Replace the ad-hoc per-plugin `RegisterTheorems(...)`, `RegisterVerifications(...)`,
`RegisterWiki(...)`, `RegisterGraph(...)`, `RegisterRAG(...)` calls in `main.go` with a single
hook every builtin implements:

```go
type BuiltinPlugin interface {
    Manifest() plugin.Manifest                 // matches plugins/<id>/plugin.json
    RegisterMigrations(app core.App) error     // own PocketBase collections
    RegisterRoutes(se *core.ServeEvent, deps Deps) error  // /api/<id>/*
    RegisterHostAPI(r *hostapi.Registry) error // <id>.* methods (optional)
}
```

`main.go` iterates the builtin list and calls each hook. New builtin plugins land by adding to
the list — no host-core surgery.

### Optional capability: `GitPullPlugin` (lean + wiki)

`git pull --ff-only` is shared by builtins that read through an upstream git repo (wiki, lean).
We **extract** the wiki package's current pull/status helpers into a host-neutral
`internal/gitpull/` package (`Pull(dir) (*PullResult, error)`, `ReadGitInfo(dir) GitInfo`,
`PullError`, `PullResult`, `GitInfo` — no wiki/lean specifics), and we **expose pull as a
platform capability** via an optional sub-interface a builtin may implement:

```go
type GitPullPlugin interface {
    GitRepoDir() string         // working-tree path the host's `git pull --ff-only` runs in
    OnPullSucceeded() error     // plugin-specific post-pull work (wiki: cache.Refresh;
                                // lean: registryCache.Reload)
}
```

The host-core platform code (the same loop that calls `BuiltinPlugin` hooks) type-asserts each
builtin to `GitPullPlugin`; for those that satisfy it, the platform mounts a **uniform shape**
`POST /api/<id>/sync/pull` + `GET /api/<id>/sync/status` and guarantees both routes behave
identically across all pull-style plugins. Plugins that don't pull (graph, rag) do not implement
the sub-interface; the platform skips them.

### Why a sub-interface (not just a helper package)

Both options were considered. A pure helper (`gitpull.Pull` called from each plugin's own
`RegisterRoutes`) leaves route shape and error-mapping consistency to reviewer vigilance. The
sub-interface promotes "git-synced builtin" to a first-class platform capability, so the host
guarantees one canonical `/api/<id>/sync/*` contract for every plugin that opts in — and
external (socket/stdio) plugins that want the same behavior can opt in too. The cost is one tiny
optional Go interface; the gain is contract uniformity for what is otherwise a recurring shape.

## Consequences

- External (socket/stdio) plugins can also write to the theorems plugin's hostapi methods
  (under the plugin's own namespace, e.g. `theorems.*`) if/when that surface is added — not in
  this iteration. The push-oriented `verifications` collection that earlier commits added is
  removed entirely (see ADR `0004`).
- Routes follow the per-plugin namespace: `POST /api/theorems/sync/pull` (uniform shape mounted
  by `GitPullPlugin`), `GET /api/theorems/list`, `GET /api/wiki/pages`, etc.
- `hostapi/core.go` shrinks to genuinely host-shared concerns.
