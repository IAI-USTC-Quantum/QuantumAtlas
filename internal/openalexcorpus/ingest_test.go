package openalexcorpus

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// ingestSource returns the text of ingest.go so the inline upsert SQL can
// be guarded without a live PostgreSQL (same approach as internal/papers'
// store_test.go).
func ingestSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := file[:strings.LastIndex(file, "/")+1]
	src, err := os.ReadFile(dir + "ingest.go")
	if err != nil {
		t.Fatalf("read ingest.go: %v", err)
	}
	return string(src)
}

// TestUpsertWorksSQL guards the works upsert: verbatim jsonb via the
// text[]::jsonb[] unnest cast, and ON CONFLICT DO UPDATE (idempotent
// re-ingest).
func TestUpsertWorksSQL(t *testing.T) {
	src := ingestSource(t)
	for _, must := range []string{
		"INSERT INTO openalex_works",
		"unnest($1::text[], $2::text[]::jsonb[], $3::text[])",
		"ON CONFLICT (openalex_id) DO UPDATE",
		"record = EXCLUDED.record",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("UpsertWorks SQL missing %q", must)
		}
	}
}
