// Package agentic implements the local backend for the metered
// POST /api/search/agentic endpoint: the engine fan-out's raw hits land
// in a per-request sandbox directory and the local claude CLI (headless
// `claude -p`, JSON output) refines them into the standardized
// search.RemoteResponse shape — the same contract the remote
// qatlas-search microservice fulfils, so the route layer can switch
// backends by configuration alone.
//
// The sandbox is filesystem-level isolation only (claude runs with
// cwd=sandbox/run and a restricted tool allowlist), not a container;
// directories are kept for audit and reaped by the janitor after the
// configured retention.
package agentic

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Sandbox is one request's working directory under the shared root.
// Dir is named <20060102T150405>-<8 hex chars of crypto/rand> (UTC) —
// auditable and collision-safe, same nonce convention as stagedupload.
type Sandbox struct {
	Root string
	Dir  string
}

// NewSandbox creates a fresh sandbox directory under root (created when
// missing).
func NewSandbox(root string) (*Sandbox, error) {
	var nonce [4]byte
	if _, err := crand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("agentic sandbox nonce: %w", err)
	}
	name := time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(nonce[:])
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("agentic sandbox mkdir: %w", err)
	}
	return &Sandbox{Root: root, Dir: dir}, nil
}

// WriteJSON writes v as indented JSON to <Dir>/<name>, atomically
// (.part + rename, same scheme as objstore.LocalStore).
func (s *Sandbox) WriteJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(name, append(data, '\n'))
}

// WriteText writes raw text (the rendered prompt) to <Dir>/<name>.
func (s *Sandbox) WriteText(name, text string) error {
	return s.writeFile(name, []byte(text))
}

func (s *Sandbox) writeFile(name string, data []byte) error {
	part := filepath.Join(s.Dir, name+".part")
	if err := os.WriteFile(part, data, 0o644); err != nil {
		return fmt.Errorf("agentic sandbox write %s: %w", name, err)
	}
	if err := os.Rename(part, filepath.Join(s.Dir, name)); err != nil {
		return fmt.Errorf("agentic sandbox commit %s: %w", name, err)
	}
	return nil
}

// RunDir returns (creating on first call) the sandbox's run/
// subdirectory — the claude CLI's working directory and only writable
// area.
func (s *Sandbox) RunDir() (string, error) {
	dir := filepath.Join(s.Dir, "run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("agentic sandbox run dir: %w", err)
	}
	return dir, nil
}

// Cleanup removes the whole sandbox directory. Sandboxes are normally
// kept for audit and reaped by the janitor; Cleanup exists for callers
// that deliberately discard one.
func (s *Sandbox) Cleanup() error {
	return os.RemoveAll(s.Dir)
}
