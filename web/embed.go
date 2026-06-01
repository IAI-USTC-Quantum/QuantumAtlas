// Package web embeds the Vite-built React SPA into the QuantumAtlas
// server binary. The artefacts live in this directory's dist/ subtree
// and are produced directly by `npm run build` (vite.config.ts pins
// build.outDir to ./dist, which is the default).
//
// Embedding the SPA from the same directory that owns the npm project
// means there is no "copy build output into a sibling package" step:
// `go build ./cmd/qatlasd` after `npm run build` always picks up the
// latest assets. Go's //go:embed rejects ".." path segments, so the
// embed.go file must sit next to dist/ — putting it in web/ keeps
// dist/ ownership with the package that produces it.
//
// The frontend is built with vite's default base of "/", so all asset
// URLs are absolute root paths (/assets/index-*.js etc.). The static
// handler in cmd/qatlasd/main.go falls back to /index.html on 404 to
// support SPA client-side routing.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the embedded SPA filesystem rooted at dist/ (so the
// root entries are index.html + assets/). Returns an error only if
// the embed itself is malformed, which would be a build-time bug.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// MustFS is FS that panics on error. Suitable for main() init.
func MustFS() fs.FS {
	sub, err := FS()
	if err != nil {
		panic("web: embedded fs unavailable: " + err.Error())
	}
	return sub
}
