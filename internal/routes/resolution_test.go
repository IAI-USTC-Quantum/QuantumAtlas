package routes

import (
	"reflect"
	"strings"
	"testing"
)

// TestComputeResolution_NoChange verifies that fully canonical input
// produces an empty resolution (no defaults applied, no rename).
func TestComputeResolution_NoChange(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"new-style versioned", "2501.00010v1"},
		{"old-style canonical versioned", "quant-ph/9508027v2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := computeResolution(c.id, c.id, c.id)
			if r.hasChanges() {
				t.Errorf("hasChanges() = true for canonical input %q; defaults=%v resolved=%q",
					c.id, r.DefaultsApplied, r.ResolvedID)
			}
		})
	}
}

// TestComputeResolution_BareArxivIDFillsVersion verifies the
// "user passed `0811.3171`, server resolved to `0811.3171v3`" case
// surfaces version=v3.
func TestComputeResolution_BareArxivIDFillsVersion(t *testing.T) {
	r := computeResolution("0811.3171", "0811.3171", "0811.3171v3")
	if !r.hasChanges() {
		t.Fatal("hasChanges() = false for bare→versioned input")
	}
	if r.RequestedID != "0811.3171" {
		t.Errorf("RequestedID = %q, want 0811.3171", r.RequestedID)
	}
	if r.ResolvedID != "0811.3171v3" {
		t.Errorf("ResolvedID = %q, want 0811.3171v3", r.ResolvedID)
	}
	if len(r.DefaultsApplied) != 1 || !strings.Contains(r.DefaultsApplied[0], "version=v3") {
		t.Errorf("DefaultsApplied = %v, want one entry mentioning version=v3", r.DefaultsApplied)
	}
}

// TestComputeResolution_BareOldStyleAddsCategoryAndVersion verifies
// that a fully-bare old-style input ("9508027") surfaces BOTH
// defaults: version + category=quant-ph.
func TestComputeResolution_BareOldStyleAddsCategoryAndVersion(t *testing.T) {
	r := computeResolution("9508027", "9508027", "9508027v2")
	if !r.hasChanges() {
		t.Fatal("hasChanges() = false for bare old-style input")
	}
	if len(r.DefaultsApplied) != 2 {
		t.Fatalf("want 2 defaults (version + category), got %d: %v", len(r.DefaultsApplied), r.DefaultsApplied)
	}
	joined := strings.Join(r.DefaultsApplied, "|")
	if !strings.Contains(joined, "version=v2") {
		t.Errorf("missing version=v2 in %q", joined)
	}
	if !strings.Contains(joined, "category=quant-ph") {
		t.Errorf("missing category=quant-ph in %q", joined)
	}
}

// TestComputeResolution_DOIChain verifies the full DOI → bare arxiv →
// versioned chain surfaces all three steps.
func TestComputeResolution_DOIChain(t *testing.T) {
	r := computeResolution("10.1103/PhysRevLett.103.150502", "0811.3171", "0811.3171v3")
	if !r.hasChanges() {
		t.Fatal("hasChanges() = false for DOI input")
	}
	if r.RequestedID != "10.1103/PhysRevLett.103.150502" {
		t.Errorf("RequestedID = %q, want the DOI", r.RequestedID)
	}
	if r.ResolvedID != "0811.3171v3" {
		t.Errorf("ResolvedID = %q, want 0811.3171v3", r.ResolvedID)
	}
	joined := strings.Join(r.DefaultsApplied, "|")
	if !strings.Contains(joined, "doi_resolved_via_openalex") {
		t.Errorf("missing doi_resolved_via_openalex hint in %q", joined)
	}
	if !strings.Contains(joined, "version=v3") {
		t.Errorf("missing version=v3 hint in %q", joined)
	}
}

// TestEmbedResolutionInBody_KeysWhenChanged verifies the JSON body
// gets requested_id / resolved_id / defaults_applied appended.
func TestEmbedResolutionInBody_KeysWhenChanged(t *testing.T) {
	body := map[string]any{"state": "queued"}
	r := computeResolution("9508027", "9508027", "9508027v2")
	embedResolutionInBody(body, r)
	if body["requested_id"] != "9508027" {
		t.Errorf("requested_id = %v, want 9508027", body["requested_id"])
	}
	if body["resolved_id"] != "9508027v2" {
		t.Errorf("resolved_id = %v, want 9508027v2", body["resolved_id"])
	}
	def, ok := body["defaults_applied"].([]string)
	if !ok {
		t.Fatalf("defaults_applied type = %T, want []string", body["defaults_applied"])
	}
	if !reflect.DeepEqual(def, r.DefaultsApplied) {
		t.Errorf("defaults_applied = %v, want %v", def, r.DefaultsApplied)
	}
}

// TestEmbedResolutionInBody_NoopWhenUnchanged keeps the body lean
// for canonical inputs.
func TestEmbedResolutionInBody_NoopWhenUnchanged(t *testing.T) {
	body := map[string]any{"state": "cached"}
	r := computeResolution("quant-ph/9508027v2", "quant-ph/9508027v2", "quant-ph/9508027v2")
	embedResolutionInBody(body, r)
	if _, has := body["requested_id"]; has {
		t.Errorf("requested_id should be absent for canonical input; body=%v", body)
	}
	if _, has := body["defaults_applied"]; has {
		t.Errorf("defaults_applied should be absent for canonical input; body=%v", body)
	}
}
