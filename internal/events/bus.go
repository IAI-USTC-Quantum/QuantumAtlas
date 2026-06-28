package events

import (
	"context"
	"sync"
	"time"
)

type Event struct {
	ID      string         `json:"event_id"`
	Type    string         `json:"type"`
	Time    time.Time      `json:"ts"`
	Actor   string         `json:"actor,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

type Handler func(context.Context, Event) error

type Bus struct {
	mu          sync.RWMutex
	subscribers map[string][]Handler
}

func NewBus() *Bus {
	return &Bus{subscribers: map[string][]Handler{}}
}

func (b *Bus) Subscribe(eventType string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[eventType] = append(b.subscribers[eventType], h)
}

func (b *Bus) Publish(ctx context.Context, ev Event) []error {
	b.mu.RLock()
	handlers := append([]Handler(nil), b.subscribers[ev.Type]...)
	b.mu.RUnlock()
	errs := make([]error, 0)
	for _, h := range handlers {
		if err := h(ctx, ev); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
