package web

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBundleUpgradeDoesNotReuseOldHTML(t *testing.T) {
	for _, version := range []string{"0.35.0", "0.36.0"} {
		source := fixtureTree()
		source["index.html"].Data = []byte("UI for " + version)
		var out bytes.Buffer
		if err := Pack(source, version, &out); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(out.Bytes())
		tree, err := openBundle(out.Bytes(), digest[:], version)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"/", "/doc/", "/assets/app.js"} {
			req := httptest.NewRequest("GET", target, nil)
			req.Header.Set("If-Modified-Since", "Fri, 30 Nov 1979 00:00:00 GMT")
			rec := httptest.NewRecorder()
			http.FileServer(http.FS(tree)).ServeHTTP(rec, req)
			if rec.Code != 200 || rec.Header().Get("Last-Modified") != "" {
				t.Fatalf("%s %s: version-independent ZIP timestamp leaked: %d %v", version, target, rec.Code, rec.Header())
			}
			if target == "/" && rec.Body.String() != "UI for "+version {
				t.Fatal("stale index after upgrade")
			}
		}
		if err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.ModTime().IsZero() {
				t.Errorf("%s has ZIP mtime instead of embed.FS semantics", name)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
