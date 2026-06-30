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
	  "contributes": {"capabilities": ["theorem.verify"], "subscribes": ["theorem.added"], "publishes": []},
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

func TestBuiltinsParticipateInEnabledDisabledFiltering(t *testing.T) {
	r, err := LoadDir(filepath.Join(t.TempDir(), "missing"), Options{
		Builtins: BuiltinManifests(),
		Enabled:  []string{"graph"},
	})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	got := r.List()
	if got[0].Status != StatusConnected {
		t.Fatalf("builtin status = %s, want connected", got[0].Status)
	}
	if !r.Available("graph") {
		t.Fatal("graph builtin should be available")
	}
	if r.Available("rag") {
		t.Fatal("rag builtin should not be available when enabled whitelist only contains graph")
	}
}

func TestNewBuiltinRegistryHonorsDisabled(t *testing.T) {
	r := NewBuiltinRegistry(Options{Disabled: []string{"graph"}})
	if r.Available("graph") {
		t.Fatal("graph should be disabled")
	}
	if !r.Available("rag") {
		t.Fatal("rag should remain available")
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
