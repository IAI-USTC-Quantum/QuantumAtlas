package routes

import (
	"testing"

	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"
)

func TestPluginAvailableReflectsRegistryChanges(t *testing.T) {
	registry := qplugin.NewBuiltinRegistry(qplugin.Options{})
	if !pluginAvailable(registry, "graph") {
		t.Fatal("graph should start available")
	}
	if _, ok := registry.Disable("graph"); !ok {
		t.Fatal("Disable(graph) failed")
	}
	if pluginAvailable(registry, "graph") {
		t.Fatal("graph should become unavailable after runtime disable")
	}
	if _, ok := registry.Enable("graph"); !ok {
		t.Fatal("Enable(graph) failed")
	}
	if !pluginAvailable(registry, "graph") {
		t.Fatal("graph should become available after runtime enable")
	}
}
