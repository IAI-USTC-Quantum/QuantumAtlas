package comments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the production PostgreSQL-backed Store (registry migration
// 00008). It wraps the shared registry pool; every mutating operation
// runs in one transaction so the comment rows and their history stay
// consistent (plan §8 Q2: "评论业务与历史在所选数据库事务边界内一致").
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore wraps a pool (may be nil → every op reports ErrUnavailable,
// mirroring registry.Store's optional-Pool convention).
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) ensure() bool { return s != nil && s.pool != nil }

func unavailable(op string, err error) error {
	if err == nil {
		return ErrUnavailable
	}
	return fmt.Errorf("%s: %w (%v)", op, ErrUnavailable, err)
}

// pgNewID mints ids with the shared monotonic scheme (cd_/cr_ + ULID;
// see ids.go for why monotonic entropy matters to the cursors).
func pgNewID(prefix string) string { return newID(prefix) }

// discussionSelect is the canonical discussion projection, aliased `d`
// for the reply-count subquery. Used by every read path.
const discussionSelect = `SELECT discussion_id, paper_id, parse_revision, page_idx, block_index,
	type, scope, status, body, created_by, model, revision, created_at, updated_at,
	(SELECT count(*) FROM comment_replies r WHERE r.discussion_id = d.discussion_id)
	FROM comment_discussions d`

func scanDiscussion(row pgx.Row) (Discussion, error) {
	var d Discussion
	var status, model *string
	err := row.Scan(&d.DiscussionID, &d.PaperID, &d.ParseRevision, &d.PageIdx, &d.BlockIndex,
		&d.Type, &d.Scope, &status, &d.Body, &d.CreatedBy, &model, &d.Revision,
		&d.CreatedAt, &d.UpdatedAt, &d.ReplyCount)
	if err != nil {
		return d, err
	}
	d.Status = derefStr(status)
	d.Model = derefStr(model)
	return d, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// getDiscussionTx re-reads the canonical projection inside a tx.
func getDiscussionTx(ctx context.Context, q pgx.Tx, id string) (Discussion, error) {
	return scanDiscussion(q.QueryRow(ctx, discussionSelect+` WHERE d.discussion_id = $1`, id))
}

func (s *PGStore) CreateDiscussion(ctx context.Context, in CreateDiscussionInput) (Discussion, error) {
	if !s.ensure() {
		return Discussion{}, ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Discussion{}, unavailable("comments: begin create discussion", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := pgNewID("cd_")
	if _, err := tx.Exec(ctx, `
		INSERT INTO comment_discussions
			(discussion_id, paper_id, parse_revision, page_idx, block_index,
			 type, scope, status, body, created_by, model)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id, in.PaperID, in.ParseRevision, in.PageIdx, in.BlockIndex,
		in.Type, in.Scope, nullStr(in.Status), in.Body, in.CreatedBy, nullStr(in.Model)); err != nil {
		return Discussion{}, unavailable("comments: insert discussion", err)
	}
	if in.Status != "" {
		reason := in.InitialReason
		if reason == "" {
			reason = "initial status"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO comment_status_events (discussion_id, from_status, to_status, reason, actor)
			VALUES ($1, NULL, $2, $3, $4)`, id, in.Status, reason, in.CreatedBy); err != nil {
			return Discussion{}, unavailable("comments: insert initial status event", err)
		}
	}
	d, err := getDiscussionTx(ctx, tx, id)
	if err != nil {
		return Discussion{}, unavailable("comments: re-read discussion", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Discussion{}, unavailable("comments: commit create discussion", err)
	}
	return d, nil
}

func (s *PGStore) ListDiscussions(ctx context.Context, f ListFilter) ([]Discussion, error) {
	if !s.ensure() {
		return nil, ErrUnavailable
	}
	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	where = append(where, "paper_id = "+arg(f.PaperID))
	if f.ParseRevision != "" {
		where = append(where, "parse_revision = "+arg(f.ParseRevision))
	}
	if f.PageIdx != nil {
		where = append(where, "page_idx = "+arg(*f.PageIdx))
	}
	if f.BlockIndex != nil {
		where = append(where, "block_index = "+arg(*f.BlockIndex))
	}
	if f.Type != "" {
		where = append(where, "type = "+arg(f.Type))
	}
	if f.StatusIsNull {
		where = append(where, "status IS NULL")
	} else if f.Status != nil {
		where = append(where, "status = "+arg(*f.Status))
	}
	if len(f.Scopes) > 0 {
		where = append(where, "scope = ANY("+arg(f.Scopes)+")")
	}
	if f.Cursor != "" {
		where = append(where, "discussion_id < "+arg(f.Cursor))
	}
	if !f.Since.IsZero() {
		// Incremental-sync position (plan §13.4.5). The row timestamp
		// is floored to millis to match the cursor's precision (see
		// since.go sinceAfter); the id half breaks same-millisecond
		// ties. Filters after the paper_id prefix of the keyset index
		// — see the pgstore integration EXPLAIN notes; no new index
		// by design.
		where = append(where, "(date_trunc('milliseconds', updated_at), discussion_id) > ("+arg(f.Since)+", "+arg(f.SinceID)+")")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 1 // callers always pass a positive limit; defensive floor
	}
	sql := discussionSelect + ` WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY discussion_id DESC LIMIT ` + arg(limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, unavailable("comments: list discussions", err)
	}
	defer rows.Close()
	var out []Discussion
	for rows.Next() {
		d, err := scanDiscussion(rows)
		if err != nil {
			return nil, unavailable("comments: scan discussion", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PGStore) GetDiscussion(ctx context.Context, id string) (Discussion, bool, error) {
	if !s.ensure() {
		return Discussion{}, false, ErrUnavailable
	}
	d, err := scanDiscussion(s.pool.QueryRow(ctx, discussionSelect+` WHERE d.discussion_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Discussion{}, false, nil
	}
	if err != nil {
		return Discussion{}, false, unavailable("comments: get discussion", err)
	}
	return d, true, nil
}

// replySelect is the canonical reply projection.
const replySelect = `SELECT reply_id, discussion_id, body, created_by, model, revision, created_at, updated_at
	FROM comment_replies`

func scanReply(row pgx.Row) (Reply, error) {
	var r Reply
	var model *string
	err := row.Scan(&r.ReplyID, &r.DiscussionID, &r.Body, &r.CreatedBy, &model, &r.Revision,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	r.Model = derefStr(model)
	return r, nil
}

// discussionExistsTx guards the reply paths so a bogus discussion id
// 404s instead of returning an empty page that looks like success
// (plan §9: no fake-success on missing objects).
func discussionExists(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM comment_discussions WHERE discussion_id = $1)`, id).Scan(&exists)
	return exists, err
}

func (s *PGStore) ListReplies(ctx context.Context, discussionID, afterReplyID string, limit int) ([]Reply, error) {
	if !s.ensure() {
		return nil, ErrUnavailable
	}
	if limit <= 0 {
		limit = 20
	}
	exists, err := discussionExists(ctx, s.pool, discussionID)
	if err != nil {
		return nil, unavailable("comments: check discussion", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	sql := replySelect + ` WHERE discussion_id = $1`
	args := []any{discussionID}
	if afterReplyID != "" {
		args = append(args, afterReplyID)
		sql += fmt.Sprintf(` AND reply_id > $%d`, len(args))
	}
	args = append(args, limit)
	sql += fmt.Sprintf(` ORDER BY reply_id ASC LIMIT $%d`, len(args))
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, unavailable("comments: list replies", err)
	}
	defer rows.Close()
	var out []Reply
	for rows.Next() {
		r, err := scanReply(rows)
		if err != nil {
			return nil, unavailable("comments: scan reply", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PGStore) CreateReply(ctx context.Context, in CreateReplyInput) (Reply, error) {
	if !s.ensure() {
		return Reply{}, ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reply{}, unavailable("comments: begin create reply", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := pgNewID("cr_")
	tag, err := tx.Exec(ctx, `
		INSERT INTO comment_replies (reply_id, discussion_id, body, created_by, model)
		SELECT $1, d.discussion_id, $3, $4, $5
		FROM comment_discussions d WHERE d.discussion_id = $2`,
		id, in.DiscussionID, in.Body, in.CreatedBy, nullStr(in.Model))
	if err != nil {
		return Reply{}, unavailable("comments: insert reply", err)
	}
	if tag.RowsAffected() == 0 {
		return Reply{}, ErrNotFound
	}
	r, err := scanReply(tx.QueryRow(ctx, replySelect+` WHERE reply_id = $1`, id))
	if err != nil {
		return Reply{}, unavailable("comments: re-read reply", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Reply{}, unavailable("comments: commit create reply", err)
	}
	return r, nil
}

func (s *PGStore) GetReply(ctx context.Context, id string) (Reply, bool, error) {
	if !s.ensure() {
		return Reply{}, false, ErrUnavailable
	}
	r, err := scanReply(s.pool.QueryRow(ctx, replySelect+` WHERE reply_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Reply{}, false, nil
	}
	if err != nil {
		return Reply{}, false, unavailable("comments: get reply", err)
	}
	return r, true, nil
}

func (s *PGStore) SetStatus(ctx context.Context, discussionID, to, reason, actor string) (Discussion, error) {
	if !s.ensure() {
		return Discussion{}, ErrUnavailable
	}
	if reason == "" {
		return Discussion{}, ErrEmptyReason
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Discussion{}, unavailable("comments: begin set status", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the row, read the current status, append the event, update,
	// re-read. Any transition (incl. reopen / re-retract) is legal.
	row := tx.QueryRow(ctx,
		`SELECT status FROM comment_discussions WHERE discussion_id = $1 FOR UPDATE`, discussionID)
	var from *string
	if err := row.Scan(&from); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Discussion{}, ErrNotFound
		}
		return Discussion{}, unavailable("comments: lock discussion", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO comment_status_events (discussion_id, from_status, to_status, reason, actor)
		VALUES ($1, $2, $3, $4, $5)`, discussionID, from, to, reason, actor); err != nil {
		return Discussion{}, unavailable("comments: insert status event", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comment_discussions SET status = $2, updated_at = now()
		WHERE discussion_id = $1`, discussionID, nullStr(to)); err != nil {
		return Discussion{}, unavailable("comments: update status", err)
	}
	d, err := getDiscussionTx(ctx, tx, discussionID)
	if err != nil {
		return Discussion{}, unavailable("comments: re-read discussion", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Discussion{}, unavailable("comments: commit set status", err)
	}
	return d, nil
}

func (s *PGStore) UpdateDiscussionBody(ctx context.Context, discussionID string, expectedRevision int, newBody, editor string) (Discussion, error) {
	if !s.ensure() {
		return Discussion{}, ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Discussion{}, unavailable("comments: begin edit discussion body", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var old string
	var rev int
	err = tx.QueryRow(ctx,
		`SELECT body, revision FROM comment_discussions WHERE discussion_id = $1 FOR UPDATE`,
		discussionID).Scan(&old, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return Discussion{}, ErrNotFound
	}
	if err != nil {
		return Discussion{}, unavailable("comments: lock discussion body", err)
	}
	if rev != expectedRevision {
		return Discussion{}, ErrRevisionConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO comment_body_revisions (target, target_id, old_body, new_body, editor)
		VALUES ($1, $2, $3, $4, $5)`, TargetDiscussion, discussionID, old, newBody, editor); err != nil {
		return Discussion{}, unavailable("comments: insert body revision", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comment_discussions SET body = $2, revision = revision + 1, updated_at = now()
		WHERE discussion_id = $1`, discussionID, newBody); err != nil {
		return Discussion{}, unavailable("comments: update discussion body", err)
	}
	d, err := getDiscussionTx(ctx, tx, discussionID)
	if err != nil {
		return Discussion{}, unavailable("comments: re-read discussion", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Discussion{}, unavailable("comments: commit edit discussion body", err)
	}
	return d, nil
}

func (s *PGStore) UpdateReplyBody(ctx context.Context, replyID string, expectedRevision int, newBody, editor string) (Reply, error) {
	if !s.ensure() {
		return Reply{}, ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reply{}, unavailable("comments: begin edit reply body", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var old string
	var rev int
	err = tx.QueryRow(ctx,
		`SELECT body, revision FROM comment_replies WHERE reply_id = $1 FOR UPDATE`, replyID).Scan(&old, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reply{}, ErrNotFound
	}
	if err != nil {
		return Reply{}, unavailable("comments: lock reply body", err)
	}
	if rev != expectedRevision {
		return Reply{}, ErrRevisionConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO comment_body_revisions (target, target_id, old_body, new_body, editor)
		VALUES ($1, $2, $3, $4, $5)`, TargetReply, replyID, old, newBody, editor); err != nil {
		return Reply{}, unavailable("comments: insert reply revision", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comment_replies SET body = $2, revision = revision + 1, updated_at = now()
		WHERE reply_id = $1`, replyID, newBody); err != nil {
		return Reply{}, unavailable("comments: update reply body", err)
	}
	r, err := scanReply(tx.QueryRow(ctx, replySelect+` WHERE reply_id = $1`, replyID))
	if err != nil {
		return Reply{}, unavailable("comments: re-read reply", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Reply{}, unavailable("comments: commit edit reply body", err)
	}
	return r, nil
}

func (s *PGStore) History(ctx context.Context, discussionID string) ([]BodyRevision, []StatusEvent, error) {
	if !s.ensure() {
		return nil, nil, ErrUnavailable
	}
	exists, err := discussionExists(ctx, s.pool, discussionID)
	if err != nil {
		return nil, nil, unavailable("comments: check discussion", err)
	}
	if !exists {
		return nil, nil, ErrNotFound
	}

	revRows, err := s.pool.Query(ctx, `
		SELECT br.target, br.target_id, br.old_body, br.new_body, br.editor, br.created_at
		FROM comment_body_revisions br
		WHERE (br.target = $1 AND br.target_id = $2)
		   OR (br.target = 'reply' AND br.target_id IN (
		        SELECT reply_id FROM comment_replies WHERE discussion_id = $2))
		ORDER BY br.revision_id ASC`,
		TargetDiscussion, discussionID)
	if err != nil {
		return nil, nil, unavailable("comments: list body revisions", err)
	}
	defer revRows.Close()
	var revs []BodyRevision
	for revRows.Next() {
		var r BodyRevision
		if err := revRows.Scan(&r.Target, &r.TargetID, &r.OldBody, &r.NewBody, &r.Editor, &r.At); err != nil {
			return nil, nil, unavailable("comments: scan body revision", err)
		}
		revs = append(revs, r)
	}
	if err := revRows.Err(); err != nil {
		return nil, nil, unavailable("comments: iterate body revisions", err)
	}

	evRows, err := s.pool.Query(ctx, `
		SELECT from_status, to_status, reason, actor, created_at
		FROM comment_status_events WHERE discussion_id = $1 ORDER BY event_id ASC`, discussionID)
	if err != nil {
		return nil, nil, unavailable("comments: list status events", err)
	}
	defer evRows.Close()
	var events []StatusEvent
	for evRows.Next() {
		var e StatusEvent
		var from *string
		if err := evRows.Scan(&from, &e.To, &e.Reason, &e.Actor, &e.At); err != nil {
			return nil, nil, unavailable("comments: scan status event", err)
		}
		e.From = derefStr(from)
		events = append(events, e)
	}
	if err := evRows.Err(); err != nil {
		return nil, nil, unavailable("comments: iterate status events", err)
	}
	return revs, events, nil
}

// Compile-time interface checks.
var _ Store = (*PGStore)(nil)
var _ Store = (*MemStore)(nil)
