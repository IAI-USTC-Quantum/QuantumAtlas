package tests

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A checkout/module zip must expose the Go toolchain, not a retired root Python
// application or a second Pixi dependency graph. Python docs/CI helpers are
// explicitly retained, as is the permanent old-package migration notice.
// Release versions come only from Git tags, never a hand-maintained root file.
func TestGoNativeRepositoryBoundary(t *testing.T) {
	root := composeRoot(t)
	for _, name := range []string{
		"pyproject.toml", "uv.lock", "pixi.lock", "setup.py", "setup.cfg",
		".github/workflows/pytest.yml", "qatlas/__init__.py",
		"scripts/wiki_pipeline/merge_concepts.py", "VERSION",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("retired root tooling/source %s must not be present (stat: %v)", name, err)
		}
	}
	for _, name := range []string{
		"go.mod", "go.sum", ".goreleaser.yaml", "PYPI_README.md",
		"docsite/conf.py", ".github/scripts/build_docs.py", "docsite/components.lock.json",
		".github/scripts/artifacts.py", ".github/scripts/docs_sources.py", "Dockerfile.goreleaser",
	} {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil || !info.Mode().IsRegular() {
			t.Errorf("required source/documentation tool %s missing: %v", name, err)
		}
	}
	module := string(composeRead(t, root, "go.mod"))
	if !strings.HasPrefix(module, "module github.com/IAI-USTC-Quantum/QuantumAtlas\n") {
		t.Error("module identity changed; go install must keep its published path")
	}
}

func TestConfigTemplateSourceStaysSynchronized(t *testing.T) {
	root := composeRoot(t)
	// config init embeds the command-local template. The root example must not
	// quietly recommend different fields/defaults to developers.
	rootTemplate := string(composeRead(t, root, "config.example.yaml"))
	embeddedTemplate := string(composeRead(t, root, "cmd/qatlasd/templates/config.yaml"))
	if rootTemplate != embeddedTemplate {
		t.Error("config.example.yaml must match cmd/qatlasd/templates/config.yaml")
	}
}
