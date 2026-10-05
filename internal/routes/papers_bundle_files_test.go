package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/pocketbase/pocketbase/core"
)

func TestContentBundleManifestExactPinnedBytesAndHashes(t *testing.T) {
	c, store, b := newReadingFixture(t)
	rec, body := readRouteRequest(t, "/api/papers/"+readTestPaper+"/parses/"+b.RevisionID+"/manifest", func(re *core.RequestEvent) error {
		return paperBundleManifestHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID)
	})
	if rec.Code != http.StatusOK || body["revision_id"] != b.RevisionID || paperbundle.SHA256(rec.Body.Bytes()) != b.ManifestSHA256 {
		t.Fatalf("manifest not exact %d %+v", rec.Code, body)
	}
	if rec.Header().Get("X-QAtlas-Sha256") != b.ManifestSHA256 || rec.Header().Get("X-QAtlas-Artifact-SHA256") != b.ManifestSHA256 || rec.Header().Get("X-QAtlas-PDF-SHA256") != c.source.Sha256 {
		t.Fatalf("manifest/source digest confusion: %v", rec.Header())
	}
	var m paperbundle.Manifest
	if json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatal("manifest JSON invalid")
	}
	if _, ok := m.Member("images/nested/figure (1).jpg"); !ok {
		t.Fatal("original nested member name flattened")
	}
}

func TestContentBundleRawMiddleDigestRemainsPinnedAfterCurrentFlip(t *testing.T) {
	c, store, old := newReadingFixture(t)
	newBundle := writeReadingBundle(t, c, store, "pr_currentother", "different current artifact")
	c.current = newBundle.RevisionID
	rec, _ := readRouteRequest(t, bundleMemberURL(readTestPaper, old.RevisionID, old.MiddlePath), func(re *core.RequestEvent) error {
		return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, old.RevisionID, old.MiddlePath)
	})
	if rec.Code != 200 || paperbundle.SHA256(rec.Body.Bytes()) != old.ArtifactSha256 || rec.Header().Get("X-QAtlas-Sha256") != old.ArtifactSha256 || rec.Header().Get("X-QAtlas-Artifact-SHA256") != old.ArtifactSha256 || rec.Header().Get("X-QAtlas-Source-SHA256") != c.source.Sha256 || rec.Header().Get("X-QAtlas-Parse-Revision") != old.RevisionID || c.readyCalls != 0 {
		t.Fatalf("raw Middle switched revision or confused PDF/member SHA: %d %v", rec.Code, rec.Header())
	}
	if old.ArtifactSha256 == c.source.Sha256 {
		t.Fatal("fixture does not distinguish raw Middle and source SHA")
	}
}

func TestContentBundleFileExactBytesSafeTypesAndRange(t *testing.T) {
	c, store, b := newReadingFixture(t)
	cases := []struct{ member, body, contentType, disposition string }{{"extra/unknown.json", `{"original":true}`, "application/json", "attachment"}, {"extra/active.svg", `<svg onload="bad()"></svg>`, "application/octet-stream", "attachment"}, {"images/nested/figure (1).jpg", "original image bytes", "image/jpeg", "inline"}}
	for _, tc := range cases {
		t.Run(tc.member, func(t *testing.T) {
			rec, _ := readRouteRequest(t, bundleMemberURL(readTestPaper, b.RevisionID, tc.member), func(re *core.RequestEvent) error {
				return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID, tc.member)
			})
			if rec.Code != 200 || rec.Body.String() != tc.body || rec.Header().Get("Content-Type") != tc.contentType || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), tc.disposition) {
				t.Fatalf("member delivery %d %q %v", rec.Code, rec.Body.String(), rec.Header())
			}
			hash := paperbundle.SHA256(rec.Body.Bytes())
			if rec.Header().Get("X-QAtlas-Sha256") != hash || rec.Header().Get("X-QAtlas-Artifact-SHA256") != hash || rec.Header().Get("X-QAtlas-PDF-SHA256") != c.source.Sha256 || rec.Header().Get("ETag") != `"`+hash+`"` {
				t.Fatal("binary artifact hash is not its exact bytes")
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("artifact sniffing not disabled")
			}
		})
	}
	rec, _ := readRouteRequest(t, "/api/papers/"+readTestPaper+"/parses/"+b.RevisionID+"/files/extra/unknown.json", func(re *core.RequestEvent) error {
		re.Request.Header.Set("Range", "bytes=0-4")
		return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID, "extra/unknown.json")
	})
	if rec.Code != 206 || rec.Body.String() != `{"ori` {
		t.Fatalf("range delivery %d %q", rec.Code, rec.Body.String())
	}
}

func TestContentBundleFilesGatePinsAndTraversal(t *testing.T) {
	c, store, b := newReadingFixture(t)
	before := c.getCalls
	for _, manifest := range []bool{false, true} {
		rec, _ := readRouteRequest(t, "/disabled", func(re *core.RequestEvent) error {
			if manifest {
				return paperBundleManifestHandler(re, &config.Config{}, store, c, readTestPaper, b.RevisionID)
			}
			return paperBundleFileHandler(re, &config.Config{}, store, c, readTestPaper, b.RevisionID, "../bad")
		})
		if rec.Code != 404 {
			t.Fatalf("disabled %d", rec.Code)
		}
	}
	if c.getCalls != before {
		t.Fatal("disabled route accessed catalog")
	}
	for _, member := range []string{"../source.pdf", "images/../../source.pdf", "/absolute", "images//a.jpg", "images/./a.jpg", "images\\a.jpg"} {
		rec, _ := readRouteRequest(t, "/test", func(re *core.RequestEvent) error {
			return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID, member)
		})
		if rec.Code != 400 {
			t.Fatalf("traversal accepted %q %d", member, rec.Code)
		}
	}
	rec, _ := readRouteRequest(t, "/test?source_id=other", func(re *core.RequestEvent) error {
		return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID, "extra/unknown.json")
	})
	if rec.Code != 409 {
		t.Fatal("source revision mismatch accepted")
	}
	rec, _ = readRouteRequest(t, "/test", func(re *core.RequestEvent) error {
		return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID, "unknown/not-manifest.json")
	})
	if rec.Code != 404 {
		t.Fatal("member absent from manifest served")
	}
	delete(c.bundles, b.RevisionID)
	rec, _ = readRouteRequest(t, "/test", func(re *core.RequestEvent) error {
		return paperBundleManifestHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID)
	})
	if rec.Code != 404 {
		t.Fatal("physical but unpublished legacy bundle served")
	}
}

func TestContentBundleFilesRejectAnyCorruptMemberOrManifestPin(t *testing.T) {
	for _, kind := range []string{"other-member", "source-pdf", "manifest-hash", "middle-path"} {
		t.Run(kind, func(t *testing.T) {
			c, store, b := newReadingFixture(t)
			switch kind {
			case "other-member":
				key := paperbundle.FileKey(readTestPaper, readTestSource, b.RevisionID, "images/nested/figure (1).jpg")
				data := []byte("corrupt image")
				if _, err := store.Put(t.Context(), key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
					t.Fatal(err)
				}
			case "source-pdf":
				data := []byte("changed PDF")
				if _, err := store.Put(t.Context(), c.source.ObjstoreKey, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
					t.Fatal(err)
				}
			case "manifest-hash":
				b.ManifestSHA256 = strings.Repeat("0", 64)
				c.bundles[b.RevisionID] = b
			case "middle-path":
				b.MiddlePath = "extra/unknown.json"
				c.bundles[b.RevisionID] = b
			}
			rec, _ := readRouteRequest(t, "/test", func(re *core.RequestEvent) error {
				return paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, readTestPaper, b.RevisionID, "extra/unknown.json")
			})
			if rec.Code != 422 || strings.Contains(rec.Body.String(), `{"original":true}`) {
				t.Fatalf("corrupt inventory served requested good member %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}
