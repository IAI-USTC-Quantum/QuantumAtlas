package paperindex

import (
	"testing"
)

func TestParseAssetKey(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		ok      bool
		kind    AssetKind
		arxivID string
		yymm    string
	}{
		{"pdf normal", "pdf/2401/2401.0001v1.pdf", true, KindPDF, "2401.0001v1", "2401"},
		{"markdown normal", "markdown/0704/0704.2988v1.md", true, KindMarkdown, "0704.2988v1", "0704"},
		{"json normal", "json/2510/2510.12345v2.json", true, KindJSON, "2510.12345v2", "2510"},
		{"images normal", "images/2401/2401.0001v1/page-001.png", true, KindImages, "2401.0001v1", "2401"},
		{"images deep path", "images/2401/2401.0001v1/sub/dir/x.png", true, KindImages, "2401.0001v1", "2401"},

		{"index parquet rejected", "index/papers.parquet", false, "", "", ""},
		{"empty key", "", false, "", "", ""},
		{"too few parts", "pdf/foo", false, "", "", ""},
		{"pdf with extra subdir rejected", "pdf/2401/sub/foo.pdf", false, "", "", ""},
		{"unknown kind", "audit/2401/foo.json", false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, arxiv, yymm, ok := ParseAssetKey(tc.key)
			if ok != tc.ok {
				t.Errorf("ok=%v want %v (key=%q)", ok, tc.ok, tc.key)
				return
			}
			if !ok {
				return
			}
			if kind != tc.kind || arxiv != tc.arxivID || yymm != tc.yymm {
				t.Errorf("got (kind=%q arxiv=%q yymm=%q) want (%q %q %q)",
					kind, arxiv, yymm, tc.kind, tc.arxivID, tc.yymm)
			}
		})
	}
}
