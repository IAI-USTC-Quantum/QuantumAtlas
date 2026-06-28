package hostapi

import (
	"context"
	"fmt"
	"sync"
)

type Handler func(context.Context, any) (any, error)

type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}}
}

func (r *Registry) Register(method string, h Handler) error {
	if method == "" {
		return fmt.Errorf("hostapi: method is required")
	}
	if h == nil {
		return fmt.Errorf("hostapi: handler for %s is nil", method)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[method]; exists {
		return fmt.Errorf("hostapi: method %s already registered", method)
	}
	r.handlers[method] = h
	return nil
}

func (r *Registry) Call(ctx context.Context, method string, params any) (any, error) {
	r.mu.RLock()
	h := r.handlers[method]
	r.mu.RUnlock()
	if h == nil {
		return nil, fmt.Errorf("hostapi: method %s not found", method)
	}
	return h(ctx, params)
}
