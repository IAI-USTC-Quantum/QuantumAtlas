package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/events"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/hostapi"
	"golang.org/x/net/websocket"
)

type RPCServer struct {
	Registry      *Registry
	HostAPI       *hostapi.Registry
	Events        *events.Bus
	ConnectSecret string
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type initializeParams struct {
	ID         string `json:"id"`
	Secret     string `json:"secret"`
	ABIVersion string `json:"abi_version"`
}

func (s *RPCServer) Handler() http.Handler {
	return websocket.Handler(s.handleConn)
}

func (s *RPCServer) handleConn(ws *websocket.Conn) {
	defer ws.Close()
	dec := json.NewDecoder(ws)
	enc := json.NewEncoder(ws)
	var writeMu sync.Mutex
	write := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(v)
	}

	var pluginID string
	var granted []string
	var unsubscribes []func()
	defer func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
		if pluginID != "" && s.Registry != nil {
			s.Registry.MarkDisconnected(pluginID)
		}
	}()

	for {
		var msg rpcMessage
		if err := dec.Decode(&msg); err != nil {
			if err != io.EOF {
				slog.Warn("plugin rpc: decode failed", "plugin", pluginID, "error", err)
			}
			return
		}
		id := rawID(msg.ID)
		if pluginID == "" {
			if msg.Method != "initialize" {
				_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32001, Message: "initialize required before other methods"}})
				return
			}
			var params initializeParams
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32602, Message: "invalid initialize params: " + err.Error()}})
				return
			}
			manifest, err := s.authenticate(params)
			if err != nil {
				_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32002, Message: err.Error()}})
				return
			}
			pluginID = manifest.ID
			granted = append([]string(nil), manifest.Needs...)
			if s.Registry != nil {
				s.Registry.MarkConnected(pluginID)
			}
			for _, eventType := range manifest.Contributes.Subscribes {
				unsubscribes = append(unsubscribes, s.subscribeConnection(eventType, ws, write))
			}
			_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
				"host_abi_version":  HostABIVersion,
				"granted_scopes":    granted,
				"connection_token":  fmt.Sprintf("conn-%d", time.Now().UnixNano()),
				"host_capabilities": []string{"papers", "wiki", "theorems", "verifications", "events"},
			}})
			continue
		}
		if msg.ID == nil {
			continue
		}
		if err := requireScope(msg.Method, granted); err != nil {
			_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32003, Message: err.Error()}})
			continue
		}
		var params any
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}})
				continue
			}
		}
		result, err := s.HostAPI.Call(context.Background(), msg.Method, params)
		if err != nil {
			_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32000, Message: err.Error()}})
			continue
		}
		_ = write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
	}
}

func (s *RPCServer) subscribeConnection(eventType string, ws *websocket.Conn, write func(any) error) func() {
	if s.Events == nil {
		return func() {}
	}
	ch := make(chan events.Event, 64)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case ev := <-ch:
				_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "event",
					"params":  ev,
				}); err != nil {
					slog.Warn("plugin rpc: event push failed", "event_type", eventType, "error", err)
					return
				}
				_ = ws.SetWriteDeadline(time.Time{})
			}
		}
	}()
	unsubscribe := s.Events.Subscribe(eventType, func(ctx context.Context, ev events.Event) error {
		select {
		case <-done:
			return nil
		case ch <- ev:
		default:
			slog.Warn("plugin rpc: event queue full; dropping event", "event_type", eventType, "event_id", ev.ID)
		}
		return nil
	})
	return func() {
		unsubscribe()
		close(done)
	}
}

func (s *RPCServer) authenticate(params initializeParams) (Manifest, error) {
	if params.ID == "" {
		return Manifest{}, fmt.Errorf("plugin id is required")
	}
	if s.ConnectSecret != "" && params.Secret != s.ConnectSecret {
		return Manifest{}, fmt.Errorf("invalid connect secret")
	}
	if s.Registry == nil {
		return Manifest{}, fmt.Errorf("plugin registry is not configured")
	}
	manifest, ok := s.Registry.Manifest(params.ID)
	if !ok {
		return Manifest{}, fmt.Errorf("plugin %q is not configured", params.ID)
	}
	if manifest.ABIVersion != HostABIVersion || params.ABIVersion != HostABIVersion {
		return Manifest{}, fmt.Errorf("plugin ABI is incompatible with host ABI %s", HostABIVersion)
	}
	if !s.Registry.Available(params.ID) {
		return Manifest{}, fmt.Errorf("plugin %q is disabled", params.ID)
	}
	if manifest.Transport != TransportJSONRPCWS {
		return Manifest{}, fmt.Errorf("plugin %q does not use jsonrpc-ws transport", params.ID)
	}
	return manifest, nil
}

func rawID(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func requireScope(method string, granted []string) error {
	required := ""
	switch method {
	case "papers/getMarkdown", "papers/getMeta", "papers/getCitedRefs":
		required = "papers:read"
	case "pages/get", "search/query":
		required = "wiki:read"
	case "theorems/get":
		required = "theorems:read"
	case "theorems/create":
		required = "theorems:write"
	case "verifications/submit":
		required = "verifications:write"
	case "events/publish":
		return nil
	default:
		return nil
	}
	for _, scope := range granted {
		if scope == required {
			return nil
		}
	}
	return fmt.Errorf("method %s requires %s", method, required)
}
