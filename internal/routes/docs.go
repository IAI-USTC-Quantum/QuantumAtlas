// Docs site sourcing (/doc + /devdoc) — disk override with embedded
// fallback.
//
// Both sphinx sites are baked into the qatlasd binary (web/public/doc +
// web/public/devdoc → dist/ subtree of the embedded web FS, see
// web/embed.go), which makes a docs refresh indistinguishable from a full
// server redeploy. To decouple the two lifecycles, each site is sourced
// at boot from the FIRST available of:
//
//  1. ~/.qatlas/docs/<name> on disk — populated out-of-band by
//     deploy/update-docs.sh; the file server reads from disk per
//     request, so a docs update takes effect with NO restart;
//  2. the embedded <name>/ subtree of the web bundle — the always-
//     available baseline (local dev, deployments without the override
//     directory, rollback safety net).
//
// /doc is the public user site (no auth); /devdoc stays behind the admin
// ticket gate in devdoc.go — only the filesystem it serves changes.
package routes

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// DefaultDocsRoot is the on-disk docs override directory,
// ~/.qatlas/docs (next to config.yaml). Returns "" when the user home
// cannot be determined — ResolveDocsFS then skips the disk source.
func DefaultDocsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".qatlas", "docs")
}

// ResolveDocsFS picks the source for one docs site subtree (name is
// "doc" or "devdoc"): the disk override <docsRoot>/<name> when that
// directory exists and is non-empty, otherwise the embedded <name>/
// subtree of distFS. The returned source string is "disk", "embedded"
// or "none" (neither available) — meant for boot logging; the fs is nil
// exactly when the source is "none".
//
// The disk check runs once at boot; the file server then reads from disk
// per request, so content updates under <docsRoot>/<name> go live
// without a restart.
func ResolveDocsFS(distFS fs.FS, docsRoot, name string) (fs.FS, string) {
	if docsRoot != "" {
		dir := filepath.Join(docsRoot, name)
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			return os.DirFS(dir), "disk"
		}
	}
	if sub, err := fs.Sub(distFS, name); err == nil {
		if entries, err := fs.ReadDir(sub, "."); err == nil && len(entries) > 0 {
			return sub, "embedded"
		}
	}
	return nil, "none"
}

// RegisterDoc hosts the public user docs at /doc/*. docFS is the
// resolved docs filesystem (see ResolveDocsFS); nil means no docs are
// available, in which case the route is not registered and the SPA
// catch-all answers with its index fallback — the same behavior builds
// without the sphinx step have always had.
//
// The site is public by design (user documentation, not a write
// surface). Registered before the SPA catch-all so the more specific
// pattern wins and /doc/* never falls through to the SPA shell.
func RegisterDoc(se *core.ServeEvent, docFS fs.FS) {
	if docFS == nil {
		return
	}
	static := http.FileServer(http.FS(docFS))
	se.Router.GET("/doc/{path...}", func(re *core.RequestEvent) error {
		// FileServer expects a leading-slash request path within its
		// root; it serves index.html for directory requests and
		// canonicalizes ".../index.html" → "./" itself. The bare "/doc"
		// never reaches this handler (the mux 307s it to "/doc/"), so
		// directory URLs always keep their trailing slash — sphinx
		// pages reference assets relatively (_static/...) and depend
		// on it.
		path := re.Request.PathValue("path")
		re.Request.URL.Path = "/" + strings.TrimPrefix(path, "/")
		static.ServeHTTP(re.Response, re.Request)
		return nil
	})
}
