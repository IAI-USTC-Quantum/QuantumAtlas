package papers

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// These are static-string guards over the store SQL — cheap regression
// tests that don't need a live PostgreSQL. Behavioural coverage of the
// two-table catalog lives in integration_test.go (gated on
// QATLAS_TEST_PG_DSN).

// TestQueryStatsTargetsArxivAssets asserts QueryStats counts arXiv assets
// (source='arxiv') from paper_assets, so published-only DOI contributions
// don't pollute the arXiv-paper dashboard.
func TestQueryStatsTargetsArxivAssets(t *testing.T) {
	fn := locateStoreFunc(t, "QueryStats")
	if !strings.Contains(fn, "paper_assets") {
		t.Error("QueryStats must aggregate over paper_assets")
	}
	if !strings.Contains(fn, "source = 'arxiv'") {
		t.Error("QueryStats must restrict to source='arxiv' (exclude published DOI contributions)")
	}
	if !strings.Contains(fn, "mineru_md_path IS NOT NULL") {
		t.Error("QueryStats has_md must derive from mineru_md_path IS NOT NULL")
	}
}

// TestNeedsMineruTargetsAssets guards NeedsMineru: the queue is arXiv
// assets with a PDF, no markdown, and no live lease.
func TestNeedsMineruTargetsAssets(t *testing.T) {
	fn := locateStoreFunc(t, "NeedsMineru")
	for _, must := range []string{
		"FROM paper_assets",
		"source = 'arxiv'",
		"mineru_md_path IS NULL",
		"lease_expires_at",
	} {
		if !strings.Contains(fn, must) {
			t.Errorf("NeedsMineru SQL missing %q", must)
		}
	}
}

// TestUpsertPDFTwoTable guards that UpsertPDF ensures the papers row and
// the arxiv paper_assets row in one CTE upsert.
func TestUpsertPDFTwoTable(t *testing.T) {
	fn := locateStoreFunc(t, "UpsertPDF")
	for _, must := range []string{
		"INSERT INTO papers (paper_arxiv_id)",
		"ON CONFLICT (paper_arxiv_id)",
		"INSERT INTO paper_assets",
		"'arxiv'",
		"ON CONFLICT (paper_id, source, arxiv_version)",
	} {
		if !strings.Contains(fn, must) {
			t.Errorf("UpsertPDF SQL missing %q", must)
		}
	}
}

// TestLookupArxivToDOISQL guards the arxiv->DOI twin lookup: it matches on
// papers.paper_arxiv_id and returns paper_doi (the same-paper DOI when one
// exists), so the GET dispatch can honour "DOI is canonical".
func TestLookupArxivToDOISQL(t *testing.T) {
	fn := locateDOIStoreFunc(t, "LookupArxivToDOI")
	if !strings.Contains(fn, "paper_arxiv_id = $1") {
		t.Error("LookupArxivToDOI must match on papers.paper_arxiv_id")
	}
	if !strings.Contains(fn, "paper_doi IS NOT NULL") {
		t.Error("LookupArxivToDOI must require a DOI twin (paper_doi IS NOT NULL)")
	}
	if !strings.Contains(fn, "SELECT paper_doi") {
		t.Error("LookupArxivToDOI must RETURN paper_doi so the GET dispatch can hand the DOI to the DOI handlers")
	}
}

// locateStoreFunc returns the source text of the named (s *Store) method
// from store.go. Used to assert on the SQL string content of a function
// body without executing it against a live PostgreSQL.
func locateStoreFunc(t *testing.T, name string) string {
	t.Helper()
	return readAndExtract(t, "store.go", name)
}

// locateDOIStoreFunc returns the source text of the named (s *Store)
// method from doi_store.go.
func locateDOIStoreFunc(t *testing.T, name string) string {
	t.Helper()
	return readAndExtract(t, "doi_store.go", name)
}

func readAndExtract(t *testing.T, filename, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := file[:strings.LastIndex(file, "/")+1]
	src, err := os.ReadFile(dir + filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	return extractFuncBody(string(src), "func (s *Store) "+name+"(")
}

// extractFuncBody slices src between the given signature and the matching
// closing brace. Tracks brace depth; good enough for the SQL queries in
// store.go, which don't nest unbalanced braces inside backtick strings.
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
