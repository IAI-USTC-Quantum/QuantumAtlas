package routes

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperread"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

func TestContentNativeProfileRoutesAndCursorPin(t *testing.T) {
	native := []byte(`{"pdf_info":[{"page_idx":0,"page_size":[595,842],"para_blocks":[{"index":1,"type":"text","bbox":[10,10,500,100],"lines":[{"spans":[{"type":"text","content":"原生标准层文字 long bounded continuation text "}]}]}],"discarded_blocks":[]}],"_backend":"hybrid","_version_name":"3.4.4"}`)
	sourcePDF := []byte("%PDF-native source fixture")
	nativeReadingRouteFixture(t, sourcePDF, map[string][]byte{"layout.json": native, "full.md": []byte("original native Markdown\r\n"), "unrecognized/model.json": []byte(`{"kept":true}`)}, "layout.json", "full.md")
}

func TestContentGenuineStandardProducerRoutesPreserveAllOriginalFiles(t *testing.T) {
	archive, err := os.ReadFile("/home/timidly/qatlas-dev/realpaper/actual-standard-result.zip")
	if os.IsNotExist(err) {
		t.Skip("captured native standard ZIP is opt-in local fixture")
	}
	if err != nil {
		t.Fatal(err)
	}
	result, err := mineru.ExtractPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := os.ReadFile("/home/timidly/qatlas-dev/realpaper/1605.01488.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if result.MiddlePath != "layout.json" || len(result.Members) != 20 {
		t.Fatalf("native originals changed %q %d", result.MiddlePath, len(result.Members))
	}
	nativeReadingRouteFixture(t, pdf, result.Members, result.MiddlePath, result.MarkdownPath)
}

func nativeReadingRouteFixture(t *testing.T, pdf []byte, files map[string][]byte, middle, markdown string) {
	t.Helper()
	c, store, old := newReadingFixture(t)
	// Replace fixture identity before writing any native revision. The producer's
	// optional sanitized *_origin.pdf remains just a member, never this source.
	srcID := "src_native_read"
	frozen, err := paperbundle.New(store).FreezePDF(t.Context(), readTestPaper, srcID, pdf, "")
	if err != nil {
		t.Fatal(err)
	}
	c.source = registry.PaperSource{PaperID: readTestPaper, SourceID: srcID, Origin: "arxiv:v2", Sha256: frozen.SHA256, ObjstoreKey: frozen.Key, SizeBytes: frozen.SizeBytes}
	rev := "pr_native_read"
	m, err := paperbundle.New(store).WriteBundle(t.Context(), paperbundle.Input{PaperID: readTestPaper, SourceID: srcID, RevisionID: rev, SourcePDFSHA256: c.source.Sha256, Files: files, MiddlePath: middle, MarkdownPath: markdown})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(m)
	b := old
	b.RevisionID = rev
	b.SourceID = srcID
	b.Schema = mineru.NativeMiddleSchema
	b.SchemaVersion = mineru.NativeMiddleSchemaVersion
	b.SourcePDFSHA256 = c.source.Sha256
	b.ArtifactSha256 = paperbundle.SHA256(files[middle])
	b.MiddlePath = middle
	b.MarkdownPath = markdown
	b.ObjstoreKey = paperbundle.FileKey(readTestPaper, srcID, rev, middle)
	b.ManifestKey = paperbundle.ManifestKey(readTestPaper, srcID, rev)
	b.ManifestSHA256 = paperbundle.SHA256(manifest)
	c.bundles[rev] = b
	c.current = rev
	response, body := callReading(t, c, store, nil, "source_id="+srcID+"&limit=40")
	if response.Code != 200 || body["renderer"] != paperread.NativeRendererVersion || body["revision"] != rev {
		t.Fatalf("native read rejected/mislabeled %d %+v", response.Code, body)
	}
	if supportedBundleRenderer(b) != paperread.NativeRendererVersion {
		t.Fatal("native status renderer mislabeled")
	}
	if body["truncated"] == true {
		cursor := body["next_request"].(map[string]any)["cursor"].(string)
		c.current = old.RevisionID
		followed, continued := callReading(t, c, store, nil, "cursor="+url.QueryEscape(cursor))
		if followed.Code != 200 || continued["revision"] != rev || continued["renderer"] != paperread.NativeRendererVersion {
			t.Fatal("native cursor switched profile/revision")
		}
	}
	got, err := verifiedBundleMember(t.Context(), store, b, m, middle)
	if err != nil || !bytes.Equal(got, files[middle]) {
		t.Fatal("native original JSON rewritten", err)
	}
	for _, member := range m.Files {
		if !strings.HasPrefix(paperbundle.FileKey(b.PaperID, b.SourceID, b.RevisionID, member.Path), "content/") {
			t.Fatal("native legacy bucket write")
		}
	}
	// Native correctness depends on hashes of original layout bytes, not the
	// renderer's normalized in-memory block view.
	if _, err = verifiedContentBundle(t.Context(), store, b, c.source); err != nil {
		t.Fatal(err)
	}
}
