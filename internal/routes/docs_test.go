// Tests for the docs-site source resolution (ResolveDocsFS) and the
// public /doc host (RegisterDoc) — see docs.go.
package routes

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// embeddedDist returns a stand-in for the embedded web/dist bundle with
// both docs subtrees populated.
func embeddedDist() fstest.MapFS {
	return fstest.MapFS{
		"doc/index.html":          &fstest.MapFile{Data: []byte("<h1>embedded user docs</h1>")},
		"doc/guide/index.html":    &fstest.MapFile{Data: []byte("<h1>embedded guide</h1>")},
		"devdoc/dev/index.html":   &fstest.MapFile{Data: []byte("<h1>embedded dev docs</h1>")},
		"index.html":              &fstest.MapFile{Data: []byte("<h1>spa</h1>")},
		"assets/index-deadbeef.js": &fstest.MapFile{Data: []byte("// js")},
	}
}

func writeDiskDocs(t *testing.T, root, name, marker string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(marker), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestResolveDocsFS_DiskPreferredOverEmbedded(t *testing.T) {
	root := t.TempDir()
	writeDiskDocs(t, root, "doc", "<h1>disk user docs</h1>")

	docFS, src := ResolveDocsFS(embeddedDist(), root, "doc")
	if src != "disk" {
		t.Fatalf("source = %q, want disk", src)
	}
	data, err := fs.ReadFile(docFS, "index.html")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "disk user docs") {
		t.Errorf("resolved fs does not serve the disk override: %s", data)
	}
}

func TestResolveDocsFS_EmbeddedWhenNoDiskDir(t *testing.T) {
	docFS, src := ResolveDocsFS(embeddedDist(), t.TempDir(), "doc")
	if src != "embedded" {
		t.Fatalf("source = %q, want embedded", src)
	}
	data, err := fs.ReadFile(docFS, "index.html")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "embedded user docs") {
		t.Errorf("resolved fs does not serve the embedded bundle: %s", data)
	}
}

func TestResolveDocsFS_EmptyDiskDirFallsBackToEmbedded(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "doc"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, src := ResolveDocsFS(embeddedDist(), root, "doc"); src != "embedded" {
		t.Errorf("empty disk dir: source = %q, want embedded", src)
	}
}

func TestResolveDocsFS_EmptyRootSkipsDisk(t *testing.T) {
	if _, src := ResolveDocsFS(embeddedDist(), "", "doc"); src != "embedded" {
		t.Errorf("empty docsRoot: source = %q, want embedded", src)
	}
}

func TestResolveDocsFS_NoneWhenNeitherAvailable(t *testing.T) {
	dist := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>spa</h1>")},
	}
	docFS, src := ResolveDocsFS(dist, t.TempDir(), "doc")
	if src != "none" || docFS != nil {
		t.Errorf("source = %q fs = %v, want none / nil", src, docFS)
	}
}

// docMux builds a router with only RegisterDoc mounted, mirroring the
// devdoc harness construction.
func docMux(t testing.TB, docFS fs.FS) http.Handler {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterDoc(e, docFS)
		m, mErr := e.Router.BuildMux()
		if mErr != nil {
			return mErr
		}
		built = m
		return nil
	})
	if err != nil {
		t.Fatalf("OnServe trigger: %v", err)
	}
	if built == nil {
		t.Fatal("mux not built by OnServe trigger")
	}
	return built
}

func TestAPI_Doc_ServesPublicSite(t *testing.T) {
	sub, err := fs.Sub(embeddedDist(), "doc")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	mux := docMux(t, sub)

	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		return rec
	}

	// No auth anywhere — the user docs are public by design.
	if rec := get("/doc/"); rec.Code != http.StatusOK {
		t.Errorf("GET /doc/: status = %d, want 200", rec.Code)
	} else if body, _ := io.ReadAll(rec.Result().Body); !strings.Contains(string(body), "embedded user docs") {
		t.Errorf("GET /doc/ wrong content: %s", body)
	}

	if rec := get("/doc/guide/"); rec.Code != http.StatusOK {
		t.Errorf("GET /doc/guide/: status = %d, want 200", rec.Code)
	}

	// The bare root is redirected to the trailing-slash form by the mux
	// (relative asset URLs in sphinx pages depend on it).
	if rec := get("/doc"); rec.Header().Get("Location") != "/doc/" ||
		(rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusTemporaryRedirect) {
		t.Errorf("GET /doc: status = %d location = %q, want 30x → /doc/",
			rec.Code, rec.Header().Get("Location"))
	}

	// Missing pages 404 instead of falling through to the SPA shell.
	if rec := get("/doc/no-such-page"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /doc/no-such-page: status = %d, want 404", rec.Code)
	}
}

func TestAPI_Doc_NilFSRegistersNothing(t *testing.T) {
	mux := docMux(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/doc/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /doc/ with nil docFS: status = %d, want 404", rec.Code)
	}
}
