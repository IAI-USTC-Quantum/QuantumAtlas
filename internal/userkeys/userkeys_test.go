package userkeys

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

// newTestStore builds an enabled Store over a fresh PB test app (the
// package's migration creates the collection during NewTestApp). Also
// returns the seeded test user's id (relation targets must exist).
func newTestStore(t testing.TB, secret string) (*Store, string) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	if _, err := app.FindCollectionByNameOrId(CollectionName); err != nil {
		t.Fatalf("collection missing after migrations: %v", err)
	}
	user, err := app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatalf("seed user lookup: %v", err)
	}
	return NewStore(app, secret), user.Id
}

func TestStore_RoundTrip(t *testing.T) {
	s, uid := newTestStore(t, "a-system-pat-secret")
	if !s.Enabled() {
		t.Fatal("store should be enabled with a secret")
	}

	if err := s.SetKey(uid, "ieee", "ieee-key-12345678"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	// Upsert the same (user, backend).
	if err := s.SetKey(uid, "ieee", "ieee-key-87654321"); err != nil {
		t.Fatalf("SetKey upsert: %v", err)
	}
	if err := s.SetKey(uid, "tavily", "tvly-xyz"); err != nil {
		t.Fatalf("SetKey second backend: %v", err)
	}

	keys, err := s.GetKeys(uid)
	if err != nil {
		t.Fatalf("GetKeys: %v", err)
	}
	if len(keys) != 2 || keys["ieee"] != "ieee-key-87654321" || keys["tavily"] != "tvly-xyz" {
		t.Fatalf("keys = %v", keys)
	}

	meta, err := s.ListMeta(uid)
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(meta) != 2 {
		t.Fatalf("meta len = %d, want 2 (%v)", len(meta), meta)
	}
	byBackend := map[string]KeyMeta{}
	for _, m := range meta {
		byBackend[m.Backend] = m
	}
	if hint := byBackend["ieee"].Hint; hint != "••••4321" {
		t.Errorf("ieee hint = %q, want masked last-4", hint)
	}
	if byBackend["ieee"].Updated == "" {
		t.Error("updated_at should be set")
	}

	// Ciphertext at rest must not contain the plaintext.
	rec, err := s.find(uid, "ieee")
	if err != nil || rec == nil {
		t.Fatalf("find: %v %v", rec, err)
	}
	if strings.Contains(rec.GetString("key_encrypted"), "ieee-key") {
		t.Error("ciphertext contains plaintext")
	}

	found, err := s.DeleteKey(uid, "ieee")
	if err != nil || !found {
		t.Fatalf("DeleteKey: found=%v err=%v", found, err)
	}
	keys, _ = s.GetKeys(uid)
	if _, ok := keys["ieee"]; ok {
		t.Error("ieee key should be gone")
	}

	found, err = s.DeleteKey(uid, "ieee")
	if err != nil || found {
		t.Fatalf("second delete: found=%v err=%v (want false, nil)", found, err)
	}
}

func TestStore_Disabled(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	s := NewStore(app, "")
	if s.Enabled() {
		t.Fatal("empty secret must disable the store")
	}
	if err := s.SetKey("u", "ieee", "k"); err != ErrDisabled {
		t.Fatalf("SetKey err = %v, want ErrDisabled", err)
	}
	if _, err := s.GetKeys("u"); err != ErrDisabled {
		t.Fatalf("GetKeys err = %v, want ErrDisabled", err)
	}
	if _, err := s.DeleteKey("u", "ieee"); err != ErrDisabled {
		t.Fatalf("DeleteKey err = %v, want ErrDisabled", err)
	}
	if _, err := s.ListMeta("u"); err != ErrDisabled {
		t.Fatalf("ListMeta err = %v, want ErrDisabled", err)
	}
}

func TestStore_SetKeyValidation(t *testing.T) {
	s, _ := newTestStore(t, "secret")
	if err := s.SetKey("u", "ieee", "   "); err == nil {
		t.Error("blank key must be rejected")
	}
	if err := s.SetKey("u", "ieee", strings.Repeat("k", MaxKeyLen+1)); err == nil {
		t.Error("over-long key must be rejected")
	}
}

func TestStore_RotatedSecretMakesStaleKeysUndecryptable(t *testing.T) {
	s1, uid := newTestStore(t, "secret-one")
	if err := s1.SetKey(uid, "ieee", "ieee-key-12345678"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	// Same pb_data, different secret: the record cannot be decrypted and
	// must be skipped (not surfaced as a search failure).
	s2 := NewStore(s1.app, "secret-two")
	keys, err := s2.GetKeys(uid)
	if err != nil {
		t.Fatalf("GetKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("stale key should be skipped, got %v", keys)
	}
	// ListMeta still lists it (with the opaque hint) so the UI can prompt.
	meta, err := s2.ListMeta(uid)
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(meta) != 1 || meta[0].Backend != "ieee" || meta[0].Hint != "••••" {
		t.Fatalf("meta = %v", meta)
	}
}

func TestDeriveKey_DomainSeparated(t *testing.T) {
	if DeriveKey("s") == DeriveKey("other") {
		t.Error("different secrets must derive different keys")
	}
	if len(DeriveKey("s")) != 32 {
		t.Fatalf("derived key must be 32 bytes, got %d", len(DeriveKey("s")))
	}
}
