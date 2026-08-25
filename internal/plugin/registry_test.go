package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirMissingReturnsEmptyRegistry(t *testing.T) {
	r, err := LoadDir(filepath.Join(t.TempDir(), "missing"), Options{})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

func TestLoadDirDiscoversManifestAndAppliesDisableWins(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "lean", `{
	  "id": "lean",
	  "name": "Lean",
	  "version": "1.0.0",
	  "abi_version": "1",
	  "kind": "external",
	  "transport": "socket",
	  "spawn": null,
	  "contributes": {"capabilities": ["lean.verify"], "subscribes": ["lean.added"], "publishes": []},
	  "needs": ["papers:read"]
	}`)

	r, err := LoadDir(dir, Options{Enabled: []string{"lean"}, Disabled: []string{"lean"}})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List len = %d, want 1", len(got))
	}
	if got[0].Status != StatusDisabled || got[0].Enabled {
		t.Fatalf("status/enabled = %s/%v, want disabled/false", got[0].Status, got[0].Enabled)
	}
}

func TestEnableDisable(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "lean", `{
	  "id": "lean",
	  "name": "Lean",
	  "version": "1.0.0",
	  "abi_version": "1",
	  "kind": "external",
	  "transport": "socket",
	  "spawn": null,
	  "contributes": {},
	  "needs": []
	}`)
	r, err := LoadDir(dir, Options{Disabled: []string{"lean"}})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if summary, ok := r.Enable("lean"); !ok || summary.Status != StatusDisconnected || !summary.Enabled {
		t.Fatalf("Enable = (%+v, %v), want disconnected enabled", summary, ok)
	}
	if summary, ok := r.Disable("lean"); !ok || summary.Status != StatusDisabled || summary.Enabled {
		t.Fatalf("Disable = (%+v, %v), want disabled", summary, ok)
	}
}

func TestIncompatibleABIStatus(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "future", `{
	  "id": "future",
	  "name": "Future",
	  "version": "1.0.0",
	  "abi_version": "99",
	  "kind": "external",
	  "transport": "socket",
	  "spawn": null,
	  "contributes": {},
	  "needs": []
	}`)
	r, err := LoadDir(dir, Options{})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	got := r.List()[0]
	if got.Status != StatusIncompatible || got.Error == "" {
		t.Fatalf("status/error = %s/%q, want incompatible with error", got.Status, got.Error)
	}
}

// syntheticBuiltin is a stand-in builtin manifest used by the registry
// tests — the repo currently ships no builtins (the Lean-content builtin
// moved out to an external plugin), so the filtering behavior is pinned
// against an injected manifest instead of BuiltinManifests().
func syntheticBuiltin(id string) Manifest {
	return Manifest{
		ID:         id,
		Name:       "Synthetic",
		Version:    "1.0.0",
		ABIVersion: HostABIVersion,
		Kind:       KindBuiltin,
	}
}

func TestBuiltinsParticipateInEnabledDisabledFiltering(t *testing.T) {
	r, err := LoadDir(filepath.Join(t.TempDir(), "missing"), Options{
		Builtins: []Manifest{syntheticBuiltin("synthetic")},
		Enabled:  []string{"synthetic"},
	})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	got := r.List()
	if got[0].Status != StatusConnected {
		t.Fatalf("builtin status = %s, want connected", got[0].Status)
	}
	if !r.Available("synthetic") {
		t.Fatal("synthetic builtin should be available")
	}
	if r.Available("unknown") {
		t.Fatal("unknown builtin should not be available")
	}
}

func TestNewBuiltinRegistryEmptyWhenNoBuiltins(t *testing.T) {
	// No builtins are compiled in today, so the registry must come up
	// empty regardless of the enabled/disabled options.
	r := NewBuiltinRegistry(Options{Disabled: []string{"synthetic"}})
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
	r2 := NewBuiltinRegistry(Options{})
	if got := r2.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

// TestIsEnabledPredicate pins the single enable/disable rule shared by the
// manifest registry and the in-process builtins: empty allowlist means all,
// the denylist always wins, and unknown ids never crash.
func TestIsEnabledPredicate(t *testing.T) {
	cases := []struct {
		name     string
		id       string
		enabled  []string
		disabled []string
		want     bool
	}{
		{"empty lists allow all", "graph", nil, nil, true},
		{"allowlist includes id", "graph", []string{"graph"}, nil, true},
		{"allowlist excludes others", "rag", []string{"graph"}, nil, false},
		{"denylist drops id", "graph", nil, []string{"graph"}, false},
		{"denylist wins over allowlist", "graph", []string{"graph"}, []string{"graph"}, false},
		{"unknown denylist id ignored", "graph", nil, []string{"typo"}, true},
	}
	for _, tc := range cases {
		if got := IsEnabled(tc.id, tc.enabled, tc.disabled); got != tc.want {
			t.Errorf("%s: IsEnabled(%q, %v, %v) = %v, want %v",
				tc.name, tc.id, tc.enabled, tc.disabled, got, tc.want)
		}
	}
}

func writeManifest(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
