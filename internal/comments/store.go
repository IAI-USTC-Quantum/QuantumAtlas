package comments

import (
	"context"
	"time"
)

// CreateDiscussionInput carries everything the store needs to insert a
// root discussion. IDs and timestamps are minted by the store — the
// caller (route layer) is responsible for having already validated
// enum/format constraints and the anchor.
type CreateDiscussionInput struct {
	PaperID       string
	ParseRevision string
	PageIdx       int
	BlockIndex    int
	Type          string
	Scope         string
	Status        string // may be "" (no status); if set the store also writes the initial status event
	InitialReason string // optional reason recorded on the initial status event
	Body          string
	CreatedBy     string
	Model         string
}

// CreateReplyInput carries everything the store needs to insert a reply.
type CreateReplyInput struct {
	DiscussionID string
	Body         string
	CreatedBy    string
	Model        string
}

// ListFilter is the Issue-style discussion list query (plan §12.2).
// Zero-value filter fields mean "no constraint". Cursor is the keyset
// position: the last discussion_id of the previous page (newest-first
// ordering). Limit<=0 means "no rows".
//
// Scope filter semantics (plan §12.1.5): filtering by lean returns
// public+lean (the Lean view includes shared public discussion);
// filtering by public returns public only; absent returns everything.
// Stores implement this via an inclusive scope SET, not a single value —
// the route layer expands "lean" into {public, lean}.
type ListFilter struct {
	PaperID       string
	ParseRevision string
	PageIdx       *int
	BlockIndex    *int
	Type          string
	Status        *string // pointer so "pending" vs "unset" vs "null-status notes" are distinguishable: nil = no filter
	StatusIsNull  bool    // true filters to statusless notes (status=none in query)
	Scopes        []string
	Cursor        string // discussion_id keyset cursor
	Limit         int

	// Since / SinceID carry the incremental-sync position (plan
	// §13.4.5): only discussions with (updated_at, discussion_id)
	// strictly greater than (Since, SinceID) are returned. Orthogonal
	// to every filter above AND to the keyset Cursor (the client may
	// page through a since-window with Cursor exactly like a normal
	// list). Zero Since = no since constraint.
	Since   time.Time
	SinceID string
}

// Store is the persistence seam for the comment domain. All methods
// stamp server time and never accept client-supplied actor/timestamps.
//
// Error contract: ErrNotFound / ErrRevisionConflict / ErrUnavailable.
type Store interface {
	CreateDiscussion(ctx context.Context, in CreateDiscussionInput) (Discussion, error)
	ListDiscussions(ctx context.Context, f ListFilter) ([]Discussion, error)
	GetDiscussion(ctx context.Context, discussionID string) (Discussion, bool, error)
	ListReplies(ctx context.Context, discussionID string, afterReplyID string, limit int) ([]Reply, error)
	CreateReply(ctx context.Context, in CreateReplyInput) (Reply, error)
	GetReply(ctx context.Context, replyID string) (Reply, bool, error)

	// SetStatus transitions the discussion status and appends a status
	// event. Any transition (including reopen and re-retract) is legal;
	// reason is mandatory (non-empty) — the store refuses empty ones.
	SetStatus(ctx context.Context, discussionID, to, reason, actor string) (Discussion, error)

	// UpdateDiscussionBody CAS-edits the root body. expectedRevision
	// must equal the stored revision (ErrRevisionConflict otherwise),
	// the row is bumped revision+1 and a BodyRevision row is appended.
	UpdateDiscussionBody(ctx context.Context, discussionID string, expectedRevision int, newBody, editor string) (Discussion, error)

	// UpdateReplyBody is the same contract for a reply body.
	UpdateReplyBody(ctx context.Context, replyID string, expectedRevision int, newBody, editor string) (Reply, error)

	// History returns the body revisions and status events of one
	// discussion (both append-only, oldest first).
	History(ctx context.Context, discussionID string) ([]BodyRevision, []StatusEvent, error)
}

// nowFn is swappable for deterministic tests.
var nowFn = time.Now
