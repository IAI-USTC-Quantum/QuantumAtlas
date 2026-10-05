package paperread

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
)

func nativeReadingFixture(t *testing.T, blocks []map[string]any, extraPages bool) []byte {
	t.Helper()
	pages := []any{map[string]any{"page_idx": 0, "page_size": []int{595, 842}, "para_blocks": blocks, "discarded_blocks": []any{}}}
	if extraPages {
		pages = append(pages, map[string]any{"page_idx": 1, "page_size": []int{595, 842}, "para_blocks": []any{}, "discarded_blocks": []any{}})
	}
	data, err := json.Marshal(map[string]any{"pdf_info": pages, "_backend": "hybrid", "_effort": "medium", "_ocr_enable": false, "_version_name": "3.4.4"})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func nativeSpanBlock(index int, kind, text string) map[string]any {
	return map[string]any{"index": index, "type": kind, "bbox": []int{50, 50, 500, 120}, "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "text", "content": text}}}}}
}

func TestNativeReadingProfileAndRichViewPreserveOriginalBytes(t *testing.T) {
	data := nativeReadingFixture(t, []map[string]any{
		{"index": 1, "type": "title", "level": 1, "bbox": []int{50, 50, 500, 120}, "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "text", "content": "Native title"}}}}},
		{"index": 3, "type": "text", "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "text", "content": "Mixed text"}, map[string]any{"type": "inline_equation", "content": "x^2"}}}}},
		{"index": 7, "type": "interline_equation", "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "interline_equation", "content": "E=mc^2", "image_path": "formula.jpg"}}}}},
		{"index": 9, "type": "image", "blocks": []any{map[string]any{"index": 9, "type": "image_body", "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "image", "image_path": "figure.jpg"}}}}}, map[string]any{"index": 10, "type": "image_caption", "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "text", "content": "Caption keeps semantics"}}}}}}},
		{"index": 13, "type": "table", "blocks": []any{map[string]any{"index": 13, "type": "table_body", "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "table", "html": "<table><tr><th>A</th><th>B</th></tr><tr><td>x</td><td><eq>n+m</eq></td></tr></table>"}}}}}}},
	}, true)
	before := append([]byte(nil), data...)
	var members []string
	opts := baseOptions()
	opts.ImageURL = func(member string) string { members = append(members, member); return "/revision-files/" + member }
	response := readOK(t, data, opts)
	if response.Renderer != NativeRendererVersion || response.ArtifactSHA256 != digest(before) || !bytes.Equal(before, data) {
		t.Fatal("native profile relabeled or original bytes/hash changed")
	}
	for _, needle := range []string{"# Native title", "Mixed text", "$x^2$", "$$\nE=mc^2\n$$", "![](/revision-files/images/figure.jpg)", "Caption keeps semantics", "| A | B |", "$n+m$"} {
		if !strings.Contains(response.Content, needle) {
			t.Errorf("native rich reading missing %q: %s", needle, response.Content)
		}
	}
	if len(members) != 1 || members[0] != "images/figure.jpg" {
		t.Fatalf("native image grammar not mapped: %v", members)
	}
	if len(response.Warnings) != 0 {
		t.Fatalf("native content fell into unsupported renderer: %v", response.Warnings)
	}
	doc, err := mineru.ParseMiddleJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != mineru.NativeMiddleSchema || doc.ProducerVersion != "3.4.4" {
		t.Fatal("native producer/profile lied")
	}
	if len(doc.Blocks) != 5 || !strings.Contains(string(doc.Blocks[0].Raw), `"lines"`) || strings.Contains(string(doc.Blocks[0].Raw), `"doc_title"`) {
		t.Fatal("original node rewritten or child anchors flattened")
	}
	wantIndices := []int{2, 4, 8, 10, 14}
	for i, rg := range response.ContentRanges {
		if rg.Page != 1 || rg.Block != wantIndices[i] {
			t.Fatalf("native ordinal invented %+v", rg)
		}
	}
	opts.Page = 2
	opts.ImageURL = nil
	if got := readOK(t, data, opts); got.Content != "" || len(got.ContentRanges) != 0 {
		t.Fatal("empty actual native page has fake block")
	}
}

func TestNativeRendererPinAndDocVortexCursorRemainCompatible(t *testing.T) {
	native := nativeReadingFixture(t, []map[string]any{nativeSpanBlock(5, "text", strings.Repeat("Native Unicode😀 ", 30))}, false)
	modern := fixture(t, []map[string]any{textBlock(0, strings.Repeat("Old released render😀 ", 30))})
	for _, tc := range []struct {
		name     string
		data     []byte
		renderer string
	}{{"native", native, NativeRendererVersion}, {"released-docvortex", modern, RendererVersion}} {
		t.Run(tc.name, func(t *testing.T) {
			opts := baseOptions()
			opts.Limit = 29
			first := readOK(t, tc.data, opts)
			if first.Renderer != tc.renderer || first.NextRequest == nil {
				t.Fatal("renderer pin missing")
			}
			pin, err := PeekCursor(first.NextRequest.Cursor)
			if err != nil || pin.Renderer != tc.renderer {
				t.Fatalf("opaque renderer pin %v %+v", err, pin)
			}
			follow := baseOptions()
			follow.Cursor = first.NextRequest.Cursor
			if got := readOK(t, tc.data, follow); got.Renderer != tc.renderer {
				t.Fatal("existing profile cursor switched renderer")
			}
			c, err := decodeCursor(first.NextRequest.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			if tc.renderer == RendererVersion {
				c.Renderer = NativeRendererVersion
			} else {
				c.Renderer = RendererVersion
			}
			wrong, _ := json.Marshal(c)
			follow.Cursor = base64.RawURLEncoding.EncodeToString(wrong)
			if _, err := Read(tc.data, follow); !errors.Is(err, ErrCursorMismatch) {
				t.Fatalf("cross-profile cursor accepted: %v", err)
			}
			full := readOK(t, tc.data, baseOptions())
			var collected strings.Builder
			opts.Cursor = ""
			for windows := 0; windows < 1000; windows++ {
				w := readOK(t, tc.data, opts)
				if utf8.RuneCountInString(w.Content) > 29 {
					t.Fatal("Unicode budget exceeded")
				}
				collected.WriteString(w.Content)
				if w.NextRequest == nil {
					break
				}
				opts.Cursor = w.NextRequest.Cursor
				opts.Limit = 0
				if windows == 999 {
					t.Fatal("cursor loop")
				}
			}
			if collected.String() != full.Content {
				t.Fatal("native/released continuation lost or duplicated text")
			}
		})
	}
	if !IsSupportedRenderer(RendererVersion) || !IsSupportedRenderer(NativeRendererVersion) || IsSupportedRenderer("qatlas-future-unknown") {
		t.Fatal("supported renderer gate is not exact")
	}
	if renderer, ok := RendererForProfile(mineru.NativeMiddleSchema, mineru.NativeMiddleSchemaVersion); !ok || renderer != NativeRendererVersion {
		t.Fatal("native profile renderer map wrong")
	}
	if _, ok := RendererForProfile(mineru.NativeMiddleSchema, "other-version"); ok {
		t.Fatal("future native profile implicitly accepted")
	}
}

func TestNativeMissingIndexDoesNotInventOrdinal(t *testing.T) {
	data := nativeReadingFixture(t, []map[string]any{{"type": "text", "lines": []any{map[string]any{"spans": []any{map[string]any{"type": "text", "content": "no stable producer anchor"}}}}}}, false)
	if _, err := Read(data, baseOptions()); !errors.Is(err, mineru.ErrBadBlocks) {
		t.Fatalf("ambiguous native block got invented public ordinal: %v", err)
	}
}

// This is the retrieved REAL V1 standard/hybrid artifact, not the prior advanced
// DocVortex fixture. Reading an existing ZIP and Middle is not a vendor parse.
func TestGenuineNativeStandardReading(t *testing.T) {
	zipPath := os.Getenv("QATLAS_REAL_NATIVE_MINERU_ZIP")
	if zipPath == "" {
		t.Skip("QATLAS_REAL_NATIVE_MINERU_ZIP unset; genuine native standard artifact not run")
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	members := make(map[string]bool)
	var data []byte
	for _, entry := range zr.File {
		members[entry.Name] = true
		if entry.Name == "layout.json" {
			r, e := entry.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e = io.ReadAll(r)
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(data) == 0 {
		t.Fatal("genuine native layout.json missing")
	}
	originalHash := digest(data)
	doc, err := mineru.ParseMiddleJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != mineru.NativeMiddleSchema || doc.SchemaVersion != mineru.NativeMiddleSchemaVersion || doc.ProducerVersion != "3.4.4" || doc.Pages != 17 || len(doc.Blocks) != 241 {
		t.Fatalf("genuine native identity %s/%s producer=%s pages=%d blocks=%d", doc.Schema, doc.SchemaVersion, doc.ProducerVersion, doc.Pages, len(doc.Blocks))
	}
	opts := baseOptions()
	opts.Limit = MaxLimit
	imageCalls := 0
	opts.ImageURL = func(member string) string {
		if !members[member] {
			t.Errorf("image reference absent from original inventory %q", member)
		}
		imageCalls++
		return "/files/" + member
	}
	full := readOK(t, data, opts)
	if full.Truncated {
		t.Fatal("genuine reference window exceeds test max; cannot compare to full reference")
	}
	if full.ArtifactSHA256 != originalHash || full.Renderer != NativeRendererVersion {
		t.Fatal("genuine native artifact/profile falsely relabeled")
	}
	for _, needle := range []string{"# Fully dynamic data structure for LCE queries in compressed space", "$O ( w f _ { A } )$", "$$\n", "![](/files/images/86612c883477493f0470e7f9f61b83c234e44b1c32ea5c2f257506e8f13e47e7.jpg)", "Figure", "References"} {
		if !strings.Contains(full.Content, needle) {
			t.Errorf("genuine native rendering missing %q", needle)
		}
	}
	if imageCalls != 7 {
		t.Errorf("image body count=%d want7", imageCalls)
	}
	if len(full.Warnings) != 0 {
		t.Errorf("genuine native renderer unsupported or invalid: %v", full.Warnings)
	}
	for _, rg := range full.ContentRanges {
		if _, found := doc.FindBlock(rg.Page-1, rg.Block); !found {
			t.Fatalf("native cursor has no producer anchor: %+v", rg)
		}
	}
	// Parent image native index 0 is public block1; child caption index1 is not
	// a top-level anchor. Page8 native para indices leave public block2 absent.
	opts.Page = 8
	opts.Block = 2
	if _, err := Read(data, opts); !errors.Is(err, ErrNotFound) {
		t.Fatalf("child image caption was fabricated as public top-level block: %v", err)
	}
	opts.Page = 1
	opts.Block = 2
	if title := readOK(t, data, opts); !strings.Contains(title.Content, "# Fully dynamic") {
		t.Fatal("original native title index1 not mapped exact public2")
	}
	opts.Page = 0
	opts.Block = 0
	opts.Limit = 5000
	var collected strings.Builder
	windows := 0
	for {
		w := readOK(t, data, opts)
		collected.WriteString(w.Content)
		windows++
		if w.NextRequest == nil {
			break
		}
		opts.Cursor = w.NextRequest.Cursor
		opts.Limit = 0
		if windows > 1000 {
			t.Fatal("genuine native cursor loop")
		}
	}
	if collected.String() != full.Content {
		t.Fatal("genuine native windows lost/duplicated content")
	}
	if digest(data) != originalHash {
		t.Fatal("genuine native input mutated")
	}
	t.Logf("genuine standard native: producer=%s pages=%d ranges=%d chars=%d windows=%d originalSHA=%s", doc.ProducerVersion, doc.Pages, len(full.ContentRanges), utf8.RuneCountInString(full.Content), windows, originalHash)
}
