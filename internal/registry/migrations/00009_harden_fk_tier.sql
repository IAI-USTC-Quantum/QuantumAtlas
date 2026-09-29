-- +goose Up
-- Backend hardening (plan docs/plans/qatlas-block-comments.md §12 Q0
-- follow-up): turn comment_discussions.parse_revision from a plain
-- string into a real foreign key, and record the parse tier on
-- parse_revisions so the combined-read anchor can emit a readable
-- MinerU-style locator (plan §5.1).
--
-- Two changes, one migration (numbering was pre-allocated in §12.3;
-- 00009 is the first post-integration hardening slot):
--
--   1. parse_revisions.tier: TEXT NOT NULL DEFAULT 'standard'.
--      MinerU tiers observed in the wild: standard / lite / premium
--      (parse_svc --tier). Old rows backfill to 'standard' via the
--      column default; new parses write the actual tier at ingest.
--
--   2. comment_discussions.parse_revision → parse_revisions(revision_id)
--      ON DELETE RESTRICT. Until now existence was only enforced at
--      the API layer (comments.AnchorValidator, 00008's deliberate
--      fixture-first compromise). The FK makes the database agree:
--      a revision with discussions can never be deleted, and a
--      discussion can never name a revision that does not exist.
--
-- Orphan safety: 00008 rows were all created through the validated
-- Q2 API, so no orphans should exist — but the ADD CONSTRAINT still
-- verifies first (a plain ADD CONSTRAINT fails the migration on
-- orphans, which is the honest outcome: fix the data, then migrate).

-- 1. tier column (backfills every existing row to 'standard').
ALTER TABLE parse_revisions
    ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'standard';

-- 2. real FK on the discussion anchor. NOT VALID is intentionally
--    avoided: the constraint is validated over existing rows, and a
--    failure names the orphans instead of silently trusting them.
ALTER TABLE comment_discussions
    ADD CONSTRAINT comment_discussions_parse_revision_fk
    FOREIGN KEY (parse_revision) REFERENCES parse_revisions(revision_id)
    ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE comment_discussions
    DROP CONSTRAINT IF EXISTS comment_discussions_parse_revision_fk;
ALTER TABLE parse_revisions
    DROP COLUMN IF EXISTS tier;
