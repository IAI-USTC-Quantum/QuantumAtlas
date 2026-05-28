package main

import (
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// Why these tests live here:
//
// planPruneCandidates is the policy gate for `storage prune`. It runs
// in-memory on a slice of ObjectVersion entries, no S3 dependency — so
// we can unit-test all the keep/drop edge cases without standing up a
// bucket. The S3 round-trip is covered by the integration tests under
// internal/objstore/s3_test.go (//go:build integration).

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tt
}

func TestParseDurationExt(t *testing.T) {
	cases := []struct {
		in    string
		want  time.Duration
		isErr bool
	}{
		{"30s", 30 * time.Second, false},
		{"15m", 15 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"", 0, true},
		{"3banana", 0, true},
		{"30q", 0, true}, // unknown unit
	}
	for _, tc := range cases {
		got, err := parseDurationExt(tc.in)
		if tc.isErr {
			if err == nil {
				t.Errorf("parseDurationExt(%q): expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDurationExt(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDurationExt(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// helper: build a synthetic ObjectVersion.
func mkVersion(key, vid string, isLatest, isDM bool, modified string, size int64) objstore.ObjectVersion {
	return objstore.ObjectVersion{
		Key:            key,
		VersionID:      vid,
		IsLatest:       isLatest,
		IsDeleteMarker: isDM,
		LastModified:   mustParseTimeStr(modified),
		Size:           size,
	}
}

// mustParseTimeStr is a panic-on-error time.Parse for table tests.
func mustParseTimeStr(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Sanity: current versions never appear in the candidate list, no
// matter how old they are or what filters say. This is the load-
// bearing safety invariant of the whole command.
func TestPlanPruneCandidates_NeverDeletesLatest(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		mkVersion("pdf/24/a.pdf", "v3", true, false, "2025-01-01T00:00:00Z", 1000),  // ancient + latest → keep
		mkVersion("pdf/24/a.pdf", "v2", false, false, "2025-06-01T00:00:00Z", 1000), // ancient + noncurrent → drop
	}
	got := planPruneCandidates(versions, 1*time.Hour /* anything older than 1h */, 0, now)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d (%v)", len(got), got)
	}
	if got[0].VersionID != "v2" {
		t.Errorf("expected v2 (noncurrent) as the only candidate, got %s", got[0].VersionID)
	}
}

func TestPlanPruneCandidates_NoFilters_DropsAllNoncurrent(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		mkVersion("k", "v1", false, false, "2029-01-01T00:00:00Z", 100),
		mkVersion("k", "v2", false, false, "2029-06-01T00:00:00Z", 200),
		mkVersion("k", "v3", true, false, "2029-12-01T00:00:00Z", 300),
	}
	got := planPruneCandidates(versions, 0, 0, now)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates (v1+v2), got %d", len(got))
	}
}

// --older-than gate: even noncurrent versions newer than the cap stay.
func TestPlanPruneCandidates_OlderThanFiltersByAge(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		mkVersion("k", "vOld", false, false, "2029-09-01T00:00:00Z", 100), // 4mo old
		mkVersion("k", "vNew", false, false, "2029-12-25T00:00:00Z", 100), // 1w old
		mkVersion("k", "vLatest", true, false, "2030-01-01T00:00:00Z", 100),
	}
	// Cap at 30d → only vOld qualifies.
	got := planPruneCandidates(versions, 30*24*time.Hour, 0, now)
	if len(got) != 1 || got[0].VersionID != "vOld" {
		t.Fatalf("expected only vOld, got %v", got)
	}
}

// --keep-last gate: keep newest N noncurrent per key.
func TestPlanPruneCandidates_KeepLastPerKey(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		// Key A has 5 noncurrent versions, oldest first.
		mkVersion("A", "a1", false, false, "2025-01-01T00:00:00Z", 100),
		mkVersion("A", "a2", false, false, "2026-01-01T00:00:00Z", 100),
		mkVersion("A", "a3", false, false, "2027-01-01T00:00:00Z", 100),
		mkVersion("A", "a4", false, false, "2028-01-01T00:00:00Z", 100),
		mkVersion("A", "a5", false, false, "2029-01-01T00:00:00Z", 100),
		mkVersion("A", "aCur", true, false, "2030-01-01T00:00:00Z", 100),
		// Key B has 1 noncurrent — keep-last=2 means it stays
		mkVersion("B", "b1", false, false, "2029-01-01T00:00:00Z", 100),
		mkVersion("B", "bCur", true, false, "2030-01-01T00:00:00Z", 100),
	}
	got := planPruneCandidates(versions, 0, 2, now)

	// A: keep 2 newest noncurrent (a5, a4) → drop a3, a2, a1
	// B: only 1 noncurrent, < keep-last → keep all
	wantIDs := map[string]bool{"a1": true, "a2": true, "a3": true}
	if len(got) != len(wantIDs) {
		t.Fatalf("expected %d candidates, got %d (%v)", len(wantIDs), len(got), got)
	}
	for _, c := range got {
		if !wantIDs[c.VersionID] {
			t.Errorf("unexpected candidate %s; want only %v", c.VersionID, wantIDs)
		}
	}
}

// Filters compose: --older-than gates first, --keep-last gates the
// remainder per key.
func TestPlanPruneCandidates_OlderThanAndKeepLastCompose(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		// All 5 noncurrent are old enough to qualify under
		// --older-than 30d, but --keep-last 2 reserves the 2
		// newest. So we should drop a1, a2, a3.
		mkVersion("A", "a1", false, false, "2025-01-01T00:00:00Z", 100),
		mkVersion("A", "a2", false, false, "2026-01-01T00:00:00Z", 100),
		mkVersion("A", "a3", false, false, "2027-01-01T00:00:00Z", 100),
		mkVersion("A", "a4", false, false, "2028-01-01T00:00:00Z", 100),
		mkVersion("A", "a5", false, false, "2029-01-01T00:00:00Z", 100),
		mkVersion("A", "aCur", true, false, "2030-01-01T00:00:00Z", 100),
	}
	got := planPruneCandidates(versions, 30*24*time.Hour, 2, now)
	wantIDs := map[string]bool{"a1": true, "a2": true, "a3": true}
	if len(got) != len(wantIDs) {
		t.Fatalf("expected %d candidates, got %d (%v)", len(wantIDs), len(got), got)
	}
	for _, c := range got {
		if !wantIDs[c.VersionID] {
			t.Errorf("unexpected candidate %s; want only %v", c.VersionID, wantIDs)
		}
	}
}

// Output sort: candidates are key-asc then LastModified-desc, so a
// human scanning the dry-run list sees per-key blocks newest-first.
func TestPlanPruneCandidates_OutputIsSorted(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		mkVersion("B", "b1", false, false, "2025-01-01T00:00:00Z", 100),
		mkVersion("A", "a2", false, false, "2025-06-01T00:00:00Z", 100),
		mkVersion("A", "a1", false, false, "2025-01-01T00:00:00Z", 100),
		mkVersion("A", "aCur", true, false, "2029-01-01T00:00:00Z", 100),
	}
	got := planPruneCandidates(versions, 0, 0, now)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// Expect: A/a2 (newer), A/a1 (older), B/b1
	if got[0].Key != "A" || got[0].VersionID != "a2" {
		t.Errorf("expected A/a2 first, got %s/%s", got[0].Key, got[0].VersionID)
	}
	if got[1].Key != "A" || got[1].VersionID != "a1" {
		t.Errorf("expected A/a1 second, got %s/%s", got[1].Key, got[1].VersionID)
	}
	if got[2].Key != "B" || got[2].VersionID != "b1" {
		t.Errorf("expected B/b1 third, got %s/%s", got[2].Key, got[2].VersionID)
	}
}

// Delete markers that are *latest* (i.e. the object's current top
// entry is a soft-delete tombstone) are NOT pruned — they're load-
// bearing for the object's deleted state. Only delete markers buried
// under newer versions count.
func TestPlanPruneCandidates_LatestDeleteMarkerNeverDropped(t *testing.T) {
	now := mustParseTimeStr("2030-01-01T00:00:00Z")
	versions := []objstore.ObjectVersion{
		mkVersion("k", "dm", true, true, "2029-01-01T00:00:00Z", 0), // current DM
		mkVersion("k", "v1", false, false, "2028-01-01T00:00:00Z", 100),
	}
	got := planPruneCandidates(versions, 0, 0, now)
	if len(got) != 1 || got[0].VersionID != "v1" {
		t.Fatalf("expected only v1 (noncurrent), got %v", got)
	}
}
