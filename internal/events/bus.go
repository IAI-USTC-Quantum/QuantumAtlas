package events

import (
	"context"
	"fmt"
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

func NewID() string {
	return fmt.Sprintf("evt-%d", time.Now().UnixNano())
}

func (b *Bus) Subscribe(eventType string, h Handler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[eventType] = append(b.subscribers[eventType], h)
	idx := len(b.subscribers[eventType]) - 1
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		handlers := b.subscribers[eventType]
		if idx >= len(handlers) || handlers[idx] == nil {
			return
		}
		handlers[idx] = nil
	}
}

func (b *Bus) Publish(ctx context.Context, ev Event) []error {
	b.mu.RLock()
	handlers := append([]Handler(nil), b.subscribers[ev.Type]...)
	b.mu.RUnlock()
	errs := make([]error, 0)
	for _, h := range handlers {
		if h == nil {
			continue
		}
		if err := h(ctx, ev); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
