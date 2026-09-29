package mineru

import "testing"

// TestShortID pins the locator doc component: first 7 lowercase hex of
// the SOURCE PDF sha256 (plan §5.1). Fixed 7 chars for now — MinerU's
// widen-on-collision behaviour is a documented TODO, not implemented.
func TestShortID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"0123456789abcdef", "0123456"},
		{"0123456789ABCDEF", "0123456"},
		{"  0123456789abcdef  ", "0123456"},
		{"0123456", "0123456"},
		{"", ""},
		{"01234", ""},        // shorter than 7
		{"012345g", ""},      // non-hex garbage
		{"zz-not-a-sha", ""}, // not hex at all
	}
	for _, c := range cases {
		if got := ShortID(c.in); got != c.want {
			t.Errorf("ShortID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLocator pins the readable locator shape (plan §5.1 +
// golden-anchors public_numbering: "public page = page_idx+1, public
// block = block.index" — the Middle JSON index is already the 1-based
// public number, used verbatim).
func TestLocator(t *testing.T) {
	sha := "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	if got, want := Locator(sha, "standard", 4, 11), "doc:a1b2c3d/tier:standard/page:5/block:11"; got != want {
		t.Errorf("Locator = %q, want %q", got, want)
	}
	if got, want := Locator(sha, "lite", 0, 1), "doc:a1b2c3d/tier:lite/page:1/block:1"; got != want {
		t.Errorf("Locator(lite) = %q, want %q", got, want)
	}
	if got, want := Locator(sha, "", 9, 2), "doc:a1b2c3d/tier:standard/page:10/block:2"; got != want {
		t.Errorf("Locator(empty tier) = %q, want %q", got, want)
	}
	if got := Locator("not-hex", "standard", 0, 1); got != "" {
		t.Errorf("Locator(bad sha) = %q, want empty", got)
	}
}
