package paperread

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRichMiddleBlocks(t *testing.T) {
	blocks := []map[string]any{
		{"type": "doc_title", "index": 0, "level": 2, "anchor": "title\"x", "content": []map[string]any{{"type": "text", "content": "Paper title"}}},
		{"type": "text", "index": 3, "content": []map[string]any{
			{"type": "text", "content": "bold", "styles": []string{"bold"}},
			{"type": "text", "content": " and "},
			{"type": "text", "content": "2", "styles": []string{"superscript"}},
			{"type": "equation_inline", "content": "x^2"},
			{"type": "code_inline", "content": "a`b"},
			{"type": "hyperlink", "url": "https://example.test/a (b)", "content": []map[string]any{{"type": "text", "content": "reference"}}},
		}},
		{"type": "equation", "index": 5, "content": "E=mc^2"},
		{"type": "image", "index": 7, "content": []map[string]any{{"type": "image_body", "content": "", "image_path": "images/figure.jpg"}, {"type": "image_caption", "content": []map[string]any{{"type": "text", "content": "Figure 1"}}}}},
		{"type": "list", "index": 8, "sub_type": "ref_text", "content": []map[string]any{
			{"type": "ref_text", "content": []map[string]any{{"type": "text", "content": "Unnumbered ref"}}},
			{"type": "list", "content": []map[string]any{{"type": "text", "content": []map[string]any{{"type": "text", "content": "Nested ref"}}}}},
		}},
		{"type": "code", "index": 9, "sub_type": "code", "guess_lang": "go", "content": []map[string]any{{"type": "code_body", "content": "fmt.Println(`ok`)"}}},
		{"type": "table", "index": 10, "content": []map[string]any{{"type": "table_caption", "content": []map[string]any{{"type": "text", "content": "Table 1"}}}, {"type": "table_body", "content": "<table><tr><th>A</th><th>B</th></tr><tr><td>x</td><td><eq>n|m</eq></td></tr></table>"}}},
	}
	got := readOK(t, fixture(t, blocks), baseOptions())
	for _, needle := range []string{`<a id="title&#34;x"></a>` + "\n## Paper title", "**bold** and <sup>2</sup>$x^2$``a`b``", "[reference](https://example.test/a%20%28b%29)", "$$\nE=mc^2\n$$", "![](images/figure.jpg)\n\nFigure 1", "- Unnumbered ref\n    Nested ref", "```go\nfmt.Println(`ok`)\n```", "Table 1\n\n| A | B |\n| --- | --- |\n| x | $n\\|m$ |"} {
		if !strings.Contains(got.Content, needle) {
			t.Errorf("missing %q in:\n%s", needle, got.Content)
		}
	}
	if len(got.Warnings) != 0 {
		t.Errorf("unexpected warnings %v", got.Warnings)
	}
	for _, rg := range got.ContentRanges {
		if rg.Block == 2 || rg.Block == 3 || rg.Block == 5 || rg.Block == 7 {
			t.Errorf("fabricated array-position index %+v", rg)
		}
	}
}

func TestComplexTablesPreserveGeometryAndResolveEmbeddedImages(t *testing.T) {
	markup := `<table onclick="bad()"><tr><th colspan="2">Header</th></tr><tr><td rowspan="2">Left<img src="images/nested/a.jpg" onerror="bad()"></td><td><eq>x+y</eq></td></tr></table>`
	data := fixture(t, []map[string]any{{"type": "table", "index": 0, "content": []map[string]any{{"type": "table_body", "content": markup}}}})
	opts := baseOptions()
	opts.ImageURL = func(s string) string {
		if s != "images/nested/a.jpg" {
			t.Errorf("changed member %q", s)
		}
		return "/per-revision/" + s
	}
	got := readOK(t, data, opts)
	for _, needle := range []string{`colspan="2"`, `rowspan="2"`, `src="/per-revision/images/nested/a.jpg"`, "$x+y$"} {
		if !strings.Contains(got.Content, needle) {
			t.Errorf("complex table lost %q: %s", needle, got.Content)
		}
	}
	for _, needle := range []string{"onclick", "onerror", "bad()"} {
		if strings.Contains(got.Content, needle) {
			t.Errorf("unsafe HTML %q survived", needle)
		}
	}
	if len(got.Warnings) == 0 {
		t.Fatal("sanitization must be disclosed")
	}
}

func TestUnknownBlocksKeepBoundedInertContent(t *testing.T) {
	data := fixture(t, []map[string]any{{"type": "future_block", "index": 4, "content": "<script>alert(1)</script>" + strings.Repeat("X", 5000)}, textBlock(9, "still readable")})
	got := readOK(t, data, baseOptions())
	if !strings.Contains(got.Content, "```txt\n<script>") || !strings.Contains(got.Content, "[truncated]") || !strings.Contains(got.Content, "still readable") {
		t.Fatal("unknown content disappeared or following block lost")
	}
	if len(got.Warnings) != 1 {
		t.Errorf("warnings=%v", got.Warnings)
	}
	if len(got.Content) > 4500 {
		t.Errorf("fallback unbounded %d", len(got.Content))
	}
	if got.ContentRanges[0].Block != 5 || got.ContentRanges[1].Block != 10 {
		t.Fatal("unknown block identity changed")
	}
}

func TestUnsafeMarkupAndMembersAreNotActive(t *testing.T) {
	data := fixture(t, []map[string]any{
		{"type": "text", "index": 0, "content": []map[string]any{{"type": "text", "content": "<img src=x onerror=bad()> $literal _literal"}, {"type": "hyperlink", "url": "javascript:bad()", "content": []map[string]any{{"type": "text", "content": "safe label"}}}}},
		{"type": "image", "index": 1, "content": []map[string]any{{"type": "image_body", "image_path": "../private.txt", "content": ""}}},
		{"type": "chart", "index": 2, "content": []map[string]any{{"type": "chart_body", "content": "<div>safe<script>bad()</script><img src='javascript:bad()'></div>"}}},
	})
	called := false
	opts := baseOptions()
	opts.ImageURL = func(s string) string { called = true; return "/file/" + s }
	got := readOK(t, data, opts)
	if called {
		t.Fatal("unsafe member passed to URL resolver")
	}
	if !strings.Contains(got.Content, "&lt;img src=x onerror=bad()&gt;") || !strings.Contains(got.Content, "safe label") || !strings.Contains(got.Content, "\\$literal \\_literal") {
		t.Errorf("plain text escaping changed: %s", got.Content)
	}
	for _, bad := range []string{"[safe label](javascript:", "src=\"javascript:", "<script>", "../private.txt"} {
		if strings.Contains(got.Content, bad) {
			t.Errorf("active unsafe source survived %q", bad)
		}
	}
}

func TestSpanUnknownContentDoesNotCrash(t *testing.T) {
	r := &markdownRenderer{}
	raw := json.RawMessage(`{"type":"text","content":[{"type":"future_span","content":"kept"}]}`)
	if got := r.block(raw, 0); got != "kept" {
		t.Fatalf("unknown span lost: %q", got)
	}
	if len(r.warnings) != 1 {
		t.Fatalf("unknown span not disclosed: %v", r.warnings)
	}
}
