# No QA-side Claim collection or Claims page this iteration

We do **not** persist Claims (pre-proof natural-language statements extracted from papers) into
PocketBase, do **not** add a `/claims` page to QA's WebUI, and do **not** keep the
`internal/theorems/` (which actually stored Claims, not Theorems) or `internal/verifications/`
collections, routes, or hostapi methods that `b79e622` + `160f806` added. They are removed.

Claim drafting and issue filing happen entirely **on the user's localhost**, in a Copilot-SDK-
driven contrib agent + WebUI (`qatlas contrib claim` — see ADR `0005`). The "Confirm claim"
button in that localhost WebUI **only files a gitea issue** on `agony/qatlas-lean`. QA's server
sees nothing until the issue produces a proved Theorem that lands in the upstream Lean-content
repo and the **theorems plugin** pulls it (ADR `0002`).

## Why nothing in QA this iteration

The handoff lists `papers / claims / units` as WebUI ground-truth surfaces, but the user has
clarified that this is a **terminal-state** vision: in the terminal state, the localhost
contrib WebUI absorbs every Lean-side agent (scout, statement, enricher, issue-raiser, solver,
repair, the three Magi), the upstream splits into a content-only `QuantumAtlas-Theorems` repo,
and Claims may then be surfaced from a different substrate. We refuse to design QA-side Claim
storage twice.

For this transitional iteration the relevant facts are:

- **No QA-side persistence is needed**. The localhost agent already maintains its own session
  state; gitea is the authoritative inbox for filed Claims; the upstream Lean-content repo is
  the authoritative store for proved Theorems.
- **No QA-side display is needed**. The WebUI shows `/wiki` (markdown content) and `/theorems`
  (proved Theorems via the theorems plugin); both come straight from `git pull`. The "list of
  Claims awaiting proof" view, if it ever exists, can be derived later from gitea or from the
  terminal-state contrib WebUI — not from a QA collection.
- **Domain words don't belong in host core**. `internal/theorems/` + `internal/verifications/`
  in `b79e622` were domain words sitting at host-core level (see ADR `0003`); even if we
  wanted Claims later they would not return to that location.

## Consequences

- `internal/theorems/` (storing pre-proof statement_nl, i.e. **Claims** under the corrected
  glossary), `internal/routes/theorems.go`, `internal/verifications/`,
  `internal/routes/verifications.go`, the theorems/verifications methods on
  `hostapi/core.go`, the registrations in `main.go`, and the related tests are all removed in
  this iteration.
- The `theorems` plugin id is freed for its proper meaning (proved Theorems, read-through from
  git — ADR `0002`).
- `b79e622`/`160f806`'s tests for the deleted theorems/verifications collections drop with
  them; the plugin-platform tests and the external-transport (socket/stdio) tests stay.
- The localhost contrib agent's WebUI keeps Claim drafts in **its own state**; QA never sees
  them. When the terminal-state `QuantumAtlas-Theorems` repo exists, "Confirmed Claims" are
  recorded as dossiers inside that content repo, alongside the proved Theorems — at which
  point the theorems plugin's git-pull pipeline picks them up naturally without a new
  PocketBase collection.
