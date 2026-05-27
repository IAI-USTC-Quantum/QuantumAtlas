package pat

import (
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestGenerate verifies the basic invariants of a freshly minted PAT:
// plaintext shape, prefix correctness, and that the hash actually
// verifies via bcrypt. Anything else that breaks here would be a
// silent security regression — keep this test painfully literal.
func TestGenerate(t *testing.T) {
	plaintext, prefix, hash, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Plaintext shape: "qat_" + 24 random chars
	wantLen := len(TokenPrefix) + secretBodyLen
	if got := len(plaintext); got != wantLen {
		t.Errorf("plaintext length = %d, want %d", got, wantLen)
	}
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		t.Errorf("plaintext %q missing prefix %q", plaintext, TokenPrefix)
	}

	// Prefix shape: first PrefixDisplayLen chars of plaintext.
	if len(prefix) != PrefixDisplayLen {
		t.Errorf("prefix length = %d, want %d", len(prefix), PrefixDisplayLen)
	}
	if !strings.HasPrefix(prefix, TokenPrefix) {
		t.Errorf("prefix %q must itself begin with %q", prefix, TokenPrefix)
	}
	if !strings.HasPrefix(plaintext, prefix) {
		t.Errorf("plaintext %q must extend prefix %q", plaintext, prefix)
	}

	// Hash must actually verify against the original plaintext.
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)); err != nil {
		t.Errorf("bcrypt verify: %v", err)
	}
	// And must NOT verify against a different plaintext.
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext+"X")); err == nil {
		t.Error("bcrypt unexpectedly accepted wrong plaintext")
	}
}

// TestGenerate_Uniqueness ensures Generate doesn't have a deterministic
// failure mode. 50 iterations is overkill for catching anything but
// a hardcoded constant — the actual collision space is 62^24 ≈ 10^43.
func TestGenerate_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 50)
	for i := 0; i < 50; i++ {
		plaintext, _, _, err := Generate()
		if err != nil {
			t.Fatalf("Generate iter %d: %v", i, err)
		}
		if _, dup := seen[plaintext]; dup {
			t.Fatalf("Generate produced duplicate plaintext at iter %d: %s", i, plaintext)
		}
		seen[plaintext] = struct{}{}
	}
}

// TestLooks pins down the cheap shape check that authGuard relies on.
// False negatives here are catastrophic (legitimate PAT rejected as
// non-PAT and never reaching Lookup) so the table covers every
// surprising input we could think of.
func TestLooks(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"empty string", "", false},
		{"prefix only", TokenPrefix, false},
		{"single body char", TokenPrefix + "x", true},
		{"full plaintext", TokenPrefix + strings.Repeat("a", secretBodyLen), true},
		{"missing prefix", "abc_xyz0123456", false},
		{"wrong case prefix", "QAT_xyz0123456", false},
		{"pocketbase JWT shape", "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6IiJ9.xxx", false},
		{"random garbage", "hello world", false},
		{"prefix in middle", "padding" + TokenPrefix + "body", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Looks(tc.token); got != tc.want {
				t.Errorf("Looks(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

// TestPrefixOf checks that PrefixOf is consistent between Generate and
// Lookup. Drift here would silently break Lookup's prefix-indexed
// optimization (lookups would never find a match).
func TestPrefixOf(t *testing.T) {
	plaintext, prefix, _, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := PrefixOf(plaintext); got != prefix {
		t.Errorf("PrefixOf(plaintext) = %q, but Generate stored %q", got, prefix)
	}

	// Defensive: a too-short input returns itself, not a slice panic.
	short := TokenPrefix
	if got := PrefixOf(short); got != short {
		t.Errorf("PrefixOf(%q) = %q, want %q", short, got, short)
	}
}

// TestRandomBase62_Entropy verifies length contract + character set.
// Real "entropy" can't be tested in a unit test, but we can at least
// catch a typo that swaps base62 for hex or some other smaller set.
func TestRandomBase62_Entropy(t *testing.T) {
	const n = 64
	out, err := randomBase62(n)
	if err != nil {
		t.Fatalf("randomBase62: %v", err)
	}
	if len(out) != n {
		t.Errorf("length = %d, want %d", len(out), n)
	}
	alphabetRE := regexp.MustCompile(`^[0-9A-Za-z]+$`)
	if !alphabetRE.MatchString(out) {
		t.Errorf("output %q contains chars outside base62 alphabet", out)
	}

	// Sanity: zero and negative lengths must error, not panic.
	if _, err := randomBase62(0); err == nil {
		t.Error("randomBase62(0) should error")
	}
	if _, err := randomBase62(-1); err == nil {
		t.Error("randomBase62(-1) should error")
	}
}
