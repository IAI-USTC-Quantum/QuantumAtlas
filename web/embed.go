//go:build embedui

package web

import (
	"embed"
	"io/fs"
)

// Release builds opt in with -tags embedui after building the full UI and docs.
// Missing dist is intentionally a compile error for this build mode. Ordinary
// go install builds use embed_none.go and require only tracked Go source.
//
//go:embed all:dist
var embedded embed.FS

func embeddedFS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
