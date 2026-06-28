package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

const HostABIVersion = "1"

const (
	TransportInProcessGo  = "in-process-go"
	TransportJSONRPCWS    = "jsonrpc-ws"
	TransportJSONRPCStdio = "jsonrpc-stdio"
)

var pluginIDRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Manifest struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	ABIVersion  string       `json:"abi_version"`
	Transport   string       `json:"transport"`
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
	switch m.Transport {
	case TransportInProcessGo, TransportJSONRPCWS:
		if m.Spawn != nil {
			return fmt.Errorf("spawn must be null for transport %s", m.Transport)
		}
	case TransportJSONRPCStdio:
		if m.Spawn == nil || m.Spawn.Command == "" {
			return fmt.Errorf("spawn.command is required for transport %s", m.Transport)
		}
	default:
		return fmt.Errorf("unsupported transport %q", m.Transport)
	}
	return nil
}
