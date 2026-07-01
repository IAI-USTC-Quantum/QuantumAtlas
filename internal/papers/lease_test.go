package papers

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// Static-string guards over the MinerU lease SQL (renamed from the old
// "claim" — the word "claim" now denotes lean's bibliographic/theorem
// claims, ADR 0007/0008). Behavioural coverage is in integration_test.go.

func readLeaseFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := file[:strings.LastIndex(file, "/")+1]
	src, err := os.ReadFile(dir + "lease.go")
	if err != nil {
		t.Fatalf("read lease.go: %v", err)
	}
	return string(src)
}

func locateLeaseFunc(t *testing.T, sig string) string {
	t.Helper()
	src := readLeaseFile(t)
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatalf("signature %q not found in lease.go", sig)
	}
	return src[start:findClosingBrace(src, start)]
}

// findClosingBrace returns the index just past the '}' that closes the
// brace opened at or after startIdx.
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

// TestLeaseLocksDefaultAsset guards that Lease targets the paper's default
// asset with a row lock, and only when that asset has a PDF but no markdown.
func TestLeaseLocksDefaultAsset(t *testing.T) {
	fn := locateLeaseFunc(t, "func (s *Store) Lease(")
	for _, must := range []string{
		"paper_default_asset_id",
		"FOR UPDATE",
		"UPDATE paper_assets",
		"lease_id = $4",
	} {
		if !strings.Contains(fn, must) {
			t.Errorf("Lease SQL missing %q", must)
		}
	}
}

// TestReleaseLeaseShape guards ReleaseLease: a matching-id clear plus a
// "different active lease?" check (the 409 path), both via the default
// asset.
func TestReleaseLeaseShape(t *testing.T) {
	fn := locateLeaseFunc(t, "func (s *Store) ReleaseLease(")
	if strings.Count(fn, "paper_default_asset_id") < 2 {
		t.Error("ReleaseLease must resolve the default asset in both the clear and the active-lease check")
	}
	if !strings.Contains(fn, "UPDATE paper_assets") {
		t.Error("ReleaseLease must UPDATE paper_assets to clear the lease")
	}
	if !strings.Contains(fn, "lease_id IS NOT NULL") {
		t.Error("ReleaseLease must check for a different active lease (lease_id IS NOT NULL + not expired)")
	}
}

// TestGCExpiredLeasesTargetsAssets guards the expired-lease sweep.
func TestGCExpiredLeasesTargetsAssets(t *testing.T) {
	fn := locateLeaseFunc(t, "func (s *Store) GCExpiredLeases(")
	if !strings.Contains(fn, "UPDATE paper_assets") {
		t.Error("GCExpiredLeases must sweep paper_assets")
	}
	if !strings.Contains(fn, "lease_expires_at < now()") {
		t.Error("GCExpiredLeases must clear only expired leases")
	}
}
