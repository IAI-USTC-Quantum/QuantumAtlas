package main

import (
	"testing"
)

// TestLegacyOldStyleKeyRE_MatchesAndCaptures locks the contract for
// the migration scanner: which keys it picks up and which it leaves
// alone. The two extension forms (.pdf, .md, .zip) plus the four
// "not-eligible" categories below catch every layout we currently
// have in production.
func TestLegacyOldStyleKeyRE_MatchesAndCaptures(t *testing.T) {
	cases := []struct {
		key      string
		want     bool
		wantYYMM string // only checked when want=true
		wantStem string // only checked when want=true
	}{
		// Eligible — every (kind, ext) variant we ship.
		{"9508/9508027v2.pdf", true, "9508", "9508027v2"},
		{"0207/0207065v1.md", true, "0207", "0207065v1"},
		{"0612/0612345v3.zip", true, "0612", "0612345v3"},
		{"0001/0001091v4.pdf", true, "0001", "0001091v4"},

		// NOT eligible: new-style id (contains a dot in the stem).
		{"2501/2501.00010v1.pdf", false, "", ""},

		// NOT eligible: already migrated (3+ segments).
		{"9508/quant-ph/9508027v2.pdf", false, "", ""},

		// NOT eligible: hep-th canonical id (slash in category prefix
		// means 3+ segments — same handling).
		{"0207/hep-th/0207065v1.pdf", false, "", ""},

		// NOT eligible: stems we don't recognise (audit probes,
		// bootstrap test fixtures, etc.).
		{"_audit-probes/bootstrap-test.txt", false, "", ""},
		{"0001/some-debug-upload.pdf", false, "", ""},
		{"9508/9508027.pdf", false, "", ""}, // no version suffix → safer to skip
		{"9508/95080278v2.pdf", false, "", ""}, // 8-digit serial — not arxiv old-style
	}
	for _, c := range cases {
		m := legacyOldStyleKeyRE.FindStringSubmatch(c.key)
		got := m != nil
		if got != c.want {
			t.Errorf("legacyOldStyleKeyRE.match(%q) = %v, want %v (matches=%v)", c.key, got, c.want, m)
			continue
		}
		if !c.want {
			continue
		}
		if m[1] != c.wantYYMM {
			t.Errorf("legacyOldStyleKeyRE.match(%q) yymm = %q, want %q", c.key, m[1], c.wantYYMM)
		}
		if m[2] != c.wantStem {
			t.Errorf("legacyOldStyleKeyRE.match(%q) stem = %q, want %q", c.key, m[2], c.wantStem)
		}
	}
}
