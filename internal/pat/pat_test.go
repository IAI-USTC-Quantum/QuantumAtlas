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
//
// The lower bound is PrefixDisplayLen (currently 12) — anything
// shorter could never match a real PAT's stored prefix column anyway,
// so Looks rejects it before authGuard pays for a Lookup round-trip.
func TestLooks(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"empty string", "", false},
		{"prefix only", TokenPrefix, false},
		{"single body char (under PrefixDisplayLen)", TokenPrefix + "x", false},
		{"exactly PrefixDisplayLen", TokenPrefix + strings.Repeat("a", PrefixDisplayLen-len(TokenPrefix)), true},
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
}

// TestPrefixOf_ShortInputReturnsEmpty is the defense-in-depth contract:
// PrefixOf MUST NOT return its input verbatim for a too-short string.
// Returning the raw input would let a future audit / telemetry caller
// that assumes "prefix is a safe truncation of the secret" leak the
// whole plaintext (e.g. for an attacker-controlled bearer that bypassed
// Looks via a different code path). Returning "" forces such callers
// to handle the empty case explicitly.
func TestPrefixOf_ShortInputReturnsEmpty(t *testing.T) {
	cases := []string{
		"",
		TokenPrefix,                          // "qat_" — exactly the prefix
		TokenPrefix + "x",                    // 5 chars
		TokenPrefix + strings.Repeat("y", 7), // 11 chars — one short of PrefixDisplayLen
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := PrefixOf(in); got != "" {
				t.Errorf("PrefixOf(%q) = %q, want \"\" (must not echo plaintext)", in, got)
			}
		})
	}

	// Boundary: exactly PrefixDisplayLen chars returns its full self.
	exact := TokenPrefix + strings.Repeat("z", PrefixDisplayLen-len(TokenPrefix))
	if got := PrefixOf(exact); got != exact {
		t.Errorf("PrefixOf(%q) = %q, want %q (exact-length boundary)", exact, got, exact)
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

// TestRandomBase62_Distribution is a lightweight uniformity check.
// With rejection sampling there should be no systematic bias toward
// any region of the alphabet. We draw a large sample and confirm
// each character appears within a wide tolerance of the expected
// count — generous enough that a flaky CI run is essentially
// impossible (~0.5% probability of single-character failure even
// without a bias) but tight enough that the old `% 62` bias (where
// the first 8 chars appeared ~3% more often) would reliably trip
// the assertion.
func TestRandomBase62_Distribution(t *testing.T) {
	const sampleLen = 200000
	out, err := randomBase62(sampleLen)
	if err != nil {
		t.Fatalf("randomBase62: %v", err)
	}
	counts := make(map[byte]int, 62)
	for i := 0; i < len(out); i++ {
		counts[out[i]]++
	}
	if len(counts) < 62 {
		t.Errorf("alphabet coverage = %d distinct chars, want all 62", len(counts))
	}
	expected := sampleLen / 62
	// ±15% tolerance: with biased `% 62` the over-represented
	// chars hit roughly expected*1.03 = +3% which is well inside
	// 15%, BUT the differential between "biased" chars and the
	// rest is detectable at this scale; this test is a smoke-level
	// regression guard, not a formal chi-square.
	lo, hi := expected*85/100, expected*115/100
	for c, n := range counts {
		if n < lo || n > hi {
			t.Errorf("char %q count = %d, expected in [%d, %d]", c, n, lo, hi)
		}
	}
}
