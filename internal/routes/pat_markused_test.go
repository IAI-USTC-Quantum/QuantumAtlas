// MarkUsed integration test. Lives in the routes test package (rather
// than internal/pat) so it can use the PocketBase tests harness and
// drive a realistic record-creation → MarkUsed → re-read flow without
// re-implementing the migrations setup.
//
// The unit test in internal/pat/pat_test.go already covers the
// in-process behaviour (nil-record guard, etc.); this scenario covers
// the SQL UPDATE path against a live PocketBase DB.

package routes

import (
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
)

// mintForMarkUsedTest is a small helper that runs through the same
// /api/pat surface to land a fresh PAT record, then returns its id +
// the live record handle. Mirrors what real auth traffic looks like.
func mintForMarkUsedTest(t *testing.T, h *patHarness, name string) (string, *core.Record) {
	t.Helper()
	tok := h.sessionToken()
	status, _, body := h.do("POST", "/api/pat",
		`{"name":"`+name+`","scopes":["papers:write"],"expires_in_days":30}`,
		rawHeader(tok),
	)
	if status != 200 {
		t.Fatalf("mint: status=%d body=%v", status, body)
	}
	id := asString(body["id"])
	rec, err := h.app.FindRecordById(pat.CollectionName, id)
	if err != nil {
		t.Fatalf("find record %s: %v", id, err)
	}
	return id, rec
}

func TestPATMarkUsed_PersistsTimestampWithoutBumpingUpdated(t *testing.T) {
	h := newPATHarness(t)
	id, rec := mintForMarkUsedTest(t, h, "mu")

	if !rec.GetDateTime("last_used_at").Time().IsZero() {
		t.Fatal("freshly minted PAT shouldn't have last_used_at set")
	}
	updatedBefore := rec.GetDateTime("updated").Time()

	// Sleep a hair so any race in DateTime serialization shows up.
	time.Sleep(20 * time.Millisecond)

	if err := pat.MarkUsed(h.app, rec); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}

	rec2, err := h.app.FindRecordById(pat.CollectionName, id)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	lu := rec2.GetDateTime("last_used_at").Time()
	if lu.IsZero() {
		t.Error("last_used_at not persisted by MarkUsed")
	}
	if time.Since(lu) > 5*time.Second {
		t.Errorf("last_used_at = %v, expected recent now", lu)
	}

	// The key property: `updated` should NOT change. MarkUsed is
	// audit bookkeeping, not a record edit; bumping `updated` would
	// jitter every list query that sorts by it.
	updatedAfter := rec2.GetDateTime("updated").Time()
	if !updatedAfter.Equal(updatedBefore) {
		t.Errorf("MarkUsed unexpectedly bumped `updated`: %v -> %v", updatedBefore, updatedAfter)
	}

	// And no other column should have changed. Spot-check name / scopes
	// since those are the ones a buggy "Save the whole row" path would
	// most likely corrupt.
	if rec2.GetString("name") != "mu" {
		t.Errorf("name corrupted: %q", rec2.GetString("name"))
	}
	if !strings.Contains(rec2.GetString("scopes"), "papers:write") {
		t.Errorf("scopes corrupted: %q", rec2.GetString("scopes"))
	}
}

// TestPATMarkUsed_NilRecordReturnsError pins the defensive guard so a
// future refactor doesn't accidentally make MarkUsed(nil) panic.
func TestPATMarkUsed_NilRecordReturnsError(t *testing.T) {
	h := newPATHarness(t)
	if err := pat.MarkUsed(h.app, nil); err == nil {
		t.Error("MarkUsed(nil) should return error")
	}
}

// TestPATMarkUsed_ConcurrentCallsDoNotRace exercises the contention
// case: many concurrent uses of the same PAT should each succeed
// (single-column UPDATE is atomic) without producing a DB error or
// leaving last_used_at unset.
//
// The old app.Save() path would have race-y `updated` jitter under
// this load; the new UPDATE path collapses concurrent writes onto
// the per-row SQLite lock cleanly.
func TestPATMarkUsed_ConcurrentCallsDoNotRace(t *testing.T) {
	h := newPATHarness(t)
	id, rec := mintForMarkUsedTest(t, h, "concurrent")

	// Fire N concurrent MarkUsed calls.
	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- pat.MarkUsed(h.app, rec) }()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent MarkUsed: %v", err)
		}
	}

	// Verify final state: last_used_at populated.
	final, err := h.app.FindRecordById(pat.CollectionName, id)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if final.GetDateTime("last_used_at").Time().IsZero() {
		t.Error("after 20 concurrent MarkUsed, last_used_at still zero")
	}
}

