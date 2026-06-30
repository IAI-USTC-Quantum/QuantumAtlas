package theoremsplugin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleRegistry = `{
  "version": 1,
  "generated_ts": "2026-06-19T03:16:55Z",
  "narrative_intro": "intro",
  "families": [
    {"id": "foundations", "name": "Foundations", "description": "kernel"},
    {"id": "hamiltonian-simulation", "name": "Hamiltonian simulation", "description": "ham"}
  ],
  "theorems": [
    {"lean_fqn": "L1_norm", "file": "lean/CBMD/BlockEncoding.lean", "kind": "theorem",
     "family_id": "hamiltonian-simulation", "statement_paraphrase": "norm eq",
     "unit_id": "existing:L1_norm", "sorry_free": true, "depends_on": [],
     "axioms_used": [], "audit_status": "verified"},
    {"lean_fqn": "circleIntegralMatrix", "file": "lean/CBMD/MatrixResidue.lean", "kind": "definition",
     "family_id": "hamiltonian-simulation", "statement_paraphrase": "contour integral",
     "unit_id": "existing:circleIntegralMatrix", "sorry_free": true, "depends_on": ["L1_norm"],
     "axioms_used": ["propext"], "audit_status": "verified"},
    {"lean_fqn": "grover_complete", "file": "lean/Grover.lean", "kind": "theorem",
     "family_id": "oracular", "statement_paraphrase": "grover", "unit_id": "existing:grover_complete",
     "sorry_free": false, "depends_on": [], "axioms_used": [], "audit_status": "verified"}
  ]
}`

const sampleCertified = `{
  "version": 1,
  "audit_pipeline": "v1",
  "generated_ts": "2026-06-22T04:10:30Z",
  "certified": {
    "existing:L1_norm": {
      "unit_id": "existing:L1_norm", "result": "confirmed", "certified": true,
      "fqns": ["L1_norm"], "kernel": "pass", "claim_faithfulness": "FAITHFUL",
      "magi": {"faithful": 3, "unfaithful": 0, "abstain": 0}, "audited_ts": "2026-06-22T04:09:29Z"
    },
    "existing:circleIntegralMatrix": {
      "unit_id": "existing:circleIntegralMatrix", "result": "confirmed", "certified": true,
      "kernel": "pass", "claim_faithfulness": "FAITHFUL",
      "magi": {"faithful": 1, "unfaithful": 0, "abstain": 0, "note": "single-reviewer manual certification"}
    }
  }
}`

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "artifacts", "registry.json"), sampleRegistry)
	mustWrite(t, filepath.Join(dir, "artifacts", "audit_records", "certified.json"), sampleCertified)
	mustWrite(t, filepath.Join(dir, "lean", "CBMD", "BlockEncoding.lean"), "theorem L1_norm : True := trivial\n")
	return dir
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTestCache(t *testing.T, dir string) *Cache {
	t.Helper()
	c := NewCache(dir, time.Hour) // long ticker; we drive refresh manually
	t.Cleanup(c.Stop)
	if !c.Ready() {
		t.Fatal("cache not ready after NewCache")
	}
	return c
}

func TestCacheParsesRegistryAndCertified(t *testing.T) {
	c := newTestCache(t, writeFixture(t))

	all := c.List(ListFilter{})
	if len(all) != 3 {
		t.Fatalf("List() = %d theorems, want 3", len(all))
	}
	// Sorted by lean_fqn: L1_norm, circleIntegralMatrix, grover_complete.
	if all[0].LeanFQN != "L1_norm" {
		t.Fatalf("first = %q, want L1_norm (sorted)", all[0].LeanFQN)
	}

	fams := c.Families()
	if len(fams) != 2 {
		t.Fatalf("Families() = %d, want 2", len(fams))
	}

	if cert, ok := c.CertifiedFor("existing:L1_norm"); !ok || cert.ClaimFaithfulness != "FAITHFUL" {
		t.Fatalf("CertifiedFor(L1_norm) = (%+v, %v), want FAITHFUL hit", cert, ok)
	}
	// A magi block carrying a free-text "note" alongside int counts must still
	// decode (Magi is map[string]any, decoded per-entry).
	if cert, ok := c.CertifiedFor("existing:circleIntegralMatrix"); !ok {
		t.Fatal("CertifiedFor(circleIntegralMatrix) should hit despite mixed-type magi")
	} else if cert.Magi["note"] == nil {
		t.Fatalf("mixed magi note dropped: %+v", cert.Magi)
	}
	if _, ok := c.CertifiedFor("existing:grover_complete"); ok {
		t.Fatal("grover should have no certified entry")
	}
}

func TestCacheListFilters(t *testing.T) {
	c := newTestCache(t, writeFixture(t))

	if got := c.List(ListFilter{FamilyID: "oracular"}); len(got) != 1 || got[0].LeanFQN != "grover_complete" {
		t.Fatalf("family filter = %+v, want [grover_complete]", got)
	}
	if got := c.List(ListFilter{Kind: "definition"}); len(got) != 1 || got[0].LeanFQN != "circleIntegralMatrix" {
		t.Fatalf("kind filter = %+v, want [circleIntegralMatrix]", got)
	}
	if got := c.List(ListFilter{FamilyID: "hamiltonian-simulation", Kind: "theorem"}); len(got) != 1 {
		t.Fatalf("combined filter = %d, want 1", len(got))
	}
	if got := c.List(ListFilter{AuditStatus: "refuted"}); len(got) != 0 {
		t.Fatalf("no-match filter = %d, want 0", len(got))
	}
}

func TestCacheStats(t *testing.T) {
	c := newTestCache(t, writeFixture(t))
	st := c.Stats()
	if st.Total != 3 || st.SorryFree != 2 {
		t.Fatalf("stats total/sorry_free = %d/%d, want 3/2", st.Total, st.SorryFree)
	}
	if st.ByKind["theorem"] != 2 || st.ByKind["definition"] != 1 {
		t.Fatalf("by_kind = %v, want theorem:2 definition:1", st.ByKind)
	}
	if st.Certified != 2 {
		t.Fatalf("certified = %d, want 2", st.Certified)
	}
}

func TestCacheSourceReadsFile(t *testing.T) {
	c := newTestCache(t, writeFixture(t))
	rel, src, err := c.Source("L1_norm")
	if err != nil {
		t.Fatalf("Source(L1_norm): %v", err)
	}
	if rel != filepath.FromSlash("lean/CBMD/BlockEncoding.lean") {
		t.Fatalf("rel = %q", rel)
	}
	if src == "" {
		t.Fatal("source empty")
	}
}

func TestCacheSourceRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	// A poisoned registry whose `file` escapes the checkout root.
	mustWrite(t, filepath.Join(dir, "artifacts", "registry.json"), `{
	  "theorems": [{"lean_fqn": "evil", "file": "../../etc/passwd", "kind": "theorem",
	    "unit_id": "x:evil", "audit_status": "verified"}]
	}`)
	c := newTestCache(t, dir)
	if _, _, err := c.Source("evil"); err == nil {
		t.Fatal("Source must reject a `..`-escaping path")
	}
}

func TestCacheUnknownFQN(t *testing.T) {
	c := newTestCache(t, writeFixture(t))
	if _, ok := c.Find("nope"); ok {
		t.Fatal("Find(nope) should miss")
	}
	if _, _, err := c.Source("nope"); err == nil {
		t.Fatal("Source(nope) should error")
	}
}

func TestCacheMissingRegistryDegrades(t *testing.T) {
	// No registry.json on disk: initial load fails, cache stays not-ready,
	// accessors return empty rather than panicking.
	c := NewCache(t.TempDir(), time.Hour)
	t.Cleanup(c.Stop)
	if c.Ready() {
		t.Fatal("cache should not be ready with no registry.json")
	}
	if got := c.List(ListFilter{}); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
	if st := c.Stats(); st.Total != 0 {
		t.Fatalf("Stats().Total = %d, want 0", st.Total)
	}
}
