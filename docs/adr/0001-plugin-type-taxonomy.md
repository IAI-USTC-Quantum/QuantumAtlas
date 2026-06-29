# Plugin type taxonomy: kind (builtin/external) × transport (socket/stdio)

The plugin manifest originally encoded a plugin's type as one `transport` enum
(`in-process-go`, `jsonrpc-ws`, `jsonrpc-stdio`), conflating four unrelated things: origin
(first-party vs third-party), locality (in-process vs separate process), implementation language,
and wire protocol. We split it into two orthogonal axes modeled on VSCode extensions and Zotero
plugins:

- **`kind`** — `builtin` (first-party, compiled into qatlasd, runs in-process as Go) or
  `external` (third-party, a separate process speaking JSON-RPC to the host).
- **`transport`** — meaningful only for `external`: `socket` (the plugin dials into the host's
  WebSocket endpoint and authenticates with a connect secret) or `stdio` (the host spawns the
  plugin executable from `spawn.command` and talks JSON-RPC over its stdin/stdout, the
  LSP/DAP model). `builtin` needs no transport (the call is an in-process function call).

Migration from the old enum: `in-process-go → kind=builtin`; `jsonrpc-ws → kind=external,
transport=socket`; `jsonrpc-stdio → kind=external, transport=stdio`. JSON-RPC is the only RPC
dialect, so it no longer appears in any name.

## Considered options

- **Keep the single `transport` enum.** Rejected: one field muddles origin/locality/language/wire,
  and the names leak the wire protocol (`jsonrpc-ws`) instead of the concept (an external plugin
  that dials in).

## Consequences

- `manifest.go` (the `Manifest` struct + `Validate`) and every `plugins/<id>/plugin.json` change
  to the two-axis shape. `builtin`/`socket` require `spawn == null`; `stdio` requires
  `spawn.command`.
- All four first-party plugins (graph, rag, wiki, lean) are `kind=builtin`.
- The `external` socket/stdio transports (the existing `plugin/rpc.go` + `hostapi`) are retained
  for genuine third-party plugins — not removed.
