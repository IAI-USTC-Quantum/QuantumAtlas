# Embedded SPA

The QuantumAtlas frontend (Vite build artefacts) gets compiled in here as
part of the production build (P6). At dev time the directory may be empty;
the static handler in `cmd/server/main.go` simply 404s on misses and the
`/api/*` surface continues to work.
