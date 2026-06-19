package papers

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestQueryStatsExcludesDOINodes asserts that the QueryStats Cypher query
// carries the identifier_scheme filter that excludes DOI-indexed nodes
// from the dashboard counters. This is a static-string regression test
// for the PR #19 follow-up — we can't run the query without a live
// Neo4j, but we can guard against accidental removal of the filter
// clause.
func TestQueryStatsExcludesDOINodes(t *testing.T) {
	fn := locateStoreFunc(t, "QueryStats")
	if !strings.Contains(fn, "identifier_scheme") {
		t.Errorf("QueryStats is missing the identifier_scheme filter for DOI nodes; " +
			"PR #19 follow-up requires excluding DOI papers from catalog stats")
	}
	if !strings.Contains(fn, "<> 'doi'") && !strings.Contains(fn, "!= 'doi'") {
		t.Errorf("QueryStats filter should compare identifier_scheme to 'doi' (use <> or !=)")
	}
}

// TestNeedsMineruExcludesDOINodes guards the NeedsMineru Cypher query
// for the same reason. NeedsMineru feeds the mineru worker queue, and
// queueing a DOI-only paper would cause the worker to look for an
// arxiv-id PDF that doesn't exist.
func TestNeedsMineruExcludesDOINodes(t *testing.T) {
	fn := locateStoreFunc(t, "NeedsMineru")
	if !strings.Contains(fn, "identifier_scheme") {
		t.Errorf("NeedsMineru is missing the identifier_scheme filter for DOI nodes")
	}
	if !strings.Contains(fn, "<> 'doi'") && !strings.Contains(fn, "!= 'doi'") {
		t.Errorf("NeedsMineru filter should compare identifier_scheme to 'doi' (use <> or !=)")
	}
}

// locateStoreFunc returns the source text of the named (s *Store) method
// from store.go. Used to assert on the Cypher string content of a
// function body without actually executing it against a live Neo4j.
func locateStoreFunc(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := file[:strings.LastIndex(file, "/")+1]
	src, err := os.ReadFile(dir + "store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	return extractFuncBody(string(src), "func (s *Store) "+name+"(")
}

// extractFuncBody slices src between the given signature and the
// matching closing brace. Tracks brace depth; ignores string literals
// — good enough for the Cypher queries in store.go, which don't nest
// unbalanced braces inside backtick strings.
func extractFuncBody(src, sig string) string {
	start := strings.Index(src, sig)
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return ""
}
