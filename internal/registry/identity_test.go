package registry

import "testing"

func TestNormalizeDOI(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"10.48550/arXiv.2401.12345", "10.48550/arxiv.2401.12345"},
		{"  HTTPS://DOI.ORG/10.1234/Foo.Bar  ", "10.1234/foo.bar"},
		{"doi:10.1234/ABC", "10.1234/abc"},
		{"", ""},
	} {
		if got := NormalizeDOI(tc.in); got != tc.want {
			t.Errorf("NormalizeDOI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeArxivID(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"2401.12345", "2401.12345"},
		{"2401.12345v2", "2401.12345"},
		{"arXiv:2401.12345v1", "2401.12345"},
		{"quant-ph/9508027v1", "quant-ph/9508027"},
		{" 2401.12345 ", "2401.12345"},
	} {
		if got := NormalizeArxivID(tc.in); got != tc.want {
			t.Errorf("NormalizeArxivID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestArxivVersionOf(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"2401.12345v2", 2},
		{"arXiv:2401.12345v10", 10},
		{"2401.12345", 0},
		{"quant-ph/9508027v1", 1},
		{"not-an-id", 0},
	} {
		if got := ArxivVersionOf(tc.in); got != tc.want {
			t.Errorf("ArxivVersionOf(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTitleHash(t *testing.T) {
	// Deterministic and insensitive to case / punctuation / extra spaces.
	a := TitleHash("Attention Is All You Need", []string{"Ashish Vaswani", "Noam Shazeer"}, 2017)
	b := TitleHash("attention is all you need!", []string{"A. Vaswani"}, 2017)
	if a != b {
		t.Errorf("TitleHash not stable across formatting:\n%s\n%s", a, b)
	}
	if len(a) != 40 {
		t.Errorf("TitleHash length = %d, want 40 (sha1 hex)", len(a))
	}
	// Different year → different hash.
	if c := TitleHash("Attention Is All You Need", []string{"Ashish Vaswani"}, 2016); c == a {
		t.Error("TitleHash ignores year")
	}
	// Different first-author lastname → different hash.
	if c := TitleHash("Attention Is All You Need", []string{"Noam Shazeer"}, 2017); c == a {
		t.Error("TitleHash ignores first-author lastname")
	}
	// Empty authors / year zero still produce a well-defined hash.
	if got := TitleHash("Some Title", nil, 0); len(got) != 40 {
		t.Errorf("TitleHash with no authors length = %d, want 40", len(got))
	}
}

func TestIdentityKeys(t *testing.T) {
	n := normalizeRef(PaperRef{
		ArxivID: "arXiv:2401.12345v2",
		DOI:     "https://doi.org/10.1234/FOO",
		Title:   "Some Title",
		Authors: []string{"Jane Doe"},
		Year:    2024,
	})
	keys := n.identityKeys()
	if len(keys) != 4 {
		t.Fatalf("identityKeys returned %d keys, want 4 (doi, arxiv, arxiv_version, title)", len(keys))
	}
	wantPrefix := []struct {
		key, kind string
	}{
		{"doi:10.1234/foo", KindDOI},
		{"arxiv:2401.12345", KindArxiv},
		{"arxiv_version:2401.12345v2", KindArxivVersion},
	}
	for i, w := range wantPrefix {
		if keys[i].key != w.key || keys[i].kind != w.kind {
			t.Errorf("keys[%d] = (%q, %q), want (%q, %q)", i, keys[i].key, keys[i].kind, w.key, w.kind)
		}
	}
	if keys[3].kind != KindTitle || keys[3].key != TitleKey(TitleHash("Some Title", []string{"Jane Doe"}, 2024)) {
		t.Errorf("keys[3] = (%q, %q), want title key", keys[3].key, keys[3].kind)
	}

	// No version → no arxiv_version key; no title → no title key.
	n = normalizeRef(PaperRef{ArxivID: "2401.12345"})
	keys = n.identityKeys()
	if len(keys) != 1 || keys[0].key != "arxiv:2401.12345" {
		t.Errorf("bare arxiv ref keys = %v, want only arxiv:2401.12345", keys)
	}
}
