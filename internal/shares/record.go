// Package shares persists share-link metadata as JSON files on disk and
// resolves share-relative paths to on-disk targets.
//
// Direct port of atlas/server/tasks.py:ShareStore + share path helpers
// from atlas/server/routers/shares.py. On-disk format is byte-compatible
// so a Python server and Go server pointed at the same DATA_DIR/shares/
// directory can read each other's tokens.
package shares

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record is the persisted share-link record. Field tags use the
// snake_case names the Python pydantic model emits so JSON written by
// either implementation can be re-read by the other.
type Record struct {
	Token     string   `json:"token"`
	Paths     []string `json:"paths"`
	CreatedBy string   `json:"created_by,omitempty"`
	CreatedAt string   `json:"created_at"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Label     string   `json:"label,omitempty"`
}

// Store is a thin {base_dir}/{token}.json key-value store with atomic
// writes (tmp + rename). Concurrent access is safe via an in-process
// mutex; we don't try to coordinate across processes because the Python
// server is being decommissioned and won't run alongside the Go one.
type Store struct {
	BaseDir string
	mu      sync.Mutex
}

// NewStore initializes the on-disk directory and returns a store handle.
func NewStore(baseDir string) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("shares: mkdir %s: %w", baseDir, err)
	}
	return &Store{BaseDir: baseDir}, nil
}

// path is the file path for a token.
func (s *Store) path(token string) string {
	return filepath.Join(s.BaseDir, token+".json")
}

// Save atomically persists rec to disk.
func (s *Store) Save(rec *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	target := s.path(rec.Token)
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("shares: marshal: %w", err)
	}
	return atomicWriteFile(target, payload, 0o644)
}

// Get returns the record for token, or (nil, nil) when no such token
// exists. Corrupt files surface as an error.
func (s *Store) Get(token string) (*Record, error) {
	if token == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.path(token))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("shares: parse %s: %w", token, err)
	}
	return &rec, nil
}

// Delete removes a token. Returns (false, nil) when nothing to delete.
func (s *Store) Delete(token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.path(token)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err := os.Remove(p); err != nil {
		return false, err
	}
	return true, nil
}

// ListAll returns every record in the store, sorted by created_at DESC.
// Corrupt files are skipped silently (matches Python warn-and-continue).
func (s *Store) ListAll() ([]*Record, error) {
	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.BaseDir, e.Name()))
		if err != nil {
			continue
		}
		var rec Record
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		out = append(out, &rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// IsExpired reports whether rec.ExpiresAt is in the past.
func (rec *Record) IsExpired() bool {
	if rec.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, rec.ExpiresAt)
	if err != nil {
		// Python tolerates "...Z" — try parsing again.
		t, err = time.Parse(time.RFC3339Nano, rec.ExpiresAt)
		if err != nil {
			return false
		}
	}
	return time.Now().UTC().After(t)
}

// atomicWriteFile writes data to a sibling .tmp file then renames.
// On POSIX this guarantees readers never see a partial write.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// CopyFile is a helper for tests / scripts.
func CopyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
