# External plugin integration

QuantumAtlas plugins share one manifest and capability model with two
orthogonal axes (ADR `0001`):

- **`kind`** — `builtin` (first-party, compiled into `qatlasd`, runs in-process
  as Go) or `external` (third-party, a separate process speaking JSON-RPC to
  the host).
- **`transport`** — meaningful only for `external`: `socket` (the plugin dials
  into the host's WebSocket endpoint and authenticates with a connect secret)
  or `stdio` (the host spawns the plugin executable from `spawn.command` and
  talks JSON-RPC over its stdin/stdout, the LSP/DAP model).

The four first-party plugins — **graph**, **rag**, **wiki**, **theorems** — are
all `kind=builtin`. They need **no integration steps**: they are compiled into
`qatlasd` and appear in `/api/v1/plugins` as `connected` on boot. The wiki and
theorems plugins read through a server-side `git pull --ff-only` checkout and
expose a uniform `POST /api/<id>/sync/pull` + `GET /api/<id>/sync/status` pair.

This guide covers connecting a **third-party `external` plugin**. (The
`external` transports are retained for genuine third-party plugins; no
first-party plugin uses them.)

## Architecture (socket transport)

1. `qatlasd` loads plugin manifests from `QATLAS_PLUGINS_DIR`.
2. The external plugin connects to `QATLAS_RPC_WS_BIND` and sends `initialize`
   with `{id, secret, abi_version}`.
3. `qatlasd` replies with granted scopes and host capabilities.
4. The plugin calls host capabilities and publishes events over that
   connection.

The WebSocket transport is message-framed JSON-RPC: each WebSocket message is
one JSON object.

## Server configuration

```bash
QATLAS_PLUGINS_DIR=/path/to/qatlasd/plugins
QATLAS_RPC_WS_BIND=127.0.0.1:8799
QATLAS_PLUGIN_CONNECT_SECRET=<shared-secret>
```

## Plugin manifest

Place the manifest at `$QATLAS_PLUGINS_DIR/<id>/plugin.json`:

```json
{
  "id": "my-plugin",
  "name": "My external plugin",
  "version": "1.0.0",
  "abi_version": "1",
  "kind": "external",
  "transport": "socket",
  "spawn": null,
  "contributes": {
    "capabilities": ["myplugin.do-thing"],
    "subscribes": [],
    "publishes": ["myplugin.completed"]
  },
  "needs": ["papers:read", "wiki:read"]
}
```

For `transport=stdio`, set `"spawn": {"command": "my-plugin", "args": [...]}`
instead of `null`; the host spawns the process and frames JSON-RPC over its
stdio.

## Host capabilities

After `initialize`, the plugin can call these host-shared capabilities (the
host core carries no plugin-domain methods — ADR `0003`):

| Method | Purpose | Scope |
|---|---|---|
| `pages/get` | Return wiki frontmatter and body for a page id | `wiki:read` |
| `search/query` | Search cached wiki pages | `wiki:read` |
| `papers/getMarkdown` | Return cached paper markdown bytes | `papers:read` |
| `papers/getMeta` | Return minimal paper metadata | `papers:read` |
| `papers/getCitedRefs` | Return the works a paper cites (currently a stub; resolvable via `/api/papers/lookup`) | `papers:read` |
| `events/publish` | Publish plugin events | — |

## Smoke test

1. Start `qatlasd` with the external plugin manifest in `QATLAS_PLUGINS_DIR`.
2. Start the plugin pointed at `ws://127.0.0.1:8799/rpc` with the shared secret.
3. Confirm the plugin is connected:

   ```bash
   curl -s "$QATLAS_SERVER_URL/api/v1/plugins" -H "Authorization: ******"
   ```

   The plugin should show `status: "connected"`.

## Troubleshooting

| Symptom | Check |
|---|---|
| Plugin is `disabled` or absent in `/api/v1/plugins` | Manifest location and `QATLAS_PLUGINS_DIR` |
| WebSocket closes during initialize | `id`, `abi_version`, and `QATLAS_PLUGIN_CONNECT_SECRET` |
| `papers/getMarkdown` fails | Paper markdown is not cached in the configured RAW/S3 backend |
| Capability call returns insufficient-scope | Manifest `needs` includes the scope listed above |
