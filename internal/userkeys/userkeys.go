// userkeys: per-user storage for third-party search API keys.
//
// Users configure their own keys for key-requiring search backends
// (IEEE, Scopus, Tavily, ...) via the dashboard (PUT /api/me/search-keys);
// qatlasd decrypts them when proxying multi/agentic search to the
// qatlas-search microservice and injects them into the request's
// api_keys map — the microservice itself never persists them.
//
// Storage: one PocketBase record per (user, backend) in the
// search_api_keys collection (see migrations.go; owner-only access
// rules, writes only via these server-side handlers, exactly like
// pat_tokens). The key material is encrypted at rest with AES-256-GCM
// (PocketBase's security.Encrypt). The encryption key is derived from
// the server's system PAT token — always present in serve mode, never
// leaves the 0600 YAML config — so an exfiltrated pb_data alone does
// not leak the stored third-party keys. Rotating the system PAT
// invalidates stored keys; they surface as decryption failures and
// users simply re-enter them.
package userkeys

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// CollectionName is the PocketBase collection holding the encrypted keys.
const CollectionName = "search_api_keys"

// MaxKeyLen bounds a stored third-party key (validation shared by the
// routes layer and SetKey).
const MaxKeyLen = 512

// MaxBackendLen mirrors the backend column's TextField cap.
const MaxBackendLen = 40

// ErrDisabled is returned by every Store method when the feature is off
// (no server secret to derive the encryption key from).
var ErrDisabled = errors.New("user search API keys are disabled (no server secret available)")

// DeriveKey derives the 32-byte AES-256-GCM key material from a server
// secret. Domain-separated SHA-256, so the raw secret is never used as
// a key directly.
func DeriveKey(secret string) string {
	sum := sha256.Sum256([]byte("qatlas-userkeys-v1:" + secret))
	return string(sum[:])
}

// Store reads/writes encrypted per-user keys. A Store built without a
// secret is "disabled": Enabled() is false and every method returns
// ErrDisabled.
type Store struct {
	app core.App
	key string // 32-byte AES key material; empty = feature disabled
}

// NewStore builds a Store over app, deriving the encryption key from
// secret (the qatlasd system PAT token). Empty secret => disabled store.
func NewStore(app core.App, secret string) *Store {
	if app == nil || secret == "" {
		return &Store{}
	}
	return &Store{app: app, key: DeriveKey(secret)}
}

// Enabled reports whether key storage is usable.
func (s *Store) Enabled() bool { return s.app != nil && s.key != "" }

// KeyMeta is the listed shape: backend name, a display-only hint (last
// 4 chars) and the update timestamp. The key itself never leaves
// storage except on the proxy path (GetKeys).
type KeyMeta struct {
	Backend string `json:"backend"`
	Hint    string `json:"hint"`
	Updated string `json:"updated_at"`
}

// SetKey upserts (userID, backend) = plaintext, encrypted at rest.
func (s *Store) SetKey(userID, backend, plaintext string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return errors.New("empty API key")
	}
	if len(plaintext) > MaxKeyLen {
		return fmt.Errorf("API key longer than %d characters", MaxKeyLen)
	}
	enc, err := security.Encrypt([]byte(plaintext), s.key)
	if err != nil {
		return fmt.Errorf("encrypt key: %w", err)
	}
	rec, err := s.find(userID, backend)
	if err != nil {
		return err
	}
	if rec == nil {
		collection, err := s.app.FindCollectionByNameOrId(CollectionName)
		if err != nil {
			return fmt.Errorf("collection lookup: %w", err)
		}
		rec = core.NewRecord(collection)
		rec.Set("user", userID)
		rec.Set("backend", backend)
	}
	rec.Set("key_encrypted", enc)
	if err := s.app.Save(rec); err != nil {
		return fmt.Errorf("save key record: %w", err)
	}
	return nil
}

// GetKeys decrypts and returns all of userID's keys (backend -> key).
// A record that fails to decrypt (e.g. the system PAT was rotated) is
// skipped with a warning rather than failing the whole search.
func (s *Store) GetKeys(userID string) (map[string]string, error) {
	out := map[string]string{}
	if !s.Enabled() {
		return out, ErrDisabled
	}
	records, err := s.app.FindRecordsByFilter(
		CollectionName,
		"user = {:user}",
		"backend",
		0,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return out, fmt.Errorf("list key records: %w", err)
	}
	for _, rec := range records {
		plain, err := security.Decrypt(rec.GetString("key_encrypted"), s.key)
		if err != nil {
			slog.Warn("userkeys: decrypt failed (stale key material?) — skipping",
				"user_id", userID, "backend", rec.GetString("backend"))
			continue
		}
		out[rec.GetString("backend")] = string(plain)
	}
	return out, nil
}

// ListMeta returns the caller's key inventory without full key material.
func (s *Store) ListMeta(userID string) ([]KeyMeta, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	keys, err := s.GetKeys(userID)
	if err != nil {
		return nil, err
	}
	records, err := s.app.FindRecordsByFilter(
		CollectionName,
		"user = {:user}",
		"backend",
		0,
		0,
		dbx.Params{"user": userID},
	)
	if err != nil {
		return nil, fmt.Errorf("list key records: %w", err)
	}
	hints := map[string]string{}
	for _, rec := range records {
		backend := rec.GetString("backend")
		plain, ok := keys[backend]
		if !ok {
			hints[backend] = "••••" // undecryptable (stale) — still listed
			continue
		}
		hints[backend] = maskHint(plain)
	}
	out := make([]KeyMeta, 0, len(records))
	for _, rec := range records {
		backend := rec.GetString("backend")
		out = append(out, KeyMeta{
			Backend: backend,
			Hint:    hints[backend],
			Updated: rec.GetString("updated"),
		})
	}
	return out, nil
}

// DeleteKey removes userID's key for backend. Returns found=false when
// no such record existed (the caller answers an opaque 404).
func (s *Store) DeleteKey(userID, backend string) (found bool, err error) {
	if !s.Enabled() {
		return false, ErrDisabled
	}
	rec, err := s.find(userID, backend)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, nil
	}
	if err := s.app.Delete(rec); err != nil {
		return true, fmt.Errorf("delete key record: %w", err)
	}
	return true, nil
}

// find returns the (user, backend) record or nil.
func (s *Store) find(userID, backend string) (*core.Record, error) {
	records, err := s.app.FindRecordsByFilter(
		CollectionName,
		"user = {:user} && backend = {:backend}",
		"",
		1,
		0,
		dbx.Params{"user": userID, "backend": backend},
	)
	if err != nil {
		return nil, fmt.Errorf("lookup key record: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0], nil
}

// maskHint keeps the last up-to-4 characters for display ("••••ab3f").
func maskHint(plain string) string {
	if len(plain) <= 4 {
		return "••••"
	}
	return "••••" + plain[len(plain)-4:]
}
