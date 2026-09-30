// comments_since_test.go — route-level acceptance for the ?since=
// incremental-sync parameter (plan §13.4.5) on the real mux with the
// memstore: a full pull-style polling loop, filter composition, the
// since-advance rule (last page only), and cursor input validation.
package routes

import (
	"net/http"
	"testing"
	"time"
)

// sinceTick guarantees the memstore's wall clock crosses into a new
// millisecond before the next write. The since window compares
// (updated_at truncated to ms, discussion_id): a row EDITED within the
// same millisecond as the cursor anchor keeps its (smaller) id and so
// falls out of the strictly-greater window — on a fast CI runner the
// whole create→edit sequence can land inside one millisecond, which
// made these tests flaky. time.Sleep always advances the wall clock by
// at least its duration, so 2ms deterministically clears the 1ms
// comparison granularity.
func sinceTick() { time.Sleep(2 * time.Millisecond) }

// sincePoll runs one GET and returns (items-by-id, top-level since).
func sincePoll(t *testing.T, h *commentsHarness, pat, query string) (map[string]bool, any) {
	t.Helper()
	rec, resp := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions"+query, "", bearer(pat))
	if rec.Code != http.StatusOK {
		t.Fatalf("poll %q: %d %v", query, rec.Code, resp)
	}
	ids := map[string]bool{}
	for _, it := range resp["items"].([]any) {
		ids[it.(map[string]any)["discussion_id"].(string)] = true
	}
	return ids, resp["since"]
}

// createSinceDiscussion posts one plain public discussion on the
// fixture anchor and returns its id.
func createSinceDiscussion(t *testing.T, h *commentsHarness, pat, body string) string {
	t.Helper()
	rec, resp := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
		"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 2,
		"type": "normal", "scope": "public", "body": "`+body+`"}`, bearer(pat))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q: %d %v", body, rec.Code, resp)
	}
	return resp["discussion_id"].(string)
}

// TestQ2_ListSincePullSync walks the client loop the endpoint exists
// for: full snapshot → poll with the returned since → edits and new
// rows arrive exactly once → quiet poll echoes the cursor unchanged.
func TestQ2_ListSincePullSync(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("since-a", false, `["papers:read","comments:write"]`)

	first := createSinceDiscussion(t, h, patA, "first")
	second := createSinceDiscussion(t, h, patA, "second")

	// Full snapshot (no since): both rows + a since the client stores.
	ids, since := sincePoll(t, h, patA, "")
	if len(ids) != 2 || !ids[first] || !ids[second] {
		t.Fatalf("snapshot = %v, want both rows", ids)
	}
	cursor, ok := since.(string)
	if !ok || cursor == "" {
		t.Fatalf("snapshot since = %v, want a non-empty cursor", since)
	}

	// Quiet window: nothing changed → empty items, since echoed back.
	quiet, since2 := sincePoll(t, h, patA, "?since="+cursor)
	if len(quiet) != 0 || since2 != cursor {
		t.Fatalf("quiet poll = %v since=%v, want empty + echo", quiet, since2)
	}

	// Edit `first` (bumps updated_at) and add a third row.
	sinceTick() // edits must land in a strictly later millisecond
	rec, resp := h.do(http.MethodPatch, "/api/discussions/"+first+"/body",
		`{"body":"first (edited)"}`, mergeHeaders(bearer(patA), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("edit first: %d %v", rec.Code, resp)
	}
	third := createSinceDiscussion(t, h, patA, "third")

	// Delta poll: exactly the edited row + the new row; since advances.
	delta, since3 := sincePoll(t, h, patA, "?since="+cursor)
	if len(delta) != 2 || !delta[first] || !delta[third] {
		t.Fatalf("delta = %v, want {first, third}", delta)
	}
	if delta[second] {
		t.Fatal("untouched row leaked into the delta")
	}
	if since3 == cursor {
		t.Fatal("since must advance after a non-empty window")
	}

	// Quiet again at the new position.
	quiet2, since4 := sincePoll(t, h, patA, "?since="+since3.(string))
	if len(quiet2) != 0 || since4 != since3 {
		t.Fatalf("quiet poll 2 = %v since=%v, want empty + echo", quiet2, since4)
	}
}

// TestQ2_ListSinceFilterComposition: since composes with the ordinary
// filters — a row inside the window but excluded by the filter stays
// out, and a filtered-in row inside the window comes back.
func TestQ2_ListSinceFilterComposition(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("since-b", false, `["papers:read","comments:write"]`)

	mk := func(ty string) string {
		t.Helper()
		rec, resp := h.do(http.MethodPost, "/api/papers/"+fixturePaper+"/discussions", `{
			"parse_revision": "`+fixtureRevisionA+`", "page_idx": 0, "block_index": 2,
			"type": "`+ty+`", "scope": "public", "body": "`+ty+` body"}`, bearer(patA))
		if rec.Code != http.StatusCreated {
			t.Fatalf("mk %s: %d %v", ty, rec.Code, resp)
		}
		return resp["discussion_id"].(string)
	}
	normal := mk("normal")
	errType := mk("transcription_error")

	_, since := sincePoll(t, h, patA, "")

	// Both rows change: normal via an edit, the other via a new reply.
	sinceTick() // edits must land in a strictly later millisecond
	rec, resp := h.do(http.MethodPatch, "/api/discussions/"+normal+"/body",
		`{"body":"normal (edited)"}`, mergeHeaders(bearer(patA), map[string]string{"If-Match": "1"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("edit normal: %d %v", rec.Code, resp)
	}
	if rec, resp = h.do(http.MethodPost, "/api/discussions/"+errType+"/replies",
		`{"body":"a reply"}`, bearer(patA)); rec.Code != http.StatusCreated {
		t.Fatalf("reply: %d %v", rec.Code, resp)
	}

	delta, _ := sincePoll(t, h, patA, "?since="+since.(string)+"&type=normal")
	if len(delta) != 1 || !delta[normal] {
		t.Fatalf("delta type=normal = %v, want only the edited normal row", delta)
	}
}

// TestQ2_ListSincePagingAdvance: inside a window with more rows than
// per_page, the top-level since only advances on the LAST page — a
// middle page echoes the incoming cursor so a client that stops mid-way
// re-pulls instead of skipping rows.
func TestQ2_ListSincePagingAdvance(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("since-c", false, `["papers:read","comments:write"]`)

	createSinceDiscussion(t, h, patA, "one")
	createSinceDiscussion(t, h, patA, "two")
	_, since := sincePoll(t, h, patA, "")
	cursor := since.(string)

	// Three more rows land inside the window past `since`.
	createSinceDiscussion(t, h, patA, "three")
	createSinceDiscussion(t, h, patA, "four")
	createSinceDiscussion(t, h, patA, "five")

	// Middle page (per_page=2 of 3): next_cursor set → since NOT advanced.
	rec, p1 := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?since="+cursor+"&per_page=2", "", bearer(patA))
	if rec.Code != http.StatusOK {
		t.Fatalf("middle page: %d %v", rec.Code, p1)
	}
	if items := p1["items"].([]any); len(items) != 2 {
		t.Fatalf("middle page = %d items, want 2", len(items))
	}
	if p1["next_cursor"] == nil {
		t.Fatal("middle page must still keyset-paginate")
	}
	if p1["since"] != cursor {
		t.Fatalf("middle page since = %v, want the echoed %v (no advance)", p1["since"], cursor)
	}

	// Last page (cursor-continued): next_cursor nil → since advances.
	next := p1["next_cursor"].(string)
	rec, p2 := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?since="+cursor+
		"&per_page=2&cursor="+next, "", bearer(patA))
	if rec.Code != http.StatusOK {
		t.Fatalf("last page: %d %v", rec.Code, p2)
	}
	if items := p2["items"].([]any); len(items) != 1 {
		t.Fatalf("last page = %d items, want 1", len(items))
	}
	if p2["next_cursor"] != nil {
		t.Fatalf("last page next_cursor = %v, want nil", p2["next_cursor"])
	}
	if p2["since"] == cursor {
		t.Fatal("last page must advance since")
	}
}

// TestQ2_ListSinceValidation: malformed cursors are a 400; a valid but
// far-future position yields an empty window with the cursor echoed.
func TestQ2_ListSinceValidation(t *testing.T) {
	h := newCommentsHarness(t)
	_, patA := h.userWithPAT("since-d", false, `["papers:read"]`)

	for _, bad := range []string{"garbage", "-cd_x", "123-", "12x-cd_x"} {
		rec, resp := h.do(http.MethodGet, "/api/papers/"+fixturePaper+"/discussions?since="+bad, "", bearer(patA))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("since=%q: %d %v, want 400", bad, rec.Code, resp)
		}
	}

	future := "999999999999999-cd_zzz"
	ids, since := sincePoll(t, h, patA, "?since="+future)
	if len(ids) != 0 || since != future {
		t.Fatalf("future window = %v since=%v, want empty + echo", ids, since)
	}
}
