package comments

import (
	"context"
	"sort"
	"sync"
)

// MemStore is an in-memory Store. It powers the route acceptance tests
// (which run without PostgreSQL) and the fixture-first Q2 phase; it is
// deliberately NOT wired in production (the PG store is). Concurrency:
// one RWMutex, coarse-grained — the acceptance suites are sequential.
type MemStore struct {
	mu          sync.Mutex
	discussions map[string]*Discussion
	replies     map[string]*Reply // reply_id → reply
	// history per discussion: body revisions + status events, oldest first
	bodyRevisions map[string][]BodyRevision
	statusEvents  map[string][]StatusEvent
	// replyRevisions keyed by reply_id (body revisions target replies too)
	replyRevisions map[string][]BodyRevision
}

// NewMemStore builds an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		discussions:    map[string]*Discussion{},
		replies:        map[string]*Reply{},
		bodyRevisions:  map[string][]BodyRevision{},
		statusEvents:   map[string][]StatusEvent{},
		replyRevisions: map[string][]BodyRevision{},
	}
}

func (m *MemStore) CreateDiscussion(_ context.Context, in CreateDiscussionInput) (Discussion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := nowFn()
	d := Discussion{
		DiscussionID:  newID("cd_"),
		PaperID:       in.PaperID,
		ParseRevision: in.ParseRevision,
		PageIdx:       in.PageIdx,
		BlockIndex:    in.BlockIndex,
		Type:          in.Type,
		Scope:         in.Scope,
		Status:        in.Status,
		Body:          in.Body,
		CreatedBy:     in.CreatedBy,
		Model:         in.Model,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	m.discussions[d.DiscussionID] = &d
	if in.Status != "" {
		m.statusEvents[d.DiscussionID] = append(m.statusEvents[d.DiscussionID], StatusEvent{
			From:   "",
			To:     in.Status,
			Reason: in.InitialReason,
			Actor:  in.CreatedBy,
			At:     now,
		})
	}
	return d, nil
}

func (m *MemStore) ListDiscussions(_ context.Context, f ListFilter) ([]Discussion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Discussion
	for _, d := range m.discussions {
		if f.PaperID != "" && d.PaperID != f.PaperID {
			continue
		}
		if f.ParseRevision != "" && d.ParseRevision != f.ParseRevision {
			continue
		}
		if f.PageIdx != nil && d.PageIdx != *f.PageIdx {
			continue
		}
		if f.BlockIndex != nil && d.BlockIndex != *f.BlockIndex {
			continue
		}
		if f.Type != "" && d.Type != f.Type {
			continue
		}
		if f.StatusIsNull {
			if d.Status != "" {
				continue
			}
		} else if f.Status != nil && d.Status != *f.Status {
			continue
		}
		if len(f.Scopes) > 0 {
			ok := false
			for _, s := range f.Scopes {
				if d.Scope == s {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if f.Cursor != "" && d.DiscussionID >= f.Cursor {
			continue
		}
		out = append(out, *d)
	}
	// Newest first (keyset: ULID desc == created desc).
	sort.Slice(out, func(i, j int) bool { return out[i].DiscussionID > out[j].DiscussionID })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	for i := range out {
		out[i].ReplyCount = m.replyCountLocked(out[i].DiscussionID)
	}
	return out, nil
}

func (m *MemStore) replyCountLocked(discussionID string) int {
	n := 0
	for _, r := range m.replies {
		if r.DiscussionID == discussionID {
			n++
		}
	}
	return n
}

func (m *MemStore) GetDiscussion(_ context.Context, id string) (Discussion, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.discussions[id]
	if !ok {
		return Discussion{}, false, nil
	}
	out := *d
	out.ReplyCount = m.replyCountLocked(id)
	return out, true, nil
}

func (m *MemStore) ListReplies(_ context.Context, discussionID, afterReplyID string, limit int) ([]Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.discussions[discussionID]; !ok {
		return nil, ErrNotFound
	}
	var out []Reply
	for _, r := range m.replies {
		if r.DiscussionID != discussionID {
			continue
		}
		if afterReplyID != "" && r.ReplyID <= afterReplyID {
			continue
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReplyID < out[j].ReplyID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) CreateReply(_ context.Context, in CreateReplyInput) (Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.discussions[in.DiscussionID]; !ok {
		return Reply{}, ErrNotFound
	}
	now := nowFn()
	r := Reply{
		ReplyID:      newID("cr_"),
		DiscussionID: in.DiscussionID,
		Body:         in.Body,
		CreatedBy:    in.CreatedBy,
		Model:        in.Model,
		Revision:     1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.replies[r.ReplyID] = &r
	return r, nil
}

func (m *MemStore) GetReply(_ context.Context, id string) (Reply, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.replies[id]
	if !ok {
		return Reply{}, false, nil
	}
	return *r, true, nil
}

func (m *MemStore) SetStatus(_ context.Context, discussionID, to, reason, actor string) (Discussion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.discussions[discussionID]
	if !ok {
		return Discussion{}, ErrNotFound
	}
	if reason == "" {
		return Discussion{}, ErrEmptyReason
	}
	from := d.Status
	d.Status = to
	d.UpdatedAt = nowFn()
	m.statusEvents[discussionID] = append(m.statusEvents[discussionID], StatusEvent{
		From:   from,
		To:     to,
		Reason: reason,
		Actor:  actor,
		At:     d.UpdatedAt,
	})
	return *d, nil
}

func (m *MemStore) UpdateDiscussionBody(_ context.Context, discussionID string, expectedRevision int, newBody, editor string) (Discussion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.discussions[discussionID]
	if !ok {
		return Discussion{}, ErrNotFound
	}
	if d.Revision != expectedRevision {
		return Discussion{}, ErrRevisionConflict
	}
	old := d.Body
	now := nowFn()
	d.Body = newBody
	d.Revision++
	d.UpdatedAt = now
	m.bodyRevisions[discussionID] = append(m.bodyRevisions[discussionID], BodyRevision{
		Target:   TargetDiscussion,
		TargetID: discussionID,
		OldBody:  old,
		NewBody:  newBody,
		Editor:   editor,
		At:       now,
	})
	return *d, nil
}

func (m *MemStore) UpdateReplyBody(_ context.Context, replyID string, expectedRevision int, newBody, editor string) (Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.replies[replyID]
	if !ok {
		return Reply{}, ErrNotFound
	}
	if r.Revision != expectedRevision {
		return Reply{}, ErrRevisionConflict
	}
	old := r.Body
	now := nowFn()
	r.Body = newBody
	r.Revision++
	r.UpdatedAt = now
	m.replyRevisions[replyID] = append(m.replyRevisions[replyID], BodyRevision{
		Target:   TargetReply,
		TargetID: replyID,
		OldBody:  old,
		NewBody:  newBody,
		Editor:   editor,
		At:       now,
	})
	return *r, nil
}

func (m *MemStore) History(_ context.Context, discussionID string) ([]BodyRevision, []StatusEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.discussions[discussionID]; !ok {
		return nil, nil, ErrNotFound
	}
	revs := append([]BodyRevision(nil), m.bodyRevisions[discussionID]...)
	// Reply-body revisions belong to this discussion's replies too:
	// a discussion's revision history must include its replies' edits.
	for _, r := range m.replies {
		if r.DiscussionID == discussionID {
			revs = append(revs, m.replyRevisions[r.ReplyID]...)
		}
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].At.Before(revs[j].At) })
	events := append([]StatusEvent(nil), m.statusEvents[discussionID]...)
	return revs, events, nil
}
