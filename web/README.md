# QuantumAtlas Web

Unified Vite + React workspace for QuantumAtlas pages.

- Source lives in `web/src`.
- Build output is copied to `atlas/server/static/web`.
- FastAPI serves the built shell for `/`, `/wiki`, `/wiki/search`, `/wiki/page/*`, `/graph`, `/graph/node/*`, and `/token`.
- Runtime data comes from `/api/*`; the backend should not contain page templates or UI design.

Common commands:

```bash
npm install
npm run lint
npm run build
cp -a dist/. ../atlas/server/static/web/
```
