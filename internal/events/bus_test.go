package events

import (
	"context"
	"testing"
	"time"
)

func TestBusPublishesToMatchingSubscribers(t *testing.T) {
	b := NewBus()
	called := 0
	b.Subscribe("theorem.added", func(_ context.Context, ev Event) error {
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

	errs := b.Publish(context.Background(), Event{ID: "evt-1", Type: "theorem.added", Time: time.Now()})

	if len(errs) != 0 {
		t.Fatalf("Publish errors = %v", errs)
	}
	if called != 1 {
		t.Fatalf("called = %d, want 1", called)
	}
}
