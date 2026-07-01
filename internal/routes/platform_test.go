package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/pocketbase/pocketbase/core"
)

// Compile-time: the pull plugins satisfy GitPullPlugin; the non-pull plugins
// satisfy BuiltinPlugin only.
var (
	_ GitPullPlugin = (*WikiPlugin)(nil)
	_ GitPullPlugin = (*TheoremsPlugin)(nil)
	_ BuiltinPlugin = graphBuiltin{}
	_ BuiltinPlugin = ragBuiltin{}
)

func TestNonPullBuiltinsAreNotGitPull(t *testing.T) {
	if _, ok := any(NewGraphPlugin()).(GitPullPlugin); ok {
		t.Fatal("graph must not be a GitPullPlugin")
	}
	if _, ok := any(NewRAGPlugin()).(GitPullPlugin); ok {
		t.Fatal("rag must not be a GitPullPlugin")
	}
}

func TestGitSyncStatusShape(t *testing.T) {
	dir := t.TempDir() // exists but not a git repo
	tp := &TheoremsPlugin{cfg: &config.Config{TheoremsDir: dir}}
	st := gitSyncStatus(tp)
	if st["id"] != "theorems" {
		t.Fatalf("id = %v, want theorems", st["id"])
	}
	if st["exists"] != true {
		t.Fatalf("exists = %v, want true", st["exists"])
	}
	if _, ok := st["git"]; !ok {
		t.Fatal("missing git key")
	}
}

func TestGitSyncStatusMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	tp := &TheoremsPlugin{cfg: &config.Config{TheoremsDir: missing}}
	st := gitSyncStatus(tp)
	if st["exists"] != false {
		t.Fatalf("exists = %v, want false for missing dir", st["exists"])
	}
}

func TestPluginIDsAndRepoDirs(t *testing.T) {
	cfg := &config.Config{WikiDir: "/w", TheoremsDir: "/t"}
	w := NewWikiPlugin(cfg, nil)
	if w.PluginID() != "wiki" || w.GitRepoDir() != "/w" {
		t.Fatalf("wiki id/dir = %q/%q", w.PluginID(), w.GitRepoDir())
	}
	tp := NewTheoremsPlugin(cfg, nil)
	if tp.PluginID() != "theorems" || tp.GitRepoDir() != "/t" {
		t.Fatalf("theorems id/dir = %q/%q", tp.PluginID(), tp.GitRepoDir())
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	if !dirExists(dir) {
		t.Fatal("temp dir should exist")
	}
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirExists(f) {
		t.Fatal("a file is not a dir")
	}
}

// spyBuiltin is a minimal BuiltinPlugin that records whether its routes were
// registered, so the filter test can assert config gating without a live
// router. It deliberately does NOT implement GitPullPlugin, so RegisterBuiltins
// never dereferences the nil ServeEvent through mountGitSync.
type spyBuiltin struct {
	id         string
	registered *[]string
}

func (s spyBuiltin) PluginID() string { return s.id }
func (s spyBuiltin) RegisterRoutes(_ *core.ServeEvent, _ PluginDeps) error {
	*s.registered = append(*s.registered, s.id)
	return nil
}

// TestRegisterBuiltinsFiltersByConfig pins the config-driven gate: which
// builtins register is decided by PluginsEnabled/PluginsDisabled, not the
// hardcoded arg list. Default (both empty) keeps every builtin on.
func TestRegisterBuiltinsFiltersByConfig(t *testing.T) {
	order := []string{"graph", "rag", "wiki", "theorems"}
	cases := []struct {
		name     string
		enabled  []string
		disabled []string
		want     []string
	}{
		{"default enables all", nil, nil, order},
		{"denylist drops theorems", nil, []string{"theorems"}, []string{"graph", "rag", "wiki"}},
		{"allowlist keeps only graph", []string{"graph"}, nil, []string{"graph"}},
		{"denylist wins over allowlist", []string{"graph"}, []string{"graph"}, nil},
		{"unknown denylist id is ignored", nil, []string{"typo"}, order},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			plugins := make([]BuiltinPlugin, len(order))
			for i, id := range order {
				plugins[i] = spyBuiltin{id: id, registered: &got}
			}
			deps := PluginDeps{Cfg: &config.Config{
				PluginsEnabled:  tc.enabled,
				PluginsDisabled: tc.disabled,
			}}
			if err := RegisterBuiltins(nil, deps, plugins...); err != nil {
				t.Fatalf("RegisterBuiltins: %v", err)
			}
			if !equalStrings(got, tc.want) {
				t.Fatalf("registered = %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
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
