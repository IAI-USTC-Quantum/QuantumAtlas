// Package comments implements the block-level comment domain (plan §6,
// §12.2/§12.3, Q2): discussions anchored to one parsed block, flat
// replies, an optional shared status machine per discussion, and an
// append-only trail of status events and body revisions.
//
// Design invariants (enforced here, not by callers' goodwill):
//
//   - actor and timestamps are always server-recorded. Inputs carry no
//     author/created_at fields; the store stamps the authenticated actor
//     and now() itself. The client-reported `model` string is stored as
//     a pure declaration (plan §6.3).
//   - every body is bound to exactly one parse_revision + page_idx +
//     block_index anchor. Discussions never migrate between revisions,
//     so re-parsing can never silently re-attach old comments to new
//     blocks (plan §4.2, §12.5 golden-anchors).
//   - status is an orthogonal, OPTIONAL state machine
//     (pending/confirmed/retracted, plan §12.1.4): null means "plain
//     note, no issue status". Transitions are free-form (reopen
//     allowed) but every change records a mandatory reason + actor.
//   - body edits are per-author (the author of that body, and nobody
//     else — not even discussion owners or admins, plan §12.1.2),
//     compare-and-swap on the integer revision (If-Match → 409 on
//     stale), and append to comment_body_revisions.
//
// Storage: Store is the persistence seam. Two implementations ship:
//
//   - PGStore — PostgreSQL (registry migrations 00008), the production
//     backend. Transactional: body edits write the new body + revision
//     row atomically; status changes write the event + row together.
//   - MemStore — in-memory, process-local. It exists for unit tests
//     (route acceptance suites run without PostgreSQL) and for the
//     fixture-first Q2 phase; it is NOT wired in production.
package comments

import (
	"errors"
	"regexp"
	"time"
)

// Sentinel errors. Handlers map these onto the shared error contract
// (plan §9): ErrNotFound → 404, ErrRevisionConflict → 409,
// ErrIdempotencyMismatch → 409, ErrUnavailable → 503.
var (
	// ErrNotFound is returned when a discussion or reply id does not
	// exist. Distinct from "block does not exist in this parse" which
	// is ErrAnchorNotFound on the anchor validator.
	ErrNotFound = errors.New("comments: not found")

	// ErrRevisionConflict is returned by the CAS body-edit paths when
	// the caller's If-Match revision does not equal the stored
	// revision (plan §12.2: ETag=revision integer, mismatch → 409).
	ErrRevisionConflict = errors.New("comments: revision conflict")

	// ErrUnavailable mirrors registry.ErrCatalogUnavailable for the
	// PG-backed store when PostgreSQL is not configured/reachable.
	ErrUnavailable = errors.New("comments: store unavailable")

	// ErrIdempotencyMismatch: the same Idempotency-Key was replayed
	// with a different request payload (plan §12.2: same key + same
	// request → replay original result; same key + different request
	// → 409).
	ErrIdempotencyMismatch = errors.New("comments: idempotency key reused with different request")

	// ErrEmptyReason: a status change (or initial status) without the
	// mandatory reason string (plan §12.2).
	ErrEmptyReason = errors.New("comments: status change requires a reason")
)

// Status machine values (plan §12.1.4). A discussion may carry NO
// status at all (the zero value "" / SQL NULL) — a plain note.
const (
	StatusPending   = "pending"   // 疑点提出，尚无结论
	StatusConfirmed = "confirmed" // 认为该具体问题存在（非平台认证）
	StatusRetracted = "retracted" // 指控不成立或撤回；内容保留
)

// ValidStatus reports whether s is a member of the status machine
// ("" — no status — is valid too).
func ValidStatus(s string) bool {
	switch s {
	case "", StatusPending, StatusConfirmed, StatusRetracted:
		return true
	}
	return false
}

// Content scopes (plan §6.1: shared classification/filter dimensions,
// NOT credential scopes and NOT visibility isolation).
const (
	ScopePublic = "public"
	ScopeLean   = "lean"
)

// Preset discussion types (plan §12.1.4): normal note, transcription
// error in the bound parse, or a typo in the original paper. Custom
// slugs are allowed — a type is just a bounded string label and never
// grants permissions.
const (
	TypeNormal             = "normal"
	TypeTranscriptionError = "transcription_error"
	TypeTypoInOriginal     = "typo_in_original"
)

// typeSlugRe constrains custom type labels: lowercase slug-ish, 1..64
// chars, [a-z0-9_:-]+ (plan §12.1.4).
var typeSlugRe = regexp.MustCompile(`^[a-z0-9_:-]{1,64}$`)

// ValidType reports whether t is a usable discussion type label.
func ValidType(t string) bool { return typeSlugRe.MatchString(t) }

// ValidScope reports whether s is a usable content scope.
func ValidScope(s string) bool {
	return s == ScopePublic || s == ScopeLean
}

// Discussion is one root comment and its anchor. Revision is the body
// CAS counter (1 on creation; +1 per body edit). ReplyCount is
// denormalized for list rendering.
type Discussion struct {
	DiscussionID  string
	PaperID       string
	ParseRevision string
	PageIdx       int
	BlockIndex    int
	Type          string
	Scope         string
	Status        string // "" (SQL NULL) = no status
	Body          string
	CreatedBy     string // authenticated PocketBase user id, server-stamped
	Model         string // client declaration, may be ""
	Revision      int
	ReplyCount    int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Reply is one flat reply inside a discussion. Same CAS/body-edit
// semantics as a discussion body (plan §12.1.2: every body's author
// edits their own).
type Reply struct {
	ReplyID      string
	DiscussionID string
	Body         string
	CreatedBy    string
	Model        string
	Revision     int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// StatusEvent is one append-only status transition record.
type StatusEvent struct {
	From   string // "" when the previous state was "no status"
	To     string // never "" — dropping a status is not a transition
	Reason string // mandatory (plan §12.2)
	Actor  string
	At     time.Time
}

// BodyRevision is one append-only body edit record (old → new).
type BodyRevision struct {
	Target   string // "discussion" | "reply"
	TargetID string
	OldBody  string
	NewBody  string
	Editor   string
	At       time.Time
}

// Body revision targets.
const (
	TargetDiscussion = "discussion"
	TargetReply      = "reply"
)
