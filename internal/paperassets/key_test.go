package paperassets

import (
	"path/filepath"
	"strings"
	"testing"
)

// AssetKey is the same logical thing as AssetPath but minus the
// rawRoot prefix. The contract we lock in:
//
//   - kind drives the suffix (pdf → .pdf, markdown → .md, json → .json,
//     images → "" — directory key, no suffix);
//   - the storage-key sharding (first 4 digits of stem) is identical
//     to AssetPath;
//   - forward slashes only (never the platform separator), so the
//     return value can be handed to objstore.Store directly.
//
// If this drifts from AssetPath the local + S3 backends would write
// objects to different paths for the same paper, and that's the kind of
// silent corruption Phase 3 specifically aims to prevent. The test
// asserts byte-equality between the two functions for every kind.
func TestAssetKey_MatchesAssetPathStripped(t *testing.T) {
	cases := []struct {
		name    string
		arxivID string
		kind    string
		want    string
	}{
		{"new style pdf", "2401.00001v1", "pdf", "pdf/2401/2401.00001v1.pdf"},
		{"new style markdown", "2401.00001v1", "markdown", "markdown/2401/2401.00001v1.md"},
		{"new style json", "2401.00001v1", "json", "json/2401/2401.00001v1.json"},
		{"new style images dir", "2401.00001v1", "images", "images/2401/2401.00001v1"},
		{"old style pdf drops category", "quant-ph/9508027v1", "pdf", "pdf/9508/9508027v1.pdf"},
		{"unknown kind returns empty", "2401.00001v1", "garbage", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AssetKey(c.kind, c.arxivID)
			if got != c.want {
				t.Errorf("AssetKey(%q, %q) = %q, want %q", c.kind, c.arxivID, got, c.want)
			}
			// Cross-check against AssetPath: stripping the rawRoot prefix
			// must yield exactly the same key (with forward slashes).
			if c.want == "" {
				return
			}
			rawRoot := "/srv/raw"
			pathGot := AssetPath(rawRoot, c.kind, c.arxivID)
			rel := filepath.ToSlash(strings.TrimPrefix(pathGot, rawRoot+"/"))
			if rel != got {
				t.Errorf("AssetPath/AssetKey drift: path=%q -> rel=%q, key=%q",
					pathGot, rel, got)
			}
		})
	}
}

func TestAssetKey_AlwaysForwardSlashes(t *testing.T) {
	// Even on Windows, the returned key must never embed backslashes —
	// objstore keys are S3-style strings, and the LocalStore handles
	// FromSlash at use-time.
	for _, kind := range []string{"pdf", "markdown", "json", "images"} {
		got := AssetKey(kind, "quant-ph/9508027v1")
		if strings.ContainsRune(got, '\\') {
			t.Errorf("AssetKey(%q, ...) contains backslash: %q", kind, got)
		}
	}
}
