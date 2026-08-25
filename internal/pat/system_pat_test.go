package pat

import (
	"strings"
	"testing"
)

// TestLoadSystemPAT_DisabledWhenUnset covers the "feature off"
// path: empty / all-whitespace tokens collapse to (nil, nil) so the
// caller treats them uniformly.
func TestLoadSystemPAT_DisabledWhenUnset(t *testing.T) {
	for _, val := range []string{"", "   ", "\n\t"} {
		s, err := LoadSystemPAT(val, nil)
		if err != nil {
			t.Fatalf("LoadSystemPAT(%q) returned error: %v", val, err)
		}
		if s != nil {
			t.Fatalf("LoadSystemPAT(%q) returned non-nil receiver: %+v", val, s)
		}
		if s.Enabled() {
			t.Fatalf("nil SystemPAT.Enabled() should be false")
		}
	}
}

func TestLoadSystemPAT_RejectsShortSecret(t *testing.T) {
	_, err := LoadSystemPAT("short", nil)
	if err == nil {
		t.Fatal("LoadSystemPAT should reject a 5-char secret")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("error should mention 'too short', got: %v", err)
	}
}

func TestLoadSystemPAT_DefaultScopesIsMaster(t *testing.T) {
	s, err := LoadSystemPAT("x-very-long-secret-value-here-32chars!", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled() {
		t.Fatal("expected enabled SystemPAT")
	}
	got := s.Scopes()
	if len(got) != 1 || got[0] != ScopeMaster {
		t.Fatalf("default scopes should be [%q]; got %v", ScopeMaster, got)
	}
}

func TestLoadSystemPAT_CustomScopes(t *testing.T) {
	s, err := LoadSystemPAT("x-very-long-secret-value-here-32chars!",
		[]string{"plugins:read", " papers:read ", "papers:write"})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Scopes()
	want := []string{"plugins:read", "papers:read", "papers:write"}
	if !equalStringSlice(got, want) {
		t.Fatalf("scopes %v, want %v", got, want)
	}
}

// Master is allowed in system_pat.scopes (operator-trusted config
// file) where it would be rejected on the /api/pat REST path.
func TestLoadSystemPAT_MasterAllowedInExplicitList(t *testing.T) {
	s, err := LoadSystemPAT("x-very-long-secret-value-here-32chars!", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Scopes()
	if len(got) != 1 || got[0] != ScopeMaster {
		t.Fatalf("want [%q], got %v", ScopeMaster, got)
	}
}

func TestLoadSystemPAT_RejectsBogusScope(t *testing.T) {
	_, err := LoadSystemPAT("x-very-long-secret-value-here-32chars!",
		[]string{"papers:read", "bogus:scope"})
	if err == nil {
		t.Fatal("LoadSystemPAT should reject unknown scope")
	}
}

func TestLoadSystemPAT_RejectsEmptyScopeList(t *testing.T) {
	_, err := LoadSystemPAT("x-very-long-secret-value-here-32chars!",
		[]string{" ", "  "})
	if err == nil {
		t.Fatal("LoadSystemPAT should reject scope list that parses to nothing")
	}
}

// Match returns the configured scopes on a correct bearer and
// (nil, false) on every other input.
func TestSystemPAT_Match(t *testing.T) {
	s, err := LoadSystemPAT("qatlas-test-secret-very-long", []string{"plugins:read"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("exact match returns scopes", func(t *testing.T) {
		scopes, ok := s.Match("qatlas-test-secret-very-long")
		if !ok {
			t.Fatal("expected match")
		}
		if len(scopes) != 1 || scopes[0] != "plugins:read" {
			t.Fatalf("scopes %v, want [plugins:read]", scopes)
		}
	})

	t.Run("wrong byte at same length rejected", func(t *testing.T) {
		_, ok := s.Match("qatlas-test-secret-very-LONG") // different case
		if ok {
			t.Fatal("expected mismatch")
		}
	})

	t.Run("wrong length rejected", func(t *testing.T) {
		_, ok := s.Match("qatlas-test-secret-very-long-and-then-some")
		if ok {
			t.Fatal("expected length mismatch to reject")
		}
		_, ok = s.Match("short")
		if ok {
			t.Fatal("expected length mismatch to reject (too short)")
		}
	})

	t.Run("empty bearer rejected", func(t *testing.T) {
		_, ok := s.Match("")
		if ok {
			t.Fatal("expected empty bearer to reject")
		}
	})
}

// A nil receiver short-circuits to "feature disabled" — callers
// can carry a *SystemPAT around without nil-checking.
func TestSystemPAT_NilReceiver(t *testing.T) {
	var s *SystemPAT
	if s.Enabled() {
		t.Fatal("nil SystemPAT should not be enabled")
	}
	if s.Length() != 0 {
		t.Fatal("nil SystemPAT.Length() should be 0")
	}
	if scopes := s.Scopes(); scopes != nil {
		t.Fatalf("nil SystemPAT.Scopes() should be nil, got %v", scopes)
	}
	if _, ok := s.Match("anything"); ok {
		t.Fatal("nil SystemPAT should never match")
	}
}

// Returned scope slices must be copies — mutating them must not
// poison the cached canonical value.
func TestSystemPAT_ScopesIsCopy(t *testing.T) {
	s, err := LoadSystemPAT("qatlas-test-secret-very-long", []string{"plugins:read"})
	if err != nil {
		t.Fatal(err)
	}

	scopes, ok := s.Match("qatlas-test-secret-very-long")
	if !ok {
		t.Fatal("expected match")
	}
	scopes[0] = "MUTATED"

	again, ok := s.Match("qatlas-test-secret-very-long")
	if !ok {
		t.Fatal("expected match")
	}
	if again[0] != "plugins:read" {
		t.Fatalf("canonical scope was mutated through returned slice: %q", again[0])
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
