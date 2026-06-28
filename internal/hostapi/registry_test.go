package hostapi

import (
	"context"
	"testing"
)

func TestRegistryCall(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("papers/getMarkdown", func(_ context.Context, params any) (any, error) {
		return map[string]any{"params": params}, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.Call(context.Background(), "papers/getMarkdown", map[string]string{"id": "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got.(map[string]any)["params"] == nil {
		t.Fatalf("Call did not pass params: %v", got)
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	h := func(context.Context, any) (any, error) { return nil, nil }
	if err := r.Register("x", h); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register("x", h); err == nil {
		t.Fatal("duplicate Register returned nil")
	}
}
