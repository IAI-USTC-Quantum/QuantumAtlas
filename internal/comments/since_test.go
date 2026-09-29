package comments

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEncodeDecodeSinceCursor(t *testing.T) {
	ts := time.UnixMilli(1765948800123)
	for _, id := range []string{"cd_01jdg8rkz0a1b2c3d4e5f6g7h8", "cd_with-dash", "anything"} {
		s := EncodeSinceCursor(ts, id)
		got, gotID, err := DecodeSinceCursor(s)
		if err != nil {
			t.Fatalf("DecodeSinceCursor(%q): %v", s, err)
		}
		if got.UnixMilli() != ts.UnixMilli() || gotID != id {
			t.Fatalf("roundtrip = %d,%q want %d,%q", got.UnixMilli(), gotID, ts.UnixMilli(), id)
		}
	}

	for _, bad := range []string{"", "-", "-cd_x", "abc-cd_x", "12.5-cd_x", "1765948800123-", "--", "123"} {
		if _, _, err := DecodeSinceCursor(bad); !errors.Is(err, ErrBadSinceCursor) {
			t.Errorf("DecodeSinceCursor(%q) err=%v, want ErrBadSinceCursor", bad, err)
		}
	}
}

// TestMemStoreSince covers the incremental-sync semantics (plan §13.4.5):
// updated rows come back, untouched rows do not, and same-millisecond
// siblings are never skipped (the id half of the cursor breaks ties).
func TestMemStoreSince(t *testing.T) {
	restore := nowFn
	t.Cleanup(func() { nowFn = restore })

	t0 := time.UnixMilli(1765948800000)
	t1 := t0.Add(1 * time.Millisecond)
	t2 := t0.Add(2 * time.Millisecond)
	ctx := context.Background()
	s := NewMemStore()

	nowFn = func() time.Time { return t0 }
	old, err := s.CreateDiscussion(ctx, CreateDiscussionInput{
		PaperID: "qa_p", ParseRevision: "rev-a", PageIdx: 0, BlockIndex: 2,
		Body: "stale thread", CreatedBy: "u",
	})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	moved, err := s.CreateDiscussion(ctx, CreateDiscussionInput{
		PaperID: "qa_p", ParseRevision: "rev-a", PageIdx: 0, BlockIndex: 2,
		Body: "thread that gets edited later", CreatedBy: "u",
	})
	if err != nil {
		t.Fatalf("create moved: %v", err)
	}

	// Same-millisecond siblings: three rows minted inside one millis
	// (with a sub-milli remainder — storage is finer than the cursor
	// format, the comparison must floor to millispace).
	sameMs := time.UnixMilli(1765948999999).Add(500 * time.Microsecond)
	nowFn = func() time.Time { return sameMs }
	var siblings []Discussion
	for i := 0; i < 3; i++ {
		d, err := s.CreateDiscussion(ctx, CreateDiscussionInput{
			PaperID: "qa_p", ParseRevision: "rev-a", PageIdx: 0, BlockIndex: 2,
			Body: "same-ms sibling", CreatedBy: "u",
		})
		if err != nil {
			t.Fatalf("create sibling %d: %v", i, err)
		}
		siblings = append(siblings, d)
	}

	// Bump `moved` strictly after the sibling millisecond.
	nowFn = func() time.Time { return t2 }
	if _, err := s.UpdateDiscussionBody(ctx, moved.DiscussionID, 1, "edited", "u"); err != nil {
		t.Fatalf("edit moved: %v", err)
	}
	nowFn = restore

	list := func(since time.Time, sinceID string) []string {
		t.Helper()
		items, err := s.ListDiscussions(ctx, ListFilter{PaperID: "qa_p", Since: since, SinceID: sinceID, Limit: 100})
		if err != nil {
			t.Fatalf("ListDiscussions since: %v", err)
		}
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.DiscussionID)
		}
		return ids
	}
	has := func(ids []string, id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}

	// Position at t1 (between t0 and the sibling ms): only the edited
	// thread and the three siblings are past it; the stale thread is not.
	got := list(t1, "")
	if has(got, old.DiscussionID) {
		t.Errorf("untouched discussion %s must not be returned", old.DiscussionID)
	}
	if !has(got, moved.DiscussionID) || !has(got, siblings[2].DiscussionID) {
		t.Errorf("updated/new discussions missing: %v", got)
	}

	// Same-millisecond tiebreak: position INSIDE the sibling ms after
	// sibling[1] → only sibling[2] (and nothing else at that ms) follows.
	got = list(sameMs.Truncate(time.Millisecond), siblings[1].DiscussionID)
	if len(got) != 1 || got[0] != siblings[2].DiscussionID {
		t.Errorf("same-ms tiebreak = %v, want only %s", got, siblings[2].DiscussionID)
	}

	// Position at the very same ms but with the empty id "" → all three
	// siblings (id > "") plus nothing older.
	got = list(sameMs.Truncate(time.Millisecond), "")
	if len(got) != 3 {
		t.Errorf("same-ms from bottom = %d items, want 3 siblings", len(got))
	}

	// Roundtrip quietness: a cursor minted from the newest row — whose
	// updated_at carries a sub-milli remainder the cursor cannot carry
	// — must NOT re-emit that row on the next poll (millispace floor).
	rt := EncodeSinceCursor(siblings[2].UpdatedAt, siblings[2].DiscussionID)
	rtT, rtID, err := DecodeSinceCursor(rt)
	if err != nil {
		t.Fatalf("decode roundtrip cursor: %v", err)
	}
	if got = list(rtT, rtID); len(got) != 0 {
		t.Errorf("cursor from newest row re-emits %v, want a quiet window", got)
	}

	// Cursor pagination composes orthogonally inside a since window:
	// page through the full window with the keyset cursor.
	page1, err := s.ListDiscussions(ctx, ListFilter{PaperID: "qa_p", Since: t1, Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("since+keyset page1: err=%v len=%d", err, len(page1))
	}
	page2, err := s.ListDiscussions(ctx, ListFilter{PaperID: "qa_p", Since: t1, Cursor: page1[1].DiscussionID, Limit: 2})
	if err != nil || len(page2) != 2 {
		t.Fatalf("since+keyset page2: err=%v len=%d", err, len(page2))
	}
	seen := map[string]bool{}
	for _, it := range append(append([]Discussion{}, page1...), page2...) {
		seen[it.DiscussionID] = true
	}
	if len(seen) != 4 || seen[old.DiscussionID] || !seen[moved.DiscussionID] {
		t.Errorf("paged since window = %v, want the 4 rows past t1 only", seen)
	}
}
