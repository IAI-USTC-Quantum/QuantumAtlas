package paperread

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func baseOptions() Options {
	return Options{PaperID: "qa_test", SourceID: "source-a", Revision: "revision-a", SourceSHA256: strings.Repeat("a", 64), BundleSHA256: strings.Repeat("b", 64)}
}
func fixture(t *testing.T, pages ...[]map[string]any) []byte {
	t.Helper()
	var ps []map[string]any
	for i, bs := range pages {
		ps = append(ps, map[string]any{"page_idx": i, "blocks": bs})
	}
	data, err := json.Marshal(map[string]any{"schema": "docvortex.middle", "schema_version": "2.0", "pages": ps})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func textBlock(index int, text string) map[string]any {
	return map[string]any{"type": "text", "index": index, "content": []map[string]any{{"type": "text", "content": text}}}
}
func readOK(t *testing.T, data []byte, opts Options) *Response {
	t.Helper()
	got, err := Read(data, opts)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestContinuationReconstructsCanonicalUnicodeMarkdown(t *testing.T) {
	data := fixture(t,
		[]map[string]any{textBlock(0, strings.Repeat("超长😀α beta\n", 40)), textBlock(4, "tail first page")},
		[]map[string]any{textBlock(3, "second page")})
	full := readOK(t, data, baseOptions())
	for _, limit := range []int{1, 2, 3, 19, 30, 67, DefaultLimit} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			opts := baseOptions()
			opts.Limit = limit
			var collected strings.Builder
			visited := map[string]bool{}
			for steps := 0; steps < 10000; steps++ {
				window := readOK(t, data, opts)
				if !utf8.ValidString(window.Content) || utf8.RuneCountInString(window.Content) > limit {
					t.Fatalf("invalid/oversized window %q", window.Content)
				}
				if window.Content == "" {
					t.Fatal("continuation failed to make progress")
				}
				collected.WriteString(window.Content)
				for _, rg := range window.ContentRanges {
					if rg.StartOffset < 0 || rg.EndOffset <= rg.StartOffset {
						t.Fatalf("bad offsets %+v", rg)
					}
					if rg.Block != 1 && rg.Block != 5 && !(rg.Page == 2 && rg.Block == 4) {
						t.Fatalf("fabricated identity %+v", rg)
					}
				}
				if window.NextRequest == nil {
					if window.Truncated {
						t.Fatal("truncated without cursor")
					}
					break
				}
				if !window.Truncated {
					t.Fatal("cursor without truncated")
				}
				if visited[window.NextRequest.Cursor] {
					t.Fatal("cursor cycle")
				}
				visited[window.NextRequest.Cursor] = true
				pin, err := PeekCursor(window.NextRequest.Cursor)
				if err != nil {
					t.Fatal(err)
				}
				if pin.Revision != opts.Revision {
					t.Fatal("revision pin lost")
				}
				opts.Cursor = window.NextRequest.Cursor
				opts.Limit = 0
				if steps == 9999 {
					t.Fatal("too many windows")
				}
			}
			if collected.String() != full.Content {
				t.Fatalf("continuation loss/duplication: got %d runes want %d", utf8.RuneCountInString(collected.String()), utf8.RuneCountInString(full.Content))
			}
		})
	}
}

func TestScopesAndNativeLocators(t *testing.T) {
	data := fixture(t, []map[string]any{textBlock(0, "one"), textBlock(4, strings.Repeat("二", 120))}, []map[string]any{textBlock(2, "last")})
	opts := baseOptions()
	opts.Page = 1
	opts.Block = 5
	opts.Limit = 29
	fullOpts := opts
	fullOpts.Limit = MaxLimit
	full := readOK(t, data, fullOpts)
	window := readOK(t, data, opts)
	var out strings.Builder
	for {
		out.WriteString(window.Content)
		for _, rg := range window.ContentRanges {
			if rg.Page != 1 || rg.Block != 5 || !strings.HasPrefix(rg.Start, "doc:aaaaaaa/tier:standard/page:1/block:5") {
				t.Fatalf("locator mismatch %+v", rg)
			}
		}
		if window.NextRequest == nil {
			break
		}
		opts = baseOptions()
		opts.Cursor = window.NextRequest.Cursor // no repeated scope or limit required
		window = readOK(t, data, opts)
		if window.RequestScope.Page != 1 || window.RequestScope.Block != 5 || window.RequestScope.Limit != 29 {
			t.Fatal("scope not carried by cursor")
		}
	}
	if out.String() != full.Content {
		t.Fatal("scoped continuation changed selection")
	}
	for _, selection := range [][2]int{{1, 3}, {3, 0}, {2, 1}} {
		opts := baseOptions()
		opts.Page = selection[0]
		opts.Block = selection[1]
		if _, err := Read(data, opts); !errors.Is(err, ErrNotFound) {
			t.Fatalf("selection %v: %v", selection, err)
		}
	}
}

func TestCursorRejectsWrongPinsAndMalformedOffsets(t *testing.T) {
	data := fixture(t, []map[string]any{textBlock(0, strings.Repeat("abcdef", 100))})
	opts := baseOptions()
	opts.Limit = 30
	w := readOK(t, data, opts)
	for _, change := range []func(*Options){
		func(o *Options) { o.PaperID = "qa_other" }, func(o *Options) { o.SourceID = "other" }, func(o *Options) { o.Revision = "new-revision" },
		func(o *Options) { o.SourceSHA256 = strings.Repeat("c", 64) }, func(o *Options) { o.BundleSHA256 = strings.Repeat("d", 64) },
		func(o *Options) { o.Tier = "basic" }, func(o *Options) { o.Page = 2 }, func(o *Options) { o.Block = 2 }, func(o *Options) { o.Limit = 31 },
	} {
		follow := baseOptions()
		follow.Cursor = w.NextRequest.Cursor
		change(&follow)
		if _, err := Read(data, follow); !errors.Is(err, ErrCursorMismatch) {
			t.Fatalf("mismatch accepted: %+v %v", follow, err)
		}
	}
	follow := baseOptions()
	follow.Cursor = w.NextRequest.Cursor
	changed := fixture(t, []map[string]any{textBlock(0, "changed bytes")})
	if _, err := Read(changed, follow); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("changed artifact accepted: %v", err)
	}
	for _, token := range []string{"not base64!!", strings.Repeat("x", 5000), base64.RawURLEncoding.EncodeToString([]byte(`{"v":1}`)), base64.RawURLEncoding.EncodeToString([]byte(`null`))} {
		if _, err := PeekCursor(token); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("bad peek %q: %v", token, err)
		}
	}
	for _, modify := range []func(*readCursor){func(c *readCursor) { c.Offset = -1 }, func(c *readCursor) { c.Offset = 999999 }, func(c *readCursor) { c.PositionBlock = 20 }, func(c *readCursor) { c.Renderer = "new-renderer" }, func(c *readCursor) { c.RenderSHA256 = strings.Repeat("0", 64) }} {
		c, err := decodeCursor(w.NextRequest.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		modify(&c)
		encoded, _ := json.Marshal(c)
		follow.Cursor = base64.RawURLEncoding.EncodeToString(encoded)
		if _, err := Read(data, follow); err == nil {
			t.Fatalf("tampered cursor accepted: %+v", c)
		}
	}
}

func TestLimitAndDuplicateValidation(t *testing.T) {
	data := fixture(t, []map[string]any{textBlock(0, "body")})
	for _, opts := range []Options{func() Options { o := baseOptions(); o.Limit = -1; return o }(), func() Options { o := baseOptions(); o.Limit = MaxLimit + 1; return o }(), func() Options { o := baseOptions(); o.Block = 1; return o }(), Options{}} {
		if _, err := Read(data, opts); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid opts accepted: %+v %v", opts, err)
		}
	}
	if got := readOK(t, data, baseOptions()); got.RequestScope.Limit != DefaultLimit {
		t.Fatal("default wrong")
	}
	dup := fixture(t, []map[string]any{textBlock(0, "x"), textBlock(0, "y")})
	if _, err := Read(dup, baseOptions()); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	empty := fixture(t, []map[string]any{})
	opts := baseOptions()
	opts.Page = 1
	opts.Limit = 1
	got := readOK(t, empty, opts)
	if len(got.ContentRanges) != 0 || got.Content != "" || got.Truncated {
		t.Fatalf("empty page fabricated blocks: %+v", got)
	}
}

func TestImageCallbackIsRenderPinned(t *testing.T) {
	data := fixture(t, []map[string]any{{"type": "image", "index": 0, "content": []map[string]any{{"type": "image_body", "content": "", "image_path": "images/nested/a (1).jpg"}}}})
	opts := baseOptions()
	opts.Limit = 25
	var members []string
	opts.ImageURL = func(s string) string {
		members = append(members, s)
		return "/api/papers/qa_test/parses/revision-a/files/" + s
	}
	window := readOK(t, data, opts)
	if len(members) != 1 || members[0] != "images/nested/a (1).jpg" {
		t.Fatalf("member path changed: %v", members)
	}
	fullOpts := opts
	fullOpts.Limit = MaxLimit
	full := readOK(t, data, fullOpts)
	if !strings.Contains(full.Content, "images/nested/a%20%281%29.jpg") {
		t.Fatalf("image reference lost: %s", full.Content)
	}
	opts.Cursor = window.NextRequest.Cursor
	opts.Limit = 0
	opts.ImageURL = func(s string) string { return "/different/" + s }
	if _, err := Read(data, opts); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("changed image resolver accepted: %v", err)
	}
}

// Opt-in genuine fixture test: no provider calls/inference or dependencies on a
// developer's absolute path in CI. Local verification sets this existing zip.
func TestGenuineMinerUArtifact(t *testing.T) {
	p := os.Getenv("QATLAS_REAL_MINERU_ZIP")
	if p == "" {
		t.Skip("QATLAS_REAL_MINERU_ZIP unset; genuine artifact test not run")
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var data []byte
	members := make(map[string]bool)
	for _, f := range zr.File {
		members[f.Name] = true
		if f.Name == "middle_json.json" {
			rd, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e = io.ReadAll(rd)
			rd.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(data) == 0 {
		t.Fatal("fixture has no Middle")
	}
	opts := baseOptions()
	opts.Limit = MaxLimit
	imageCount := 0
	opts.ImageURL = func(member string) string {
		if !members[member] {
			t.Errorf("render references absent fixture member %q", member)
		}
		imageCount++
		return "/api/papers/qa_test/parses/revision-a/files/" + member
	}
	full := readOK(t, data, opts)
	for _, needle := range []string{"# Fully dynamic data structure for LCE queries in compressed space", "$O ( w f _ { A } )$", "$$\n", "images/page_7_image_0.jpg"} {
		if !strings.Contains(full.Content, needle) {
			t.Errorf("genuine render missing %q", needle)
		}
	}
	if imageCount != 7 {
		t.Errorf("image count=%d, want 7 visual bodies", imageCount)
	}
	if len(full.Warnings) != 0 {
		t.Errorf("unexpected real fixture warnings: %v", full.Warnings)
	}
	opts.Limit = 5000
	var collected strings.Builder
	windows := 0
	for {
		window := readOK(t, data, opts)
		collected.WriteString(window.Content)
		windows++
		if window.NextRequest == nil {
			break
		}
		opts.Cursor = window.NextRequest.Cursor
		opts.Limit = 0
		if windows > 1000 {
			t.Fatal("cursor did not finish")
		}
	}
	if collected.String() != full.Content {
		t.Fatal("genuine fixture continuation mismatch")
	}
	t.Logf("genuine Middle rendered: %d ranges, %d Unicode characters, %d windows, nested image paths retained", len(full.ContentRanges), utf8.RuneCountInString(full.Content), windows)
}
