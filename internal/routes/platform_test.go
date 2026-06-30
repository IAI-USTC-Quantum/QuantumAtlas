package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
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
