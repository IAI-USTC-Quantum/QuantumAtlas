package routes

import (
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/theorems"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/verifications"
	"github.com/pocketbase/pocketbase/tests"
)

func TestTheoremCollectionsMigrated(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	if _, err := app.FindCollectionByNameOrId(theorems.CollectionName); err != nil {
		t.Fatalf("theorems collection missing after migrations: %v", err)
	}
	if _, err := app.FindCollectionByNameOrId(verifications.CollectionName); err != nil {
		t.Fatalf("verifications collection missing after migrations: %v", err)
	}
}
