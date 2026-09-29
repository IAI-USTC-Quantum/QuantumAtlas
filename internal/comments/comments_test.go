package comments

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidType(t *testing.T) {
	for _, ok := range []string{"normal", "transcription_error", "typo_in_original", "lean:note", "agent-v2", "x"} {
		if !ValidType(ok) {
			t.Errorf("ValidType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "UPPER", "with space", " unicode✓", "a-very-long-type-slug-that-exceeds-sixty-four-characters-limit-1234567890"} {
		if ValidType(bad) {
			t.Errorf("ValidType(%q) = true, want false", bad)
		}
	}
}

func TestValidStatusAndScope(t *testing.T) {
	for _, s := range []string{"", StatusPending, StatusConfirmed, StatusRetracted} {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false, want true", s)
		}
	}
	if ValidStatus("closed") {
		t.Error(`ValidStatus("closed") = true, want false`)
	}
	if !ValidScope(ScopePublic) || !ValidScope(ScopeLean) || ValidScope("secret") {
		t.Error("ValidScope broken")
	}
}

// TestFixtureValidatorGoldenAnchors re-derives the §12.5 golden-anchor
// cases against the Q0 synthetic parse fixtures: hits must hit, misses
// must miss (no nearest-block fallback), missing pages must miss, and
// unknown revisions must miss.
func TestFixtureValidatorGoldenAnchors(t *testing.T) {
	dir := filepath.Join("..", "..", "tests", "fixtures", "blockcomments")
	load := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return raw
	}
	v, err := NewFixtureValidator(map[string][]byte{
		"rev-fixture-a": load("parse-a.middle.json"),
		"rev-fixture-b": load("parse-b.middle.json"),
	})
	if err != nil {
		t.Fatalf("NewFixtureValidator: %v", err)
	}

	// parse-A page 1 block 2 (equation E = m c^2) exists.
	if err := v.ValidateBlock("qa_x", "rev-fixture-a", 0, 2); err != nil {
		t.Errorf("a/0/2: unexpected error %v", err)
	}
	// parse-A page 2 block 2 exists.
	if err := v.ValidateBlock("qa_x", "rev-fixture-a", 1, 2); err != nil {
		t.Errorf("a/1/2: unexpected error %v", err)
	}
	// Non-contiguous indexes: 3/4 do NOT exist on page 1 — must miss.
	for _, idx := range []int{3, 4} {
		if err := v.ValidateBlock("qa_x", "rev-fixture-a", 0, idx); !errors.Is(err, ErrAnchorNotFound) {
			t.Errorf("a/0/%d: err=%v, want ErrAnchorNotFound", idx, err)
		}
	}
	// parse-B lacks page 2 entirely — any block must miss.
	if err := v.ValidateBlock("qa_x", "rev-fixture-b", 1, 1); !errors.Is(err, ErrAnchorNotFound) {
		t.Errorf("b/1/1: err=%v, want ErrAnchorNotFound", err)
	}
	// Unknown revision must miss (no fallback to another parse).
	if err := v.ValidateBlock("qa_x", "rev-does-not-exist", 0, 1); !errors.Is(err, ErrAnchorNotFound) {
		t.Errorf("unknown revision: err=%v, want ErrAnchorNotFound", err)
	}
	// parse-B page 1 block 7 (image, no bbox) exists as an anchor even
	// though its image crop is unavailable (that's Q1's concern).
	if err := v.ValidateBlock("qa_x", "rev-fixture-b", 0, 7); err != nil {
		t.Errorf("b/0/7: unexpected error %v", err)
	}
}

func TestPermissiveAnchorAcceptsAll(t *testing.T) {
	v := PermissiveAnchor()
	if err := v.ValidateBlock("any", "any", 999, 999); err != nil {
		t.Errorf("permissive validator returned %v", err)
	}
}

func TestIdempotency(t *testing.T) {
	idem := NewIdempotency(2)
	h1 := RequestHash("POST", "/api/papers/qa_x/discussions", []byte(`{"body":"a"}`))
	h2 := RequestHash("POST", "/api/papers/qa_x/discussions", []byte(`{"body":"b"}`))
	if h1 == h2 {
		t.Fatal("different bodies must produce different hashes")
	}

	// Miss.
	if _, _, ok, err := idem.Result("k1", h1); ok || err != nil {
		t.Fatalf("empty cache: ok=%v err=%v, want miss", ok, err)
	}

	// Same key + same request → replay.
	idem.Remember("k1", h1, 201, []byte(`{"discussion_id":"cd_1"}`))
	status, body, ok, err := idem.Result("k1", h1)
	if !ok || err != nil || status != 201 || string(body) != `{"discussion_id":"cd_1"}` {
		t.Fatalf("replay: ok=%v err=%v status=%d body=%s", ok, err, status, body)
	}

	// Same key + different request → conflict sentinel.
	if _, _, _, err := idem.Result("k1", h2); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatch: err=%v, want ErrIdempotencyMismatch", err)
	}

	// Empty key is a no-op everywhere.
	idem.Remember("", h1, 201, nil)
	if _, _, ok, _ := idem.Result("", h1); ok {
		t.Fatal("empty key must never replay")
	}

	// Bounded eviction: cap 2, inserting k3 evicts k1 (oldest).
	idem.Remember("k2", h1, 201, nil)
	idem.Remember("k3", h1, 201, nil)
	if _, _, ok, _ := idem.Result("k1", h1); ok {
		t.Fatal("k1 should have been evicted")
	}
	if _, _, ok, _ := idem.Result("k2", h1); !ok {
		t.Fatal("k2 must survive")
	}
}

// TestMemStoreLifecycle covers the domain mechanics the acceptance
// suite then exercises over HTTP: CAS body edits with revision trail,
// status events with mandatory reason, keyset listing, reply
// pagination, and the discussion-not-found distinctions.
func TestMemStoreLifecycle(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	pageIdx, blockIdx := 0, 2

	d, err := s.CreateDiscussion(ctx, CreateDiscussionInput{
		PaperID: "qa_p", ParseRevision: "rev-a", PageIdx: pageIdx, BlockIndex: blockIdx,
		Type: TypeTranscriptionError, Scope: ScopePublic, Status: StatusPending, InitialReason: "initial doubt",
		Body: "root body v1", CreatedBy: "userA", Model: "test-model",
	})
	if err != nil {
		t.Fatalf("CreateDiscussion: %v", err)
	}
	if d.Revision != 1 || d.Status != StatusPending || d.ReplyCount != 0 {
		t.Fatalf("created discussion = %+v", d)
	}

	// Second discussion on the SAME block: independent state (§8 Q2).
	d2, err := s.CreateDiscussion(ctx, CreateDiscussionInput{
		PaperID: "qa_p", ParseRevision: "rev-a", PageIdx: pageIdx, BlockIndex: blockIdx,
		Type: TypeNormal, Scope: ScopeLean, Body: "second thread", CreatedBy: "userB",
	})
	if err != nil {
		t.Fatalf("CreateDiscussion 2: %v", err)
	}

	// Reply by B.
	r, err := s.CreateReply(ctx, CreateReplyInput{DiscussionID: d.DiscussionID, Body: "reply v1", CreatedBy: "userB"})
	if err != nil {
		t.Fatalf("CreateReply: %v", err)
	}
	if got, _, _ := s.GetDiscussion(ctx, d.DiscussionID); got.ReplyCount != 1 {
		t.Fatalf("reply_count = %d, want 1", got.ReplyCount)
	}

	// Stale CAS edit → conflict.
	if _, err := s.UpdateDiscussionBody(ctx, d.DiscussionID, 99, "hijack", "userA"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale edit err=%v, want ErrRevisionConflict", err)
	}
	// Correct CAS edit by the author.
	edited, err := s.UpdateDiscussionBody(ctx, d.DiscussionID, 1, "root body v2", "userA")
	if err != nil || edited.Revision != 2 || edited.Body != "root body v2" {
		t.Fatalf("edit: err=%v revision=%d", err, edited.Revision)
	}
	// Reply CAS edit.
	editedReply, err := s.UpdateReplyBody(ctx, r.ReplyID, 1, "reply v2", "userB")
	if err != nil || editedReply.Revision != 2 {
		t.Fatalf("reply edit: err=%v revision=%d", err, editedReply.Revision)
	}

	// Status transitions record events; empty reason refused.
	if _, err := s.SetStatus(ctx, d.DiscussionID, StatusRetracted, "", "userA"); !errors.Is(err, ErrEmptyReason) {
		t.Fatalf("empty reason err=%v, want ErrEmptyReason", err)
	}
	if _, err := s.SetStatus(ctx, d.DiscussionID, StatusRetracted, "checked against original", "userA"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	// Reopen is legal (§12.1.4).
	if _, err := s.SetStatus(ctx, d.DiscussionID, StatusPending, "new evidence", "userA"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// d2 untouched by d's status churn.
	got2, _, _ := s.GetDiscussion(ctx, d2.DiscussionID)
	if got2.Status != "" {
		t.Fatalf("second discussion status = %q, want empty", got2.Status)
	}

	// History: 2 body revisions (root + reply), 3 status events
	// (initial + retract + reopen).
	revs, events, err := s.History(ctx, d.DiscussionID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(revs) != 2 || len(events) != 3 {
		t.Fatalf("history revs=%d events=%d, want 2/3", len(revs), len(events))
	}
	if events[0].To != StatusPending || events[1].To != StatusRetracted || events[2].To != StatusPending {
		t.Fatalf("event order broken: %+v", events)
	}

	// Keyset list newest-first + anchor filter.
	items, err := s.ListDiscussions(ctx, ListFilter{PaperID: "qa_p", Limit: 10})
	if err != nil || len(items) != 2 || items[0].DiscussionID != d2.DiscussionID {
		t.Fatalf("list: err=%v items=%d", err, len(items))
	}
	anchored, err := s.ListDiscussions(ctx, ListFilter{PaperID: "qa_p", PageIdx: &pageIdx, BlockIndex: &blockIdx, Limit: 10})
	if err != nil || len(anchored) != 2 {
		t.Fatalf("anchor list: err=%v len=%d", err, len(anchored))
	}
	other, err := s.ListDiscussions(ctx, ListFilter{PaperID: "qa_p", BlockIndex: &[]int{9}[0], Limit: 10})
	if err != nil || len(other) != 0 {
		t.Fatalf("bogus anchor list: err=%v len=%d", err, len(other))
	}

	// Missing objects are distinguished (ErrNotFound), not empty.
	if _, _, err := s.GetDiscussion(ctx, "cd_missing"); err != nil {
		t.Fatalf("GetDiscussion missing should be found=false nil err, got %v", err)
	}
	if _, err := s.ListReplies(ctx, "cd_missing", "", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListReplies on missing discussion err=%v, want ErrNotFound", err)
	}
	if _, err := s.CreateReply(ctx, CreateReplyInput{DiscussionID: "cd_missing", Body: "x", CreatedBy: "u"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateReply on missing discussion err=%v, want ErrNotFound", err)
	}
	if _, err := s.UpdateReplyBody(ctx, "cr_missing", 1, "x", "u"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateReplyBody missing err=%v, want ErrNotFound", err)
	}
}
