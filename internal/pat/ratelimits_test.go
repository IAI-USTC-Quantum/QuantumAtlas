// Tests for the PAT-surface rate-limit defaults installed at
// server startup. We verify both the idempotent EnsureDefaults
// installer and the matching helper directly, without spinning up
// the full PocketBase serve loop — same pattern as pat_test.go.

package pat

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// TestEnsureDefaults_FreshAppInstallsBothRules confirms the canonical
// behaviour: on a fresh app with no PAT rules, EnsureDefaults installs
// every rule in DefaultRateLimitRules and flips Enabled to true.
func TestEnsureDefaults_FreshAppInstallsBothRules(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Cleanup()

	if app.Settings().RateLimits.Enabled {
		t.Fatal("test app starts with RateLimits.Enabled — fixture changed?")
	}

	changed, err := EnsureDefaults(app)
	if err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	if !changed {
		t.Error("EnsureDefaults on fresh app should report changed=true")
	}
	if !app.Settings().RateLimits.Enabled {
		t.Error("Enabled should be flipped to true after first apply")
	}

	for _, want := range DefaultRateLimitRules {
		if !hasMatchingRule(app.Settings().RateLimits.Rules, want) {
			t.Errorf("rule missing after EnsureDefaults: %+v", want)
		}
	}
}

// TestEnsureDefaults_IsIdempotent is the critical property: calling it
// repeatedly (which happens every server restart) must NOT mutate the
// settings beyond the first call. Otherwise we'd cause WAL churn on
// every boot and double-register rules.
func TestEnsureDefaults_IsIdempotent(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Cleanup()

	if _, err := EnsureDefaults(app); err != nil {
		t.Fatalf("first EnsureDefaults: %v", err)
	}
	rulesAfterFirst := len(app.Settings().RateLimits.Rules)

	changed, err := EnsureDefaults(app)
	if err != nil {
		t.Fatalf("second EnsureDefaults: %v", err)
	}
	if changed {
		t.Error("second EnsureDefaults should report changed=false (idempotent)")
	}
	if got := len(app.Settings().RateLimits.Rules); got != rulesAfterFirst {
		t.Errorf("rule count drifted: %d → %d on second call", rulesAfterFirst, got)
	}
}

// TestEnsureDefaults_RespectsOperatorOverrides verifies we don't
// clobber a manually-tuned rule. If an admin has tightened the
// /api/pat limit (say 5/min instead of our 10/min default), our code
// must leave it alone — it's their deliberate policy.
func TestEnsureDefaults_RespectsOperatorOverrides(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer app.Cleanup()

	// Operator-tuned rule: tighter than our default.
	app.Settings().RateLimits.Rules = []core.RateLimitRule{
		{
			Label:       "POST /api/pat",
			Audience:    core.RateLimitRuleAudienceAuth,
			Duration:    60,
			MaxRequests: 5, // <-- tighter than our default 10
		},
	}

	if _, err := EnsureDefaults(app); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	// Should still have the operator's 5/min rule, NOT replaced with 10.
	var found *core.RateLimitRule
	for i, r := range app.Settings().RateLimits.Rules {
		if r.Label == "POST /api/pat" && r.Audience == core.RateLimitRuleAudienceAuth {
			found = &app.Settings().RateLimits.Rules[i]
			break
		}
	}
	if found == nil {
		t.Fatal("operator's POST /api/pat @auth rule was removed")
	}
	if found.MaxRequests != 5 {
		t.Errorf("operator's MaxRequests overridden to %d, want 5", found.MaxRequests)
	}

	// But the @guest rule (no operator override) should be added.
	var guestFound bool
	for _, r := range app.Settings().RateLimits.Rules {
		if r.Label == "POST /api/pat" && r.Audience == core.RateLimitRuleAudienceGuest {
			guestFound = true
			break
		}
	}
	if !guestFound {
		t.Error("guest-audience default rule should have been added alongside operator override")
	}
}

// TestDefaultRateLimitRules_ValidPolicy is a sanity guard: the canonical
// policy slice must satisfy PocketBase's own Validate() rules (positive
// Duration / MaxRequests, valid Audience), so the server doesn't refuse
// to save Settings on first boot.
func TestDefaultRateLimitRules_ValidPolicy(t *testing.T) {
	for _, r := range DefaultRateLimitRules {
		if err := r.Validate(); err != nil {
			t.Errorf("default rule %+v fails PocketBase Validate: %v", r, err)
		}
	}
}
