package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationAdoptPendingDownloadRequests(t *testing.T) {
	admin, ctx := testPool(t)
	schema := fmt.Sprintf("download_adoption_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg := admin.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool)
	if _, err := pool.Exec(ctx, `INSERT INTO papers(paper_id,doi,title,authors,status) VALUES
 ('a','10.5555/adopt-a','Already finished',ARRAY['Author A'],'pending'),
 ('b','10.5555/adopt-b','Legacy pending B',ARRAY['Author B'],'pending'),
 ('c','10.5555/adopt-c','Legacy pending C',ARRAY['Author C'],'pending');
 INSERT INTO downloader_requests(paper_id,request_id,input,kind,ref,state)
 VALUES('a','keep-this-generation','10.5555/adopt-a','doi','{"DOI":"10.5555/adopt-a"}','failed')`); err != nil {
		t.Fatal(err)
	}
	// LIMIT applies after excluding existing journal rows; otherwise 'a' would
	// indefinitely starve both fresh admissions on every recovery pass.
	for i := 0; i < 3; i++ {
		if err := s.AdoptPendingDownloadRequests(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	var state, requestID string
	if err := pool.QueryRow(ctx, `SELECT state,request_id FROM downloader_requests WHERE paper_id='a'`).Scan(&state, &requestID); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || requestID != "keep-this-generation" {
		t.Fatalf("terminal admission reset: %q %q", state, requestID)
	}
	for _, id := range []string{"b", "c"} {
		var kind, input string
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT state,request_id,kind,input,ref FROM downloader_requests WHERE paper_id=$1`, id).Scan(&state, &requestID, &kind, &input, &raw); err != nil {
			t.Fatal(err)
		}
		var ref PaperRef
		if err := json.Unmarshal(raw, &ref); err != nil {
			t.Fatal(err)
		}
		if state != "queued" || requestID == "" || kind != "doi" || input != "10.5555/adopt-"+id || ref.DOI != input || len(ref.Authors) != 1 {
			t.Fatalf("bad adopted request: state=%s id=%s kind=%s input=%s ref=%+v", state, requestID, kind, input, ref)
		}
	}
}
