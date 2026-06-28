package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	StatusConnected    = "connected"
	StatusDisconnected = "disconnected"
	StatusDisabled     = "disabled"
	StatusIncompatible = "incompatible"
)

type Options struct {
	Enabled  []string
	Disabled []string
	Builtins []Manifest
}

type Plugin struct {
	Manifest Manifest `json:"manifest"`
	Status   string   `json:"status"`
	Enabled  bool     `json:"enabled"`
	Error    string   `json:"error,omitempty"`
}

type Summary struct {
	ID          string      `json:"id"`
	Name        string      `json:"name,omitempty"`
	Version     string      `json:"version,omitempty"`
	ABIVersion  string      `json:"abi_version,omitempty"`
	Transport   string      `json:"transport,omitempty"`
	Status      string      `json:"status"`
	Enabled     bool        `json:"enabled"`
	Error       string      `json:"error,omitempty"`
	Contributes Contributes `json:"contributes"`
	Needs       []string    `json:"needs"`
}

type Registry struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin
}

func NewBuiltinRegistry(opts Options) *Registry {
	r := &Registry{plugins: map[string]*Plugin{}}
	enabledSet := stringSet(opts.Enabled)
	disabledSet := stringSet(opts.Disabled)
	allowAll := len(enabledSet) == 0
	for _, manifest := range BuiltinManifests() {
		_ = r.addManifest(manifest, allowAll, enabledSet, disabledSet)
	}
	return r
}

func LoadDir(dir string, opts Options) (*Registry, error) {
	r := &Registry{plugins: map[string]*Plugin{}}
	enabledSet := stringSet(opts.Enabled)
	disabledSet := stringSet(opts.Disabled)
	allowAll := len(enabledSet) == 0

	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read plugins dir %s: %w", dir, err)
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := ReadManifest(filepath.Join(dir, entry.Name(), "plugin.json"))
		if err != nil {
			return nil, err
		}
		if err := r.addManifest(manifest, allowAll, enabledSet, disabledSet); err != nil {
			return nil, err
		}
	}
	for _, manifest := range opts.Builtins {
		if _, exists := r.plugins[manifest.ID]; exists {
			continue
		}
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("validate builtin plugin %s: %w", manifest.ID, err)
		}
		if err := r.addManifest(manifest, allowAll, enabledSet, disabledSet); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) addManifest(manifest Manifest, allowAll bool, enabledSet, disabledSet map[string]bool) error {
	if _, exists := r.plugins[manifest.ID]; exists {
		return fmt.Errorf("duplicate plugin id %q", manifest.ID)
	}
	enabled := (allowAll || enabledSet[manifest.ID]) && !disabledSet[manifest.ID]
	status := StatusDisconnected
	errMsg := ""
	if !enabled {
		status = StatusDisabled
	} else if manifest.ABIVersion != HostABIVersion {
		status = StatusIncompatible
		errMsg = fmt.Sprintf("plugin ABI %s is incompatible with host ABI %s", manifest.ABIVersion, HostABIVersion)
	} else if manifest.Transport == TransportInProcessGo {
		status = StatusConnected
	}
	r.plugins[manifest.ID] = &Plugin{
		Manifest: manifest,
		Status:   status,
		Enabled:  enabled,
		Error:    errMsg,
	}
	return nil
}

func (r *Registry) List() []Summary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Summary, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p.summary())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Available(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.plugins[id]
	return p != nil && p.Enabled && p.Status != StatusIncompatible
}

func (r *Registry) Enable(id string) (Summary, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.plugins[id]
	if p == nil {
		return Summary{}, false
	}
	p.Enabled = true
	if p.Manifest.ABIVersion != HostABIVersion {
		p.Status = StatusIncompatible
		p.Error = fmt.Sprintf("plugin ABI %s is incompatible with host ABI %s", p.Manifest.ABIVersion, HostABIVersion)
	} else if p.Manifest.Transport == TransportInProcessGo {
		p.Status = StatusConnected
		p.Error = ""
	} else {
		p.Status = StatusDisconnected
		p.Error = ""
	}
	return p.summary(), true
}

func (r *Registry) Disable(id string) (Summary, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.plugins[id]
	if p == nil {
		return Summary{}, false
	}
	p.Enabled = false
	p.Status = StatusDisabled
	p.Error = ""
	return p.summary(), true
}

func (p *Plugin) summary() Summary {
	return Summary{
		ID:          p.Manifest.ID,
		Name:        p.Manifest.Name,
		Version:     p.Manifest.Version,
		ABIVersion:  p.Manifest.ABIVersion,
		Transport:   p.Manifest.Transport,
		Status:      p.Status,
		Enabled:     p.Enabled,
		Error:       p.Error,
		Contributes: p.Manifest.Contributes,
		Needs:       append([]string(nil), p.Manifest.Needs...),
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			out[v] = true
		}
	}
	return out
}
