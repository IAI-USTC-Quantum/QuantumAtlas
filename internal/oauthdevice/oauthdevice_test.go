package oauthdevice

import (
	"regexp"
	"strings"
	"testing"
)

func TestGenerateCodesShape(t *testing.T) {
	t.Parallel()

	// 48 random bytes → ceil(48*4/3) = 64 chars of unpadded base64url.
	const deviceCodeLen = 64
	userCodeRe := regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}$`)

	for i := 0; i < 50; i++ {
		dc, uc, err := GenerateCodes()
		if err != nil {
			t.Fatalf("GenerateCodes: %v", err)
		}
		if len(dc) != deviceCodeLen {
			t.Errorf("device_code len = %d, want %d (value=%q)", len(dc), deviceCodeLen, dc)
		}
		// base64url alphabet check (no padding)
		for _, r := range dc {
			ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Errorf("device_code char %q not in url-safe base64 alphabet (full=%q)", r, dc)
				break
			}
		}
		if !userCodeRe.MatchString(uc) {
			t.Errorf("user_code = %q does not match shape XXXX-XXXX over ambiguity-free alphabet", uc)
		}
	}
}

func TestGenerateCodesNoCollisionWithinSample(t *testing.T) {
	t.Parallel()

	// 1024 codes is well under the birthday bound (40 bits → expected
	// first collision ≈ 2^20 ≈ 1M). Hitting one here means the RNG or
	// the alphabet is broken, not a flaky-test problem.
	const n = 1024
	devices := make(map[string]struct{}, n)
	users := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		dc, uc, err := GenerateCodes()
		if err != nil {
			t.Fatalf("GenerateCodes[%d]: %v", i, err)
		}
		if _, dup := devices[dc]; dup {
			t.Errorf("device_code collision at i=%d: %s", i, dc)
		}
		devices[dc] = struct{}{}
		if _, dup := users[uc]; dup {
			t.Errorf("user_code collision at i=%d: %s (acceptable if rare; investigate if frequent)", i, uc)
		}
		users[uc] = struct{}{}
	}
}

func TestHashDeviceCodeStable(t *testing.T) {
	t.Parallel()

	const sample = "xnA1k_8YJg-qZj4ZkqL6dGq7vQbZnA9eZxV3wXjNeM0n9k6N0o2c1p3qB4d5e6f7"
	want := HashDeviceCode(sample)
	if len(want) != 64 {
		t.Fatalf("sha256 hex len = %d, want 64", len(want))
	}
	for i := 0; i < 8; i++ {
		got := HashDeviceCode(sample)
		if got != want {
			t.Fatalf("HashDeviceCode is non-deterministic: got %q want %q (call %d)", got, want, i)
		}
	}
	if HashDeviceCode("different") == want {
		t.Fatalf("HashDeviceCode collided across distinct inputs")
	}
}

func TestNormalizeUserCodeRoundtrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    string
		wantErr error
	}{
		{"WDJB-MJHT", "WDJB-MJHT", nil},
		{"wdjb-mjht", "WDJB-MJHT", nil},
		{"wdjbmjht", "WDJB-MJHT", nil},
		{"wdjb mjht", "WDJB-MJHT", nil},
		{"  WDJB-MJHT  ", "WDJB-MJHT", nil},
		{"W-D-J-B-M-J-H-T", "WDJB-MJHT", nil},
		{"", "", ErrEmpty},
		{"   ", "", ErrEmpty},
		{"...!!!", "", ErrEmpty},
		{"WDJB", "", ErrShape},
		{"WDJBMJHTX", "", ErrShape},
		{"WDJBMJHTAB", "", ErrShape},
	}
	for _, tc := range cases {
		got, err := NormalizeUserCode(tc.in)
		if err != tc.wantErr {
			t.Errorf("NormalizeUserCode(%q) err = %v, want %v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeUserCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUserCodeAlphabetIsAmbiguityFree(t *testing.T) {
	t.Parallel()

	const banned = "0OIl1"
	for _, r := range banned {
		if strings.ContainsRune(userCodeAlphabet, r) {
			t.Errorf("userCodeAlphabet contains ambiguous char %q", r)
		}
	}
	if 256%len(userCodeAlphabet) != 0 {
		t.Fatalf("len(userCodeAlphabet)=%d must divide 256 evenly to avoid modulo bias", len(userCodeAlphabet))
	}
}
