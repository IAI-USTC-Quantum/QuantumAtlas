package mineru

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// buildNewFormatZip assembles a new-format result zip in memory:
// optional per-paper dir prefix, middle_json.json, markdown.md, a
// metadata.json with the given tier (empty string → omit), and images.
func buildNewFormatZip(t *testing.T, dir, tier string, images map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mustWrite := func(name string, b []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	middle := map[string]any{
		"schema":         "docvortex.middle",
		"schema_version": "2.0",
		"pdf_info":       map[string]any{"pages": 1},
		"blocks": []map[string]any{
			{"page_idx": 0, "index": 1, "type": "text", "content": "HELLO NEW FORMAT"},
		},
	}
	mb, _ := json.Marshal(middle)
	mustWrite(dir+"middle_json.json", mb)
	mustWrite(dir+"markdown.md", []byte("# HELLO NEW FORMAT\n"))
	if tier != "" {
		mustWrite(dir+"metadata.json", []byte(fmt.Sprintf(`{"tier":%q}`, tier)))
	}
	for name, b := range images {
		mustWrite(dir+"images/"+name, b)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestHasMiddleJSON(t *testing.T) {
	newFmt := buildNewFormatZip(t, "1605.01488/", "lite", nil)
	if !HasMiddleJSON(newFmt) {
		t.Error("new-format zip not detected")
	}
	// Legacy zip (full.md only) must NOT be detected.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("full.md")
	_, _ = w.Write([]byte("legacy"))
	_ = zw.Close()
	if HasMiddleJSON(buf.Bytes()) {
		t.Error("legacy full.md zip falsely detected as new format")
	}
	if HasMiddleJSON([]byte("not a zip")) {
		t.Error("garbage detected as new format")
	}
}

func TestExtractNewFormat(t *testing.T) {
	images := map[string][]byte{"fig1.jpg": {0xFF, 0xD8, 0xFF}, "fig2.png": {0x89, 0x50}}
	raw := buildNewFormatZip(t, "1605.01488/", "lite", images)

	if !HasMiddleJSON(raw) {
		t.Fatal("detection failed")
	}
	res, err := ExtractNewFormat(raw)
	if err != nil {
		t.Fatalf("ExtractNewFormat: %v", err)
	}

	// Middle JSON parses as docvortex.middle and carries the block.
	doc, err := ParseMiddleJSON(res.MiddleJSON)
	if err != nil {
		t.Fatalf("ParseMiddleJSON on extracted middle: %v", err)
	}
	if b, ok := doc.FindBlock(0, 1); !ok || b.ContentText() != "HELLO NEW FORMAT" {
		t.Errorf("block not found / wrong content: %+v", b)
	}
	if string(res.Markdown) != "# HELLO NEW FORMAT\n" {
		t.Errorf("markdown = %q", res.Markdown)
	}
	if res.Tier != "lite" {
		t.Errorf("tier = %q, want lite", res.Tier)
	}
	if len(res.Images) != 2 || string(res.Images["images/fig1.jpg"]) != "\xff\xd8\xff" {
		t.Errorf("images = %v", res.Images)
	}
}

func TestExtractNewFormatNoTierMetadata(t *testing.T) {
	raw := buildNewFormatZip(t, "", "", nil) // no metadata.json, zip-root members
	res, err := ExtractNewFormat(raw)
	if err != nil {
		t.Fatalf("ExtractNewFormat: %v", err)
	}
	if res.Tier != "" {
		t.Errorf("tier = %q, want empty (caller defaults)", res.Tier)
	}
	if !HasMiddleJSON(raw) {
		t.Error("root-level middle_json.json not detected")
	}
}

func TestExtractNewFormatHybridFullMD(t *testing.T) {
	// Hybrid zip: middle_json.json + full.md (no markdown.md) — the
	// legacy markdown is still captured, never silently dropped.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range map[string][]byte{
		"out/middle_json.json": []byte(`{"schema":"docvortex.middle","schema_version":"2.0","blocks":[]}`),
		"out/full.md":          []byte("legacy markdown body"),
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write(b)
	}
	_ = zw.Close()
	res, err := ExtractNewFormat(buf.Bytes())
	if err != nil {
		t.Fatalf("ExtractNewFormat: %v", err)
	}
	if string(res.Markdown) != "legacy markdown body" {
		t.Errorf("hybrid markdown = %q", res.Markdown)
	}
}

func TestExtractNewFormatCorruptMetadataIgnored(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("middle_json.json")
	_, _ = w.Write([]byte(`{"schema":"docvortex.middle","schema_version":"2.0","blocks":[]}`))
	w2, _ := zw.Create("metadata.json")
	_, _ = w2.Write([]byte("{not json"))
	_ = zw.Close()
	res, err := ExtractNewFormat(buf.Bytes())
	if err != nil {
		t.Fatalf("ExtractNewFormat: %v", err)
	}
	if res.Tier != "" {
		t.Errorf("tier = %q, want empty on corrupt metadata", res.Tier)
	}
}
