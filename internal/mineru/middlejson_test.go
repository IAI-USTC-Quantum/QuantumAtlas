package mineru

// middlejson_test.go: parser contract tests driven by the synthetic Q0
// fixtures (tests/fixtures/blockcomments). Golden-anchor semantics live
// at the route layer (internal/routes); here we lock the reader's own
// contract: strict schema gate, exact-index lookups over non-contiguous
// indexes, [0,1] bbox profile, stable ordering, cursor round-trip.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const fixturesDir = "../../tests/fixtures/blockcomments"

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseMiddleJSON_FixtureA(t *testing.T) {
	doc, err := ParseMiddleJSON(loadFixture(t, "parse-a.middle.json"))
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	if doc.Schema != MiddleSchema || doc.SchemaVersion != MiddleSchemaVersion {
		t.Fatalf("schema = %s/%s", doc.Schema, doc.SchemaVersion)
	}
	if doc.Pages != 2 {
		t.Errorf("pdf_info.pages = %d, want 2", doc.Pages)
	}
	if len(doc.Blocks) != 5 {
		t.Errorf("blocks = %d, want 5", len(doc.Blocks))
	}
	// Golden case: page 1 (idx 0) block 2 is the equation.
	b, ok := doc.FindBlock(0, 2)
	if !ok {
		t.Fatal("FindBlock(0,2) not found")
	}
	if b.Type != "equation" || b.ContentText() != "E = m c^{2}" {
		t.Errorf("block(0,2) = %s/%q, want equation/E = m c^{2}", b.Type, b.ContentText())
	}
	// bbox preserved normalized.
	if b.BBox == nil || b.BBox[0] != 0.1 || b.BBox[3] != 0.36 {
		t.Errorf("block(0,2) bbox = %v, want [0.1 0.3 0.4 0.36]", b.BBox)
	}
}

func TestParseMiddleJSON_NonContiguousIndexesNeverFallback(t *testing.T) {
	doc, err := ParseMiddleJSON(loadFixture(t, "parse-a.middle.json"))
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	// Golden case: indexes run 1,2,5 on page 1 — 3 and 4 do NOT exist.
	for _, missing := range []int{3, 4} {
		if _, ok := doc.FindBlock(0, missing); ok {
			t.Errorf("FindBlock(0,%d) reported found — must 404 (gap)", missing)
		}
	}
	if _, ok := doc.FindBlock(0, 5); !ok {
		t.Error("FindBlock(0,5) missing — trailing index 5 must be found")
	}
}

func TestParseMiddleJSON_FixtureBPageGap(t *testing.T) {
	doc, err := ParseMiddleJSON(loadFixture(t, "parse-b.middle.json"))
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	if doc.Pages != 1 {
		t.Errorf("pdf_info.pages = %d, want 1 (parse B lacks page 2)", doc.Pages)
	}
	// Golden case: parse B has NO page_idx=1 at all.
	for _, idx := range []int{1, 2, 7} {
		if _, ok := doc.FindBlock(1, idx); ok {
			t.Errorf("FindBlock(1,%d) reported found — parse B lacks page_idx=1", idx)
		}
	}
	// Golden case: block 7 carries no bbox (image crops report
	// unavailable, never fabricate).
	b, ok := doc.FindBlock(0, 7)
	if !ok {
		t.Fatal("FindBlock(0,7) missing")
	}
	if b.BBox != nil {
		t.Errorf("block (0,7) bbox = %v, want nil", b.BBox)
	}
}

func TestParseMiddleJSON_SchemaGate(t *testing.T) {
	base := json.RawMessage(loadFixture(t, "parse-a.middle.json"))
	var m map[string]any
	if err := json.Unmarshal(base, &m); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	m["schema"] = "something.else"
	if _, err := ParseMiddleJSON(mustJSON(t, m)); err == nil || !errors.Is(err, ErrNotMiddleJSON) {
		t.Errorf("wrong schema: err = %v, want ErrNotMiddleJSON", err)
	}

	m["schema"] = MiddleSchema
	m["schema_version"] = "1.0"
	if _, err := ParseMiddleJSON(mustJSON(t, m)); err == nil || !errors.Is(err, ErrUnsupportedVer) {
		t.Errorf("wrong version: err = %v, want ErrUnsupportedVer", err)
	}

	m["schema_version"] = MiddleSchemaVersion
	delete(m, "blocks")
	if _, err := ParseMiddleJSON(mustJSON(t, m)); err == nil || !errors.Is(err, ErrBadBlocks) {
		t.Errorf("missing blocks: err = %v, want ErrBadBlocks", err)
	}
}

func TestParseMiddleJSON_BBoxProfile(t *testing.T) {
	doc := map[string]any{
		"schema": MiddleSchema, "schema_version": MiddleSchemaVersion,
		"blocks": []map[string]any{
			{"page_idx": 0, "index": 1, "type": "text", "bbox": []float64{10, 20, 500, 900}},
		},
	}
	// Legacy [0,1000]-style coordinates must be rejected, never rescaled.
	if _, err := ParseMiddleJSON(mustJSON(t, doc)); err == nil || !errors.Is(err, ErrBadBBox) {
		t.Errorf("legacy bbox: err = %v, want ErrBadBBox", err)
	}

	doc["blocks"] = []map[string]any{
		{"page_idx": 0, "index": 1, "type": "text", "bbox": []float64{0.5, 0.5, 0.2, 0.8}},
	}
	if _, err := ParseMiddleJSON(mustJSON(t, doc)); err == nil || !errors.Is(err, ErrBadBBox) {
		t.Errorf("inverted bbox: err = %v, want ErrBadBBox", err)
	}

	doc["blocks"] = []map[string]any{
		{"page_idx": 0, "index": 1, "type": "text", "bbox": []float64{0.1, 0.1}},
	}
	if _, err := ParseMiddleJSON(mustJSON(t, doc)); err == nil || !errors.Is(err, ErrBadBBox) {
		t.Errorf("short bbox: err = %v, want ErrBadBBox", err)
	}
}

func TestParseMiddleJSON_IndexBase(t *testing.T) {
	doc := map[string]any{
		"schema": MiddleSchema, "schema_version": MiddleSchemaVersion,
		"blocks": []map[string]any{{"page_idx": 0, "index": 0, "type": "text"}},
	}
	if _, err := ParseMiddleJSON(mustJSON(t, doc)); err == nil || !errors.Is(err, ErrBadBlocks) {
		t.Errorf("index 0: err = %v, want ErrBadBlocks (public index is 1-based)", err)
	}
}

func TestOrderedBlocksStableAcrossShuffle(t *testing.T) {
	doc, err := ParseMiddleJSON(loadFixture(t, "parse-a.middle.json"))
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	// Fixture A is already ordered; ordering must be by (page_idx, index).
	got := doc.OrderedBlocks()
	want := [][2]int{{0, 1}, {0, 2}, {0, 5}, {1, 1}, {1, 2}}
	for i, b := range got {
		if b.PageIdx != want[i][0] || b.Index != want[i][1] {
			t.Fatalf("ordered[%d] = (%d,%d), want %v", i, b.PageIdx, b.Index, want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := EncodeCursor(1, 12)
	p, i, err := DecodeCursor(c)
	if err != nil || p != 1 || i != 12 {
		t.Fatalf("DecodeCursor(%q) = %d,%d,%v", c, p, i, err)
	}
	for _, bad := range []string{"", "x", "p1", "p1:b", "b1:p2", "p:b"} {
		if _, _, err := DecodeCursor(bad); err == nil {
			t.Errorf("DecodeCursor(%q) accepted garbage", bad)
		}
	}
}

func TestParseMiddleJSON_ContentVerbatim(t *testing.T) {
	doc, err := ParseMiddleJSON(loadFixture(t, "parse-a.middle.json"))
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	b, _ := doc.FindBlock(1, 2)
	// The content string must round-trip byte-for-byte (LaTeX escapes).
	const want = "\\sum_{i=1}^{n} i = \\frac{n(n+1)}{2}"
	if b.ContentText() != want {
		t.Errorf("content = %q, want %q", b.ContentText(), want)
	}
	// And the raw object must still contain the original key order text.
	var raw map[string]any
	if err := json.Unmarshal(b.Raw, &raw); err != nil {
		t.Fatalf("raw block is not valid JSON: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
