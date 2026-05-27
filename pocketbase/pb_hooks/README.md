# PocketBase JS Hooks

This directory contains JavaScript hooks that extend PocketBase's
built-in functionality via the JSVM runtime.

## Files

- `pat.pb.js` — PAT (Personal Access Token) self-service endpoints
  (created in Stage 2)

## Development

Hooks are hot-reloaded by PocketBase on file change (Linux/macOS only).
After modifying a hook, verify with:

```bash
curl http://127.0.0.1:8090/api/health
```

Reference: https://pocketbase.io/docs/js-overview/
