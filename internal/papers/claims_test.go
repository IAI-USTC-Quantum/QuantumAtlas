package papers

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// These tests are static-string guards for the PR #19 follow-up: the
// Cypher queries in claims.go (Claim, classifyClaimFailure, the two
// sites in ReleaseClaim) must exclude DOI-indexed nodes so that the
// MinerU claim machinery never tries to claim a published-version
// contribution. We can't run the queries without a live Neo4j, so we
// assert on the source text of each method body.

func TestClaimExcludesDOINodes(t *testing.T) {
	fn := locateClaimsFunc(t, "func (s *Store) Claim(")
	if !strings.Contains(fn, "identifier_scheme") {
		t.Errorf("Claim is missing the identifier_scheme filter for DOI nodes; " +
			"a MinerU claim against a DOI paper would look for a missing arxiv PDF")
	}
}

func TestClassifyClaimFailureExcludesDOINodes(t *testing.T) {
	fn := locateClaimsFunc(t, "func (s *Store) classifyClaimFailure(")
	if !strings.Contains(fn, "identifier_scheme") {
		t.Errorf("classifyClaimFailure is missing the identifier_scheme filter for DOI nodes")
	}
}

func TestReleaseClaimExcludesDOINodes(t *testing.T) {
	// ReleaseClaim has TWO Cypher sites: the matching-id removal and
	// the "is there a different active lease?" check. Both must
	// exclude DOI nodes.
	src := readClaimsFile(t)

	sig := "func (s *Store) ReleaseClaim("
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatal("ReleaseClaim not found in claims.go")
	}
	end := findClosingBrace(src, start)
	body := src[start:end]

	if c := strings.Count(body, "identifier_scheme"); c < 2 {
		t.Errorf("ReleaseClaim has %d identifier_scheme clauses, want >= 2 "+
			"(matching-id removal + 'different active lease' check)", c)
	}
}

func TestReleaseClaimChecksCount(t *testing.T) {
	// ReleaseClaim must do BOTH a matching-id removal and a "is there
	// a different active lease?" check (the second is the 409 path).
	// We assert the pattern: after the first REMOVE, there should be
	// a second MATCH that looks for a *different* active claim.
	src := readClaimsFile(t)
	sig := "func (s *Store) ReleaseClaim("
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatal("ReleaseClaim not found")
	}
	end := findClosingBrace(src, start)
	body := src[start:end]

	removes := strings.Count(body, "REMOVE")
	matches := strings.Count(body, "MATCH (p:PaperWork")
	if removes < 1 || matches < 2 {
		t.Errorf("ReleaseClaim shape: removes=%d, matches=%d, want removes>=1 and matches>=2",
			removes, matches)
	}
}

func locateClaimsFunc(t *testing.T, sig string) string {
	t.Helper()
	src := readClaimsFile(t)
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatalf("signature %q not found in claims.go", sig)
	}
	end := findClosingBrace(src, start)
	return src[start:end]
}

func readClaimsFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := file[:strings.LastIndex(file, "/")+1]
	src, err := os.ReadFile(dir + "claims.go")
	if err != nil {
		t.Fatalf("read claims.go: %v", err)
	}
	return string(src)
}

// findClosingBrace returns the index of the '}' that closes the brace
// opened at or after startIdx. Naive depth counter; ignores braces
// inside string literals — fine for claims.go whose Cypher is in
// backtick strings without unbalanced braces.
func findClosingBrace(src string, startIdx int) int {
	depth := 0
	for i := startIdx; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(src)
}
