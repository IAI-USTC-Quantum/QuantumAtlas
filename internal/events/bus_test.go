package events

import (
	"context"
	"testing"
	"time"
)

func TestBusPublishesToMatchingSubscribers(t *testing.T) {
	b := NewBus()
	called := 0
	unsubscribe := b.Subscribe("lean.added", func(_ context.Context, ev Event) error {
		called++
		if ev.ID != "evt-1" {
			t.Fatalf("event id = %q", ev.ID)
		}
		return nil
	})
	b.Subscribe("paper.added", func(context.Context, Event) error {
		t.Fatal("non-matching subscriber called")
		return nil
	})

	errs := b.Publish(context.Background(), Event{ID: "evt-1", Type: "lean.added", Time: time.Now()})

	if len(errs) != 0 {
		t.Fatalf("Publish errors = %v", errs)
	}
	if called != 1 {
		t.Fatalf("called = %d, want 1", called)
	}
	unsubscribe()
	_ = b.Publish(context.Background(), Event{ID: "evt-2", Type: "lean.added", Time: time.Now()})
	if called != 1 {
		t.Fatalf("called after unsubscribe = %d, want 1", called)
	}
}
