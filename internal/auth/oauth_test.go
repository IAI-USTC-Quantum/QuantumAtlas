package auth

import (
	"regexp"
	"testing"
)

func TestDeriveStableUserID_DeterministicAndProperShape(t *testing.T) {
	got := deriveStableUserID("github", "12345")
	if len(got) != stableUserIDLength {
		t.Errorf("len = %d, want %d", len(got), stableUserIDLength)
	}
	if got[:len(StableUserIDPrefix)] != StableUserIDPrefix {
		t.Errorf("missing prefix %q in %q", StableUserIDPrefix, got)
	}
	// Default PocketBase id charset is alphanumeric.
	if !regexp.MustCompile(`^[a-zA-Z0-9]+$`).MatchString(got) {
		t.Errorf("id %q is not pure alphanumeric (would fail PB id validator)", got)
	}
	// Determinism: same inputs → same id, every call.
	for i := 0; i < 5; i++ {
		if deriveStableUserID("github", "12345") != got {
			t.Errorf("deriveStableUserID is not deterministic (call %d)", i)
		}
	}
}

// Cross-provider collision protection: different providers with the
// same provider-user-id MUST produce different qatlas ids.
func TestDeriveStableUserID_ProviderSeparation(t *testing.T) {
	gh := deriveStableUserID("github", "12345")
	g := deriveStableUserID("google", "12345")
	if gh == g {
		t.Errorf("github and google with same provider-user-id collided: %s", gh)
	}
}

// Different provider-user-ids → different qatlas ids (no obvious
// truncation collision in the 14-hex-char window).
func TestDeriveStableUserID_UserSeparation(t *testing.T) {
	a := deriveStableUserID("github", "12345")
	b := deriveStableUserID("github", "12346")
	if a == b {
		t.Errorf("adjacent provider-user-ids collided: %s", a)
	}
}

// Empty inputs are still hashed — we don't treat them specially. (The
// caller in syncStableUserID short-circuits before reaching us when
// provider or user id is blank, but deriveStableUserID itself is pure
// and predictable for any input.)
func TestDeriveStableUserID_EmptyInputsStillHash(t *testing.T) {
	got := deriveStableUserID("", "")
	if len(got) != stableUserIDLength {
		t.Errorf("len = %d, want %d", len(got), stableUserIDLength)
	}
}
