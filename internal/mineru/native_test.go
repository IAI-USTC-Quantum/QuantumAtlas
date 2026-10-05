package mineru

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

const nativeFixture = `{
 "_backend":"hybrid","_version_name":"3.4.4","pdf_info":[{
 "page_idx":0,"page_size":[200,100],
 "preproc_blocks":[{"type":"text","index":2,"lines":[{"spans":[{"type":"text","content":"PREPROC not final"}]}]}],
 "para_blocks":[{"type":"title","index":2,"level":1,"bbox":[10,20,100,60],"lines":[{"spans":[{"type":"text","content":"Hello"}]},{"spans":[{"type":"text","content":"world"}]}]}],
 "discarded_blocks":[{"type":"page_number","index":0,"bbox":[100,90,110,95],"lines":[{"spans":[{"type":"text","content":"1"}]}]}]
 }]}`

func TestNativeMiddleExactOriginalIndicesAndInMemoryView(t *testing.T) {
	doc, err := ParseMiddleJSON([]byte(nativeFixture))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != NativeMiddleSchema || doc.SchemaVersion != NativeMiddleSchemaVersion || doc.ProducerVersion != "3.4.4" || doc.Pages != 1 || len(doc.Blocks) != 2 {
		t.Fatalf("dishonest/empty native profile: %+v", doc)
	}
	title, found := doc.FindBlock(0, 3)
	if !found || title.ContentText() != "Hello world" || title.NativePath != "/pdf_info/0/para_blocks/0" || string(title.NativeIndex) != "2" {
		t.Fatalf("native exact anchor/content: %+v", title)
	}
	if _, found := doc.FindBlock(0, 2); found {
		t.Fatal("missing native index replaced by array position")
	}
	if title.BBox[0] != 0.05 || title.BBox[1] != 0.2 || title.BBox[2] != 0.5 || title.BBox[3] != 0.6 {
		t.Fatal("native page-unit bbox not normalized using actual dimensions")
	}
	if !bytes.Contains(title.Raw, []byte(`"type":"title"`)) || !bytes.Contains(title.Raw, []byte(`"bbox":[10,20,100,60]`)) || bytes.Equal(title.Raw, title.RenderJSON()) {
		t.Fatal("original raw block rewritten")
	}
	if !bytes.Contains(title.RenderJSON(), []byte(`"type":"doc_title"`)) || bytes.Contains(title.RenderJSON(), []byte("PREPROC")) {
		t.Fatal("final para view not derived")
	}
	if _, found := doc.FindBlock(0, 1); !found {
		t.Fatal("discarded page auxiliary silently dropped")
	}
}
func TestNativeMiddleRejectsMissingDuplicateOrMixedAnchors(t *testing.T) {
	cases := []string{
		strings.Replace(nativeFixture, `"index":2,"level"`, `"level"`, 1),
		strings.Replace(nativeFixture, `"index":2,"level"`, `"index":2.5,"level"`, 1),
		strings.Replace(nativeFixture, `"index":0,"bbox"`, `"index":2,"bbox"`, 1),
		strings.Replace(nativeFixture, `"page_idx":0`, `"page_idx":5`, 1),
		strings.Replace(nativeFixture, `"_backend"`, `"schema":"content_list","_backend"`, 1),
		`{"pdf_info":[{"page_idx":0,"page_size":[100,100],"blocks":[]}]}`,
	}
	for _, raw := range cases {
		if _, err := ParseMiddleJSON([]byte(raw)); err == nil {
			t.Fatalf("invalid native shape accepted: %s", raw)
		}
	}
}
func TestNativeMiddleNeverFabricatesOrClampsBBox(t *testing.T) {
	for _, raw := range []string{strings.Replace(nativeFixture, `[200,100]`, `[0,100]`, 1), strings.Replace(nativeFixture, `[10,20,100,60]`, `[-10,20,300,60]`, 1), strings.Replace(nativeFixture, `[10,20,100,60]`, `[10,20,10,20]`, 1)} {
		doc, err := ParseMiddleJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := doc.FindBlock(0, 3)
		if b.BBox != nil || len(doc.NormalizationWarnings) == 0 {
			t.Fatal("invalid native box was fabricated/clamped")
		}
		if len(b.Raw) == 0 {
			t.Fatal("invalid original box discarded")
		}
	}
}
func TestNativeVisualChildrenKeepParentIdentityAndImagePath(t *testing.T) {
	raw := `{"pdf_info":[{"page_idx":0,"page_size":[100,100],"para_blocks":[{"type":"image","index":4,"bbox":[0,0,50,50],"blocks":[{"type":"image_body","index":4,"lines":[{"spans":[{"type":"image","image_path":"original.jpg"}]}]},{"type":"image_caption","index":5,"lines":[{"spans":[{"type":"text","content":"Caption"},{"type":"inline_equation","content":"x^2"}]}]}]}]}]}`
	doc, err := ParseMiddleJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 1 {
		t.Fatal("visual children became fabricated top-level anchors")
	}
	b, _ := doc.FindBlock(0, 5)
	if !bytes.Contains(b.Raw, []byte(`"image_path":"original.jpg"`)) || !bytes.Contains(b.RenderJSON(), []byte(`"image_path":"images/original.jpg"`)) || !bytes.Contains(b.RenderJSON(), []byte(`"type":"equation_inline"`)) {
		t.Fatal("native visual semantics lost/rewritten original")
	}
	if _, found := doc.FindBlock(0, 6); found {
		t.Fatal("caption child masqueraded as public top-level block")
	}
}
func nativeArchive(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, raw := range map[string]string{"layout.json": nativeFixture, "full.md": "# ORIGINAL MD", "uuid_model.json": "{\"model\":true}", "images/original.jpg": "RAWIMAGE"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(raw))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestNativeSourcePublicationHonestProfileAndUntouchedMembers(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", fakePDFBytes)
	stub := newMinerUStub(t)
	stub.zipBody = nativeArchive(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	c.Ensure(t.Context(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateDone, time.Second) {
		j, _ := c.Lookup("2401.12345v1")
		t.Fatalf("native returned package failed: %+v", j)
	}
	j, _ := c.Lookup("2401.12345v1")
	cat := c.sources.(*fakeSourceCatalog)
	b := cat.bundles[j.SourceID]
	if b.Schema != NativeMiddleSchema || b.SchemaVersion != NativeMiddleSchemaVersion || b.MiddlePath != "layout.json" || b.MarkdownPath != "full.md" {
		t.Fatalf("native mislabeled/renamed: %+v", b)
	}
	m, err := paperbundle.New(store).VerifyBundle(t.Context(), j.PaperID, j.SourceID, j.RevisionID)
	if err != nil || len(m.Files) != 4 {
		t.Fatal("native complete package not published: ", err)
	}
	original, ok := store.get(paperbundle.FileKey(j.PaperID, j.SourceID, j.RevisionID, "layout.json"))
	if !ok || !bytes.Equal(original, []byte(nativeFixture)) {
		t.Fatal("native artifact reserialized")
	}
}
func TestRealNativeOriginMemberNeverReplacesFrozenInput(t *testing.T) {
	zipPath, pdfPath := os.Getenv("QATLAS_NATIVE_MINERU_ZIP"), os.Getenv("QATLAS_NATIVE_SOURCE_PDF")
	if zipPath == "" || pdfPath == "" {
		t.Skip("genuine native archive/input fixture environment unset")
	}
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExtractPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	originName := ""
	for name := range result.Members {
		if strings.HasSuffix(name, "_origin.pdf") {
			originName = name
		}
	}
	if originName == "" || paperbundle.SHA256(result.Members[originName]) == paperbundle.SHA256(input) {
		t.Fatal("fixture must expose genuine sanitized origin vs frozen input distinction")
	}
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", input)
	stub := newMinerUStub(t)
	stub.zipBody = archive
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	c.Ensure(t.Context(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateDone, 3*time.Second) {
		j, _ := c.Lookup("2401.12345v1")
		t.Fatalf("genuine native package publication: %+v", j)
	}
	j, _ := c.Lookup("2401.12345v1")
	frozen, ok := store.get(paperbundle.PDFKey(j.PaperID, j.SourceID))
	if !ok || !bytes.Equal(frozen, input) || j.SourcePDFSHA256 != paperbundle.SHA256(input) {
		t.Fatal("provider sanitized origin substituted exact frozen source")
	}
	m, err := paperbundle.New(store).VerifyBundle(t.Context(), j.PaperID, j.SourceID, j.RevisionID)
	if err != nil || len(m.Files) != 20 || m.SourcePDFSHA256 != paperbundle.SHA256(input) {
		t.Fatal("genuine native manifest/input identity mismatch", err)
	}
	for name, original := range result.Members {
		stored, ok := store.get(paperbundle.FileKey(j.PaperID, j.SourceID, j.RevisionID, name))
		if !ok || !bytes.Equal(stored, original) {
			t.Fatalf("genuine original member changed: %s", name)
		}
	}
}

func TestRealNativeStandardArchivePreservesAllMembers(t *testing.T) {
	p := os.Getenv("QATLAS_NATIVE_MINERU_ZIP")
	if p == "" {
		t.Skip("QATLAS_NATIVE_MINERU_ZIP unset; genuine native standard fixture pending")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ExtractPackage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.MiddlePath != "layout.json" || r.MarkdownPath != "full.md" || len(r.Members) != 20 {
		t.Fatalf("genuine archive names/inventory: %s %s %d", r.MiddlePath, r.MarkdownPath, len(r.Members))
	}
	doc, err := ParseMiddleJSON(r.MiddleJSON)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != NativeMiddleSchema || doc.ProducerVersion != "3.4.4" || doc.Pages != 17 || len(doc.Blocks) != 241 {
		t.Fatalf("genuine native profile: %+v", doc)
	}
	title, found := doc.FindBlock(0, 2)
	if !found || !strings.Contains(title.ContentText(), "Fully dynamic data structure") {
		t.Fatal("genuine title original native index not preserved")
	}
	if _, found := doc.FindBlock(7, 2); found {
		t.Fatal("native visual-caption gap fabricated")
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range archive.File {
		if member.FileInfo().IsDir() {
			continue
		}
		reader, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		original, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || !bytes.Equal(r.Members[member.Name], original) {
			t.Fatalf("genuine %s rewritten/dropped", member.Name)
		}
	}
	var native map[string]any
	if json.Unmarshal(r.MiddleJSON, &native) != nil || native["schema"] != nil {
		t.Fatal("native artifact mislabeled with synthetic schema")
	}
	t.Logf("native standard producer=%s pages=%d topblocks=%d originalmembers=%d warnings=%d", doc.ProducerVersion, doc.Pages, len(doc.Blocks), len(r.Members), len(doc.NormalizationWarnings))
}
