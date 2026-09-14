package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func fixtureTree() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte(`<title>QuantumAtlas</title><div id="root"></div><script src="/assets/app.js"></script>`)},
		"assets/app.js":         {Data: []byte("console.log('same UI');")},
		"doc/index.html":        {Data: []byte("public documentation")},
		"doc/_static/theme.css": {Data: []byte("body{color:black}")},
		"devdoc/dev/index.html": {Data: []byte("developer documentation")},
		".nojekyll":             {},
	}
}

func fixtureBundle(t *testing.T, version string) ([]byte, []byte) {
	t.Helper()
	var out bytes.Buffer
	if err := Pack(fixtureTree(), version, &out); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(out.Bytes())
	return out.Bytes(), digest[:]
}

func TestReleaseVersion(t *testing.T) {
	for _, good := range []string{"0.35.0", "v0.35.0", "0.35.0-rc.1", "1.0.0-rc.1+build.2"} {
		if _, err := ReleaseVersion(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
	for _, bad := range []string{"", "dev", "latest", "(devel)", "1.2", "01.2.3", "1.2.3a1", "1.2.3-01", "1.2.3-rc.01", "../1.2.3", "0.0.0-20260401000000-abcdefabcdef", "0.35.1-0.20260401000000-abcdefabcdef", "0.35.0\n"} {
		if _, err := ReleaseVersion(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestBundleDeterminismAndFilesystem(t *testing.T) {
	data, digest := fixtureBundle(t, "0.35.0")
	again, _ := fixtureBundle(t, "0.35.0")
	if !bytes.Equal(data, again) {
		t.Fatal("packaging is not deterministic")
	}
	tree, err := openBundle(data, digest, "0.35.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(tree, "index.html", "assets/app.js", "doc/index.html", "devdoc/dev/index.html", "doc/_static/theme.css", ".nojekyll"); err != nil {
		t.Fatal(err)
	}
	for name, file := range fixtureTree() {
		got, err := fs.ReadFile(tree, name)
		if err != nil || !bytes.Equal(got, file.Data) {
			t.Fatalf("payload differs: %s: %v", name, err)
		}
	}
	// This exercises seeking, HEAD and docs subtrees just like embed.FS.
	for _, tc := range []struct {
		method, target, byteRange string
		status                    int
		body                      string
	}{
		{"GET", "/assets/app.js", "", 200, "console.log('same UI');"},
		{"GET", "/assets/app.js", "bytes=0-6", 206, "console"},
		{"HEAD", "/assets/app.js", "", 200, ""},
		{"GET", "/doc/", "", 200, "public documentation"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.target, nil)
		if tc.byteRange != "" {
			req.Header.Set("Range", tc.byteRange)
		}
		http.FileServer(http.FS(tree)).ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Body.String() != tc.body {
			t.Fatalf("%s %s: status=%d body=%q", tc.method, tc.target, rec.Code, rec.Body.String())
		}
	}
}

func TestBundleChecksums(t *testing.T) {
	asset := BundleName("0.35.0")
	_, digest := fixtureBundle(t, "0.35.0")
	line := fmt.Sprintf("%x  %s\n", digest, asset)
	for _, body := range []string{line, strings.Replace(line, "  ", " *", 1), "\n" + line + strings.Repeat("a", 64) + "  other.tar.gz\n"} {
		got, err := checksumFor([]byte(body), asset)
		if err != nil || !bytes.Equal(got, digest) {
			t.Fatalf("valid checksum rejected: %v", err)
		}
	}
	for _, body := range []string{"", line + line, "bad  " + asset, strings.Repeat("a", 63) + "  " + asset, line + "bad " + asset, "a b " + asset} {
		if _, err := checksumFor([]byte(body), asset); err == nil {
			t.Fatal("accepted invalid/missing/duplicate checksum")
		}
	}
}

func TestBundleRejectsCorruptionAndWrongVersion(t *testing.T) {
	data, digest := fixtureBundle(t, "0.35.0")
	if _, err := openBundle(data, digest, "0.35.1"); err == nil {
		t.Fatal("wrong version accepted")
	}
	data[0] ^= 1
	if _, err := openBundle(data, digest, "0.35.0"); err == nil {
		t.Fatal("bad checksum accepted")
	}
	digest2 := sha256.Sum256([]byte("not zip"))
	if _, err := openBundle([]byte("not zip"), digest2[:], "0.35.0"); err == nil {
		t.Fatal("bad ZIP accepted")
	}
}

func TestBundleRejectsUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fs.FileMode
	}{
		{"../escape", 0644}, {"/absolute", 0644}, {`dir\escape`, 0644}, {"C:escape", 0644},
		{"bad\nname", 0644}, {"assets/app.js", 0644}, {"link", fs.ModeSymlink | 0777},
		{"pipe", fs.ModeNamedPipe | 0600}, {"doc", 0644}, {"link/", fs.ModeSymlink | 0777},
	} {
		t.Run(fmt.Sprintf("%q-%d", tc.name, tc.mode), func(t *testing.T) {
			data, _ := fixtureBundle(t, "0.35.0")
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			zw := zip.NewWriter(&out)
			if err := zw.SetComment(bundleComment("0.35.0")); err != nil {
				t.Fatal(err)
			}
			for _, file := range zr.File {
				if err := zw.Copy(file); err != nil {
					t.Fatal(err)
				}
			}
			header := &zip.FileHeader{Name: tc.name}
			header.SetMode(tc.mode)
			w, err := zw.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(tc.name, "/") {
				if _, err := io.WriteString(w, "unsafe"); err != nil {
					t.Fatal(err)
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(out.Bytes())
			if _, err := openBundle(out.Bytes(), digest[:], "0.35.0"); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
}

func TestPackRequiresFullUI(t *testing.T) {
	for _, name := range []string{"index.html", "doc/index.html", "devdoc/dev/index.html", "assets/app.js"} {
		tree := fixtureTree()
		delete(tree, name)
		if err := Pack(tree, "0.35.0", io.Discard); err == nil {
			t.Errorf("pack accepted missing %s", name)
		}
	}
	tree := fixtureTree()
	tree["link"] = &fstest.MapFile{Mode: fs.ModeSymlink | 0777, Data: []byte("/etc/passwd")}
	if err := Pack(tree, "0.35.0", io.Discard); err == nil {
		t.Fatal("pack accepted symlink")
	}
}
