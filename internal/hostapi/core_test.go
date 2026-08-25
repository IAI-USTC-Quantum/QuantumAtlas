package hostapi

import (
	"context"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/events"
)

func TestEventsPublish(t *testing.T) {
	bus := events.NewBus()
	called := false
	bus.Subscribe("lean.verification.completed", func(_ context.Context, ev events.Event) error {
		called = ev.Payload["lemma_id"] == "lem-1"
		return nil
	})
	h := eventsPublish(bus)
	result, err := h(context.Background(), map[string]any{
		"type":    "lean.verification.completed",
		"payload": map[string]any{"lemma_id": "lem-1"},
	})
	if err != nil {
		t.Fatalf("eventsPublish: %v", err)
	}
	if result.(map[string]any)["event_id"] == "" {
		t.Fatalf("missing event_id: %v", result)
	}
	if !called {
		t.Fatal("subscriber was not called")
	}
}
