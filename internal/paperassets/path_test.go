package paperassets

import "testing"

// TestValidateUploadID locks in the three accepted forms and a set of
// reject patterns. Critical for the mineru-claim endpoint: the catalog
// stores old-style IDs as bare "9508027v3" (subject prefix dropped at
// ingest time), so rejecting that form leaves every pre-2007 paper
// permanently unclaimable.
func TestValidateUploadID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string // expected canonical; "" if invalid
	}{
		// new style (post-2007)
		{"new-style 4-digit", "2401.0001v1", "2401.0001v1"},
		{"new-style 5-digit", "2501.12345v2", "2501.12345v2"},
		{"new-style 6-digit", "2401.123456v1", "2401.123456v1"},
		{"with arXiv: prefix", "arXiv:2401.0001v1", "2401.0001v1"},
		{"with arxiv: prefix lowercase", "arxiv: 2401.0001v1", "2401.0001v1"},

		// old-style canonical (subject prefix preserved)
		{"old-style canonical", "quant-ph/9508027v1", "quant-ph/9508027v1"},
		// A1 regex expansion: dotted subcategories now accept both
		// upper-case 2-letter (cs.AI) and lower-case hyphenated
		// (cond-mat.stat-mech, physics.atom-ph) forms.
		{"old-style with cond-mat subcat", "cond-mat.stat-mech/0001001v1", "cond-mat.stat-mech/0001001v1"},
		{"old-style with cs.AI subcat", "cs.AI/0001001v1", "cs.AI/0001001v1"},
		{"old-style with physics.atom-ph subcat", "physics.atom-ph/0001001v1", "physics.atom-ph/0001001v1"},
		{"old-style hep-th", "hep-th/0207001v3", "hep-th/0207001v3"},

		// old-style bare (catalog form — must accept)
		{"bare old v1", "9508027v1", "9508027v1"},
		{"bare old v3", "0207065v3", "0207065v3"},
		{"bare old v12", "9503006v12", "9503006v12"},

		// invalid
		{"no version suffix new", "2401.0001", ""},
		{"no version suffix old", "9508027", ""},
		{"no version subject only", "quant-ph/9508027", ""},
		{"bad length bare", "950802v1", ""},   // 6 digits not 7
		{"too long bare", "95080277v1", ""},   // 8 digits not 7
		{"non-digit bare", "950abc7v1", ""},
		{"random string", "foobar", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ValidateUploadID(tc.in)
			if tc.want == "" {
				if ok {
					t.Fatalf("ValidateUploadID(%q) = (%q, true), want (\"\", false)", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ValidateUploadID(%q) rejected, want (%q, true)", tc.in, tc.want)
			}
			if got != tc.want {
				t.Fatalf("ValidateUploadID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
