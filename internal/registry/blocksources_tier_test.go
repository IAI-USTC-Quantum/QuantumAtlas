package registry

import "testing"

// TestNormalizeParseTier pins the tier-label contract (00009): the tier
// is a locator component (plan §5.1), never identity, so anything that
// does not fit the bounded alphabet collapses to the default instead of
// failing the ingest.
func TestNormalizeParseTier(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"standard", "standard"},
		{"LITE", "lite"},
		{"  Premium  ", "premium"},
		{"", "standard"},
		{"   ", "standard"},
		{"tier-1_v2", "tier-1_v2"},
		{"has space", "standard"},
		{"has/slash", "standard"},
		{"has:colon", "standard"},
		{"!", "standard"},
		{string(make([]byte, 33)), "standard"}, // > 32 chars → default
		{string(make([]byte, 32)), "standard"}, // 32 NULs invalid → default
	}
	for _, c := range cases {
		if got := NormalizeParseTier(c.in); got != c.want {
			t.Errorf("NormalizeParseTier(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if NormalizeParseTier("0123456789012345678901234567890a") != "0123456789012345678901234567890a" {
		t.Error("32-char valid tier was rejected")
	}
}
