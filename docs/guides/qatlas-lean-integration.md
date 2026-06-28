# qatlas-lean plugin integration

This guide describes how to connect a `qatlas-lean` formal-verification
daemon to a running QuantumAtlas server. QuantumAtlas supports native
`in-process-go` plugins and external `jsonrpc-ws` plugins under one manifest
model. `qatlas-lean` is an external plugin: it uses one outbound WebSocket
JSON-RPC connection to the server and does not expose public HTTP endpoints.

## Architecture

1. `qatlasd` loads plugin manifests from `QATLAS_PLUGINS_DIR`.
2. `qatlas-lean` connects to `QATLAS_RPC_WS_BIND` and sends `initialize`
   with `{id, secret, abi_version}`.
3. `qatlasd` replies with granted scopes and host capabilities.
4. `qatlasd` pushes `theorem.added` / `theorem.updated` events to subscribed
   plugins.
5. `qatlas-lean` calls host capabilities such as `pages/get`,
   `papers/getMarkdown`, and `verifications/submit`.
6. The web/API reads theorem and verification state from the core
   `theorems` and `verifications` collections.

The WebSocket transport is message-framed JSON-RPC: each WebSocket message is
one JSON object. Do not use `Content-Length` stream framing on this transport.

## Server configuration

Set these environment variables for `qatlasd`:

```bash
QATLAS_PLUGINS_DIR=/path/to/qatlasd/plugins
QATLAS_RPC_WS_BIND=127.0.0.1:8799
QATLAS_PLUGIN_CONNECT_SECRET=<shared-secret>
```

For an isolated smoke test, also isolate mutable server state:

```bash
QATLAS_PB_DATA_DIR=/tmp/qatlas-smoke/pb_data
QATLAS_DATA_DIR=/tmp/qatlas-smoke/data
QATLAS_WIKI_DIR=/tmp/qatlas-smoke/wiki
QATLAS_RAW_DIR=/tmp/qatlas-smoke/raw
```

If the smoke test uses S3/RustFS, use dedicated test buckets and test
credentials. Do not point a smoke test at production buckets.

## Plugin manifest

Place the `qatlas-lean` manifest at:

```text
$QATLAS_PLUGINS_DIR/lean/plugin.json
```

The manifest shape is:

```json
{
  "id": "lean",
  "name": "qatlas-lean",
  "version": "1.0.0",
  "abi_version": "1",
  "transport": "jsonrpc-ws",
  "spawn": null,
  "contributes": {
    "capabilities": ["theorem.verify", "proof.render"],
    "subscribes": ["theorem.added", "theorem.updated"],
    "publishes": ["lean.verification.completed", "lean.claim.blocked"]
  },
  "needs": [
    "papers:read",
    "wiki:read",
    "theorems:read",
    "theorems:write",
    "verifications:write"
  ]
}
```

## Host capabilities

After `initialize`, the plugin can call:

| Method | Purpose |
|---|---|
| `pages/get` | Return wiki frontmatter and body for a page id |
| `papers/getMarkdown` | Return cached paper markdown bytes |
| `papers/getMeta` | Return minimal paper metadata |
| `papers/getCitedRefs` | Return citation references; currently an empty list when no local citation data is available |
| `search/query` | Search cached wiki pages |
| `theorems/get` | Return a theorem record |
| `theorems/create` | Create or update a theorem record |
| `verifications/submit` | Create or update a verification record |
| `events/publish` | Publish plugin events such as `lean.verification.completed` |

`verifications/submit` accepts verdicts:

```text
verified | refuted | disputed | intractable | pending | failed
```

## Smoke test

1. Start `qatlasd` with isolated state and the `lean` plugin manifest.
2. Start `qatlas-lean` with:

   ```bash
   QATLAS_QA_RPC_URL=ws://127.0.0.1:8799/rpc
   QATLAS_QA_CONNECT_SECRET=<shared-secret>
   ```

3. Confirm the plugin is connected:

   ```bash
   curl -s "$QATLAS_SERVER_URL/api/v1/plugins" \
     -H "Authorization: Bearer $QATLAS_TOKEN"
   ```

4. Create a theorem:

   ```bash
   curl -s -X POST "$QATLAS_SERVER_URL/api/v1/theorems" \
     -H "Authorization: Bearer $QATLAS_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "id": "thm-smoke",
       "page_id": "thm-smoke",
       "paper_id": "2501.00010v1",
       "statement_nl": "Smoke theorem for plugin integration."
     }'
   ```

5. Watch `qatlas-lean` logs for `theorem.added`, host capability calls, and
   `verifications/submit`.

6. Confirm verification state in QuantumAtlas:

   ```bash
   curl -s "$QATLAS_SERVER_URL/api/v1/verifications?theorem_id=thm-smoke" \
     -H "Authorization: Bearer $QATLAS_TOKEN"
   ```

The expected result is one verification row with `plugin_id: "lean"` and a
terminal verdict such as `verified`, `refuted`, `disputed`, or `intractable`.

## Troubleshooting

| Symptom | Check |
|---|---|
| `lean` is `disabled` or absent in `/api/v1/plugins` | Manifest location and `QATLAS_PLUGINS_DIR` |
| WebSocket closes during initialize | `id`, `abi_version`, and `QATLAS_PLUGIN_CONNECT_SECRET` |
| `papers/getMarkdown` fails | Paper markdown is not cached in the configured RAW/S3 backend |
| Theorem creation succeeds but plugin sees no event | Plugin manifest `subscribes` includes `theorem.added`; plugin is connected when the theorem is created |
| Verification submit fails | Verdict is in the accepted vocabulary; plugin manifest includes `verifications:write` |
