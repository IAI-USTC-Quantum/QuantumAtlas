package plugin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/events"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/hostapi"
	"golang.org/x/net/websocket"
)

func TestRPCInitializeAndHostCall(t *testing.T) {
	dir := t.TempDir()
	writeRPCManifest(t, dir, "lean", `{
	  "id": "lean",
	  "name": "Lean",
	  "version": "1.0.0",
	  "abi_version": "1",
	  "kind": "external",
	  "transport": "socket",
	  "spawn": null,
	  "contributes": {"capabilities": ["lean.verify"], "subscribes": [], "publishes": []},
	  "needs": ["papers:read"]
	}`)
	registry, err := LoadDir(dir, Options{})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	host := hostapi.NewRegistry()
	if err := host.Register("pages/get", func(_ context.Context, params any) (any, error) {
		return map[string]any{"page_id": "p1"}, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	s := httptest.NewServer((&RPCServer{
		Registry:      registry,
		HostAPI:       host,
		Events:        events.NewBus(),
		ConnectSecret: "secret",
	}).Handler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer ws.Close()

	writeJSON(t, ws, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"id":          "lean",
			"secret":      "secret",
			"abi_version": "1",
		},
	})
	resp := readJSON(t, ws)
	if resp["error"] != nil {
		t.Fatalf("initialize error: %v", resp["error"])
	}
	if !registry.Available("lean") {
		t.Fatal("lean should be available after initialize")
	}

	writeJSON(t, ws, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "pages/get",
		"params":  map[string]any{"page_id": "p1"},
	})
	resp = readJSON(t, ws)
	if resp["error"] != nil {
		t.Fatalf("pages/get error: %v", resp["error"])
	}
}

func TestRPCEventsPublishAllowedWithoutPluginWriteScope(t *testing.T) {
	dir := t.TempDir()
	writeRPCManifest(t, dir, "lean", `{
	  "id": "lean",
	  "name": "Lean",
	  "version": "1.0.0",
	  "abi_version": "1",
	  "kind": "external",
	  "transport": "socket",
	  "spawn": null,
	  "contributes": {"capabilities": [], "subscribes": [], "publishes": ["lean.verification.completed"]},
	  "needs": ["verifications:write"]
	}`)
	registry, err := LoadDir(dir, Options{})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	host := hostapi.NewRegistry()
	bus := events.NewBus()
	if err := hostapi.RegisterCoreMethods(host, nil, bus); err != nil {
		t.Fatalf("RegisterCoreMethods: %v", err)
	}
	s := httptest.NewServer((&RPCServer{
		Registry:      registry,
		HostAPI:       host,
		Events:        bus,
		ConnectSecret: "secret",
	}).Handler())
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer ws.Close()
	writeJSON(t, ws, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"id": "lean", "secret": "secret", "abi_version": "1"},
	})
	if resp := readJSON(t, ws); resp["error"] != nil {
		t.Fatalf("initialize error: %v", resp["error"])
	}
	writeJSON(t, ws, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "events/publish",
		"params":  map[string]any{"type": "lean.verification.completed", "payload": map[string]any{"lemma_id": "lem-1"}},
	})
	if resp := readJSON(t, ws); resp["error"] != nil {
		t.Fatalf("events/publish error: %v", resp["error"])
	}
}

func TestRPCPushesSubscribedEvents(t *testing.T) {
	dir := t.TempDir()
	writeRPCManifest(t, dir, "lean", `{
	  "id": "lean",
	  "name": "Lean",
	  "version": "1.0.0",
	  "abi_version": "1",
	  "kind": "external",
	  "transport": "socket",
	  "spawn": null,
	  "contributes": {"capabilities": [], "subscribes": ["lean.added"], "publishes": []},
	  "needs": ["papers:read"]
	}`)
	registry, err := LoadDir(dir, Options{})
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	bus := events.NewBus()
	s := httptest.NewServer((&RPCServer{
		Registry:      registry,
		HostAPI:       hostapi.NewRegistry(),
		Events:        bus,
		ConnectSecret: "secret",
	}).Handler())
	defer s.Close()
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer ws.Close()
	writeJSON(t, ws, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"id": "lean", "secret": "secret", "abi_version": "1"},
	})
	if resp := readJSON(t, ws); resp["error"] != nil {
		t.Fatalf("initialize error: %v", resp["error"])
	}
	bus.Publish(context.Background(), events.Event{
		ID:   "evt-1",
		Type: "lean.added",
		Time: time.Now().UTC(),
		Payload: map[string]any{
			"lemma_id": "lem-1",
		},
	})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg := readJSON(t, ws)
	if msg["method"] != "event" {
		t.Fatalf("notification method = %v, want event", msg["method"])
	}
}

func writeJSON(t *testing.T, ws *websocket.Conn, value any) {
	t.Helper()
	if err := json.NewEncoder(ws).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func readJSON(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(ws).Decode(&out); err != nil {
		t.Fatalf("read json: %v", err)
	}
	return out
}

func writeRPCManifest(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
