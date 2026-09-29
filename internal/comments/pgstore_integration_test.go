//go:build integration

package comments

// pgstore_integration_test.go — incremental-sync (?since) semantics of
// PGStore.ListDiscussions against a live PostgreSQL (QATLAS_TEST_PG_DSN;
// skipped when unset). Mirrors TestMemStoreSince plus a same-millisecond
// UPDATE case that PG's now() truncation makes easy to hit.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pgTestStore(t *testing.T) (*PGStore, context.Context) {
	t.Helper()
	if !testutil.IntegrationEnabled {
		t.Skip("requires -tags integration and explicit live-service environment variables")
	}
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping live-PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := registry.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewPGStore(pool), ctx
}

func cleanupDiscussions(t *testing.T, s *PGStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, err := s.pool.Exec(context.Background(),
			`DELETE FROM comment_discussions WHERE discussion_id = $1`, id)
		if err != nil {
			t.Fatalf("cleanup %s: %v", id, err)
		}
	}
}

// seedParseRevision inserts the papers → paper_sources →
// parse_revisions chain the 00009 FK requires, and returns a cleanup.
func seedParseRevision(t *testing.T, s *PGStore, ctx context.Context, paper, revision string) {
	t.Helper()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO papers (paper_id, arxiv_id) VALUES ($1, $2)
		ON CONFLICT (paper_id) DO NOTHING`, paper, "since-integration-"+paper)
	if err != nil {
		t.Fatalf("seed paper: %v", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO paper_sources (source_id, paper_id, origin, sha256, objstore_key, size_bytes)
		VALUES ($1, $2, 'integration-test', $3, 'test', 1)
		ON CONFLICT (source_id) DO NOTHING`, "src_"+paper, paper, sha256Hex(paper))
	if err != nil {
		t.Fatalf("seed paper_source: %v", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO parse_revisions (revision_id, paper_id, source_id, schema, schema_version, artifact_sha256, objstore_key)
		VALUES ($1, $2, $3, 'middle', 'v1', $4, 'test')
		ON CONFLICT (revision_id) DO NOTHING`, revision, paper, "src_"+paper, sha256Hex(revision))
	if err != nil {
		t.Fatalf("seed parse_revision: %v", err)
	}
	t.Cleanup(func() {
		// ON DELETE CASCADE down paper_sources; papers last.
		_, _ = s.pool.Exec(context.Background(),
			`DELETE FROM papers WHERE paper_id = $1`, paper)
	})
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestIntegrationSinceWindow walks the pull-sync contract on PG:
// untouched rows stay out, bumped/new rows come back, the
// same-millisecond id tiebreak holds, and the row-value comparison
// stays correct across the window edge.
func TestIntegrationSinceWindow(t *testing.T) {
	s, ctx := pgTestStore(t)

	paper := "qa_since_it"
	// Defensive clean slate in case a prior failed run left rows behind.
	_, _ = s.pool.Exec(ctx, `DELETE FROM comment_discussions WHERE paper_id = $1`, paper)
	seedParseRevision(t, s, ctx, paper, "rev-a")
	mk := func(body string) Discussion {
		t.Helper()
		d, err := s.CreateDiscussion(ctx, CreateDiscussionInput{
			PaperID: paper, ParseRevision: "rev-a", PageIdx: 0, BlockIndex: 2,
			Scope: ScopePublic, Body: body, CreatedBy: "u",
		})
		if err != nil {
			t.Fatalf("create %q: %v", body, err)
		}
		t.Cleanup(func() { cleanupDiscussions(t, s, d.DiscussionID) })
		return d
	}

	stale := mk("untouched")
	moved := mk("gets edited after the first poll")

	// First poll snapshot: everything at/after the very beginning.
	first, err := s.ListDiscussions(ctx, ListFilter{PaperID: paper, Limit: 100})
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first list = %d items, want 2", len(first))
	}

	// Bump `moved` — its updated_at moves past both creation stamps.
	if _, err := s.UpdateDiscussionBody(ctx, moved.DiscussionID, 1, "edited body", "u"); err != nil {
		t.Fatalf("edit moved: %v", err)
	}
	fresh := mk("created after the first poll")

	// Position strictly after the newest row of the first poll → only
	// the edited thread plus the new one.
	pos := EncodeSinceCursor(first[0].UpdatedAt, first[0].DiscussionID)
	sinceT, sinceID, err := DecodeSinceCursor(pos)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	delta, err := s.ListDiscussions(ctx, ListFilter{PaperID: paper, Since: sinceT, SinceID: sinceID, Limit: 100})
	if err != nil {
		t.Fatalf("delta list: %v", err)
	}
	got := map[string]bool{}
	for _, d := range delta {
		got[d.DiscussionID] = true
	}
	if len(delta) != 2 || !got[moved.DiscussionID] || !got[fresh.DiscussionID] {
		t.Fatalf("delta = %v, want exactly {moved, fresh}", got)
	}
	if got[stale.DiscussionID] {
		t.Errorf("untouched discussion %s leaked into the delta", stale.DiscussionID)
	}

	// Quiet roundtrip: a cursor minted from the newest row — PG now()
	// carries microseconds the cursor cannot — must not re-emit it
	// (the comparison floors both sides to millispace).
	rtT, rtID, err := DecodeSinceCursor(EncodeSinceCursor(fresh.UpdatedAt, fresh.DiscussionID))
	if err != nil {
		t.Fatalf("decode roundtrip cursor: %v", err)
	}
	quiet, err := s.ListDiscussions(ctx, ListFilter{PaperID: paper, Since: rtT, SinceID: rtID, Limit: 100})
	if err != nil {
		t.Fatalf("quiet list: %v", err)
	}
	if len(quiet) != 0 {
		t.Fatalf("cursor from newest row re-emits %d rows, want a quiet window", len(quiet))
	}

	// Same-millisecond tiebreak: pin two rows into one millisecond and
	// resume from between them — the earlier id must stay excluded, the
	// later must come back (no same-ms skips).
	pinned := time.UnixMilli(time.Now().UnixMilli())
	for _, id := range []string{stale.DiscussionID, moved.DiscussionID} {
		if _, err := s.pool.Exec(ctx,
			`UPDATE comment_discussions SET updated_at = $2 WHERE discussion_id = $1`,
			id, pinned); err != nil {
			t.Fatalf("pin %s: %v", id, err)
		}
	}
	tie, err := s.ListDiscussions(ctx, ListFilter{
		PaperID: paper, Since: pinned, SinceID: stale.DiscussionID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("tiebreak list: %v", err)
	}
	if len(tie) != 1 || tie[0].DiscussionID != moved.DiscussionID {
		t.Fatalf("same-ms tiebreak = %d rows, want only %s", len(tie), moved.DiscussionID)
	}

	// Scope filter composes with since inside the same window.
	leanOnly, err := s.ListDiscussions(ctx, ListFilter{
		PaperID: paper, Scopes: []string{ScopeLean}, Since: sinceT, SinceID: sinceID, Limit: 100,
	})
	if err != nil || len(leanOnly) != 0 {
		t.Fatalf("since+scope(lean) = %d rows err=%v, want 0 (all rows are public)", len(leanOnly), err)
	}
}

// TestIntegrationSinceExplain records the query plan for the since
// window (plan §13.4.5: reuse the existing keyset index prefix, no new
// index unless proven necessary). It asserts the planner picks the
// paper index and logs the EXPLAIN output for the record.
func TestIntegrationSinceExplain(t *testing.T) {
	s, ctx := pgTestStore(t)
	rows, err := s.pool.Query(ctx, `
		EXPLAIN (COSTS OFF) `+discussionSelect+`
		WHERE paper_id = $1 AND (date_trunc('milliseconds', updated_at), discussion_id) > ($2, $3)
		ORDER BY discussion_id DESC LIMIT 20`,
		"qa_since_explain", time.UnixMilli(0), "")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	plan := strings.Join(lines, "\n")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	t.Logf("EXPLAIN since-window:\n%s", plan)
	if !strings.Contains(plan, "comment_discussions_paper_idx") {
		t.Errorf("plan does not use comment_discussions_paper_idx:\n%s", plan)
	}
}
