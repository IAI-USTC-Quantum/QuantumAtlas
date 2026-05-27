package pat

import (
	"strings"
	"testing"
)

// TestNewEnforcer confirms the enforcer wires up cleanly and that the
// seeded policy table actually answers Yes/No for the obvious cases.
// Failures here are catastrophic (every write endpoint would 500) so
// keep the assertions painfully literal.
func TestNewEnforcer(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	// Direct .Enforce sanity check — bypasses Allows wrapper so we
	// can prove the seeded table is correct independently of the
	// helper's "*" short-circuit logic.
	cases := []struct {
		scope, obj, act string
		want            bool
	}{
		{ScopePapersWrite, "papers", "write", true},
		{ScopePapersWrite, "shares", "write", false},
		{ScopeSharesRead, "shares", "read", true},
		{ScopeSharesRead, "shares", "write", false},
		{ScopeSharesWrite, "shares", "read", true}, // write implies read
		{ScopeSharesWrite, "shares", "write", true},
		{ScopeSharesWrite, "papers", "write", false},
		{"bogus", "papers", "write", false},
	}
	for _, tc := range cases {
		got, err := e.Enforce(tc.scope, tc.obj, tc.act)
		if err != nil {
			t.Errorf("Enforce(%s,%s,%s): %v", tc.scope, tc.obj, tc.act, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Enforce(%s,%s,%s) = %v, want %v", tc.scope, tc.obj, tc.act, got, tc.want)
		}
	}
}

// TestAllows covers the held-list iteration + master short-circuit
// behavior the wrapper layers on top of the raw enforcer.
func TestAllows(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	cases := []struct {
		name     string
		held     []string
		obj, act string
		want     bool
	}{
		// Empty held = always deny (default-deny invariant).
		{"empty held denies", nil, "shares", "read", false},
		{"empty held denies write", nil, "papers", "write", false},

		// Single-scope grants.
		{"papers:write covers papers/write", []string{ScopePapersWrite}, "papers", "write", true},
		{"papers:write does not cover shares", []string{ScopePapersWrite}, "shares", "read", false},

		// Write implies read.
		{"shares:write covers shares/read", []string{ScopeSharesWrite}, "shares", "read", true},
		{"shares:read does not cover shares/write", []string{ScopeSharesRead}, "shares", "write", false},

		// Multiple scopes are OR-ed together.
		{"multi-scope covers union", []string{ScopePapersWrite, ScopeSharesRead}, "shares", "read", true},
		{"multi-scope still denies uncovered", []string{ScopePapersWrite, ScopeSharesRead}, "shares", "write", false},

		// Master wildcard short-circuit (session-token path).
		{"master covers anything", []string{ScopeMaster}, "papers", "write", true},
		{"master covers anything 2", []string{ScopeMaster}, "shares", "write", true},
		{"master in mixed list still wins", []string{ScopePapersWrite, ScopeMaster}, "shares", "write", true},

		// Unknown scope just doesn't match — it isn't an error, it
		// simply matches no policy. Validation is a separate concern
		// (ValidateScopes), kept out of the hot enforcement path.
		{"unknown scope denies", []string{"bogus:scope"}, "papers", "write", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Allows(e, tc.held, tc.obj, tc.act)
			if err != nil {
				t.Fatalf("Allows: %v", err)
			}
			if got != tc.want {
				t.Errorf("Allows(%v, %s, %s) = %v, want %v", tc.held, tc.obj, tc.act, got, tc.want)
			}
		})
	}
}

// TestAllows_NilEnforcer is a defensive check; wiring code that
// forgets to construct the enforcer must fail fast, not silently
// allow everything.
func TestAllows_NilEnforcer(t *testing.T) {
	_, err := Allows(nil, []string{ScopeMaster}, "papers", "write")
	if err == nil {
		t.Error("Allows with nil enforcer should error")
	}
}

// TestValidateScopes ensures the external-input gate rejects bogus
// scopes and (critically) refuses to grant the master wildcard via
// REST input — that is the only path a malicious caller could try to
// elevate themselves.
func TestValidateScopes(t *testing.T) {
	if err := ValidateScopes(nil); err != nil {
		t.Errorf("empty slice should be valid, got %v", err)
	}
	if err := ValidateScopes([]string{}); err != nil {
		t.Errorf("empty slice should be valid, got %v", err)
	}
	if err := ValidateScopes(AllScopes); err != nil {
		t.Errorf("all canonical scopes should be valid, got %v", err)
	}
	if err := ValidateScopes([]string{ScopePapersWrite, "bogus"}); err == nil {
		t.Error("bogus scope should fail validation")
	}
	if err := ValidateScopes([]string{ScopeMaster}); err == nil {
		t.Error("wildcard must be rejected from user input")
	}
	if err := ValidateScopes([]string{ScopePapersWrite, ScopeMaster}); err == nil {
		t.Error("wildcard mixed with valid scope must still be rejected")
	}
	// Error message should at least hint at what failed (for the
	// SPA's error display).
	err := ValidateScopes([]string{"nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error message should reference the bad scope, got %v", err)
	}
}

// TestScopeDescription_Coverage ensures every entry in AllScopes has
// human-readable copy. Drift here means the SPA shows a blank line
// next to a checkbox.
func TestScopeDescription_Coverage(t *testing.T) {
	for _, s := range AllScopes {
		if desc, ok := ScopeDescription[s]; !ok || desc == "" {
			t.Errorf("scope %q missing description", s)
		}
	}
}
