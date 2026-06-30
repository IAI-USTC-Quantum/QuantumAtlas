package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

const HostABIVersion = "1"

// Plugin type is two orthogonal axes (ADR 0001):
//
//   - Kind — where the plugin lives. builtin = first-party, compiled into
//     qatlasd, runs in-process as Go; external = third-party, a separate
//     process speaking JSON-RPC to the host.
//   - Transport — how the host talks to an external plugin (meaningless for
//     builtin). socket = the plugin dials into the host's WebSocket endpoint
//     and authenticates with a connect secret; stdio = the host spawns the
//     plugin executable from spawn.command and talks JSON-RPC over its
//     stdin/stdout (the LSP/DAP model).
const (
	KindBuiltin  = "builtin"
	KindExternal = "external"

	TransportSocket = "socket"
	TransportStdio  = "stdio"
)

var pluginIDRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Manifest struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	ABIVersion string `json:"abi_version"`
	// Kind is builtin or external. Required.
	Kind string `json:"kind"`
	// Transport is socket or stdio, meaningful only for kind=external.
	// Empty for builtin.
	Transport   string       `json:"transport,omitempty"`
	Spawn       *SpawnConfig `json:"spawn"`
	Contributes Contributes  `json:"contributes"`
	Needs       []string     `json:"needs"`
}

type SpawnConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type Contributes struct {
	Capabilities []string `json:"capabilities"`
	Subscribes   []string `json:"subscribes"`
	Publishes    []string `json:"publishes"`
}

func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse plugin manifest %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate plugin manifest %s: %w", path, err)
	}
	return m, nil
}

func (m Manifest) Validate() error {
	if !pluginIDRE.MatchString(m.ID) {
		return fmt.Errorf("id %q must match %s", m.ID, pluginIDRE.String())
	}
	if m.ABIVersion == "" {
		return fmt.Errorf("abi_version is required")
	}
	switch m.Kind {
	case KindBuiltin:
		if m.Transport != "" {
			return fmt.Errorf("transport must be empty for kind=builtin (got %q)", m.Transport)
		}
		if m.Spawn != nil {
			return fmt.Errorf("spawn must be null for kind=builtin")
		}
	case KindExternal:
		switch m.Transport {
		case TransportSocket:
			if m.Spawn != nil {
				return fmt.Errorf("spawn must be null for transport=socket")
			}
		case TransportStdio:
			if m.Spawn == nil || m.Spawn.Command == "" {
				return fmt.Errorf("spawn.command is required for transport=stdio")
			}
		default:
			return fmt.Errorf("unsupported transport %q for kind=external", m.Transport)
		}
	default:
		return fmt.Errorf("unsupported kind %q", m.Kind)
	}
	return nil
}
