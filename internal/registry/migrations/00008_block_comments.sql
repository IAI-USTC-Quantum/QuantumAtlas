-- +goose Up
-- Block-level comments (plan §12.3, migration 00008 / Q2).
--
-- Four tables:
--   comment_discussions   one root comment = one discussion bound to a
--                         permanent anchor (paper_id + parse_revision +
--                         page_idx + block_index, plan §4.2). A block may
--                         carry many independent discussions.
--   comment_replies       threaded-free flat replies belonging to one
--                         discussion.
--   comment_status_events append-only status history (pending/confirmed/
--                         retracted, plan §12.1.4; reopen allowed, reason
--                         mandatory on every change).
--   comment_body_revisions append-only body edit history for discussions
--                         and replies alike (old/new/editor, plan §12.1.2).
--
-- Anchor note: parse_revision deliberately has NO foreign key into the Q1
-- parse_revisions table yet (00007 lands on the Q1 branch; Q2 develops
-- fixture-first per plan §12.5). Existence validation is enforced at the
-- API layer via the comments.AnchorValidator hook and will be tightened to
-- a real FK during Q1→Q2 integration.
--
-- identity prefixes: discussion_id = "cd_" + lowercase ULID,
-- reply_id = "cr_" + lowercase ULID (self-describing, same convention as
-- qa_ paper ids; ULIDs sort by creation time which the keyset cursors
-- rely on).

CREATE TABLE IF NOT EXISTS comment_discussions (
    discussion_id text PRIMARY KEY,
    paper_id      text NOT NULL,
    parse_revision text NOT NULL,
    page_idx      integer NOT NULL CHECK (page_idx >= 0),
    block_index   integer NOT NULL CHECK (block_index >= 1),
    type          text NOT NULL DEFAULT 'normal',
    scope         text NOT NULL DEFAULT 'public' CHECK (scope IN ('public', 'lean')),
    status        text CHECK (status IN ('pending', 'confirmed', 'retracted')),
    body          text NOT NULL,
    created_by    text NOT NULL,
    model         text,
    revision      integer NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Keyset listing per paper (newest first: ULID desc = created desc).
CREATE INDEX IF NOT EXISTS comment_discussions_paper_idx
    ON comment_discussions (paper_id, discussion_id DESC);
-- Anchor drill-down (block panel: all discussions on one block).
CREATE INDEX IF NOT EXISTS comment_discussions_anchor_idx
    ON comment_discussions (paper_id, parse_revision, page_idx, block_index);

CREATE TABLE IF NOT EXISTS comment_replies (
    reply_id      text PRIMARY KEY,
    discussion_id text NOT NULL REFERENCES comment_discussions(discussion_id) ON DELETE CASCADE,
    body          text NOT NULL,
    created_by    text NOT NULL,
    model         text,
    revision      integer NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS comment_replies_discussion_idx
    ON comment_replies (discussion_id, reply_id);

CREATE TABLE IF NOT EXISTS comment_status_events (
    event_id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    discussion_id text NOT NULL REFERENCES comment_discussions(discussion_id) ON DELETE CASCADE,
    from_status   text,
    to_status     text NOT NULL CHECK (to_status IN ('pending', 'confirmed', 'retracted')),
    reason        text NOT NULL,
    actor         text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS comment_status_events_discussion_idx
    ON comment_status_events (discussion_id, event_id);

CREATE TABLE IF NOT EXISTS comment_body_revisions (
    revision_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    target      text NOT NULL CHECK (target IN ('discussion', 'reply')),
    target_id   text NOT NULL,
    old_body    text NOT NULL,
    new_body    text NOT NULL,
    editor      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS comment_body_revisions_target_idx
    ON comment_body_revisions (target, target_id, revision_id);

-- +goose Down
DROP TABLE IF EXISTS comment_body_revisions;
DROP TABLE IF EXISTS comment_status_events;
DROP TABLE IF EXISTS comment_replies;
DROP TABLE IF EXISTS comment_discussions;
