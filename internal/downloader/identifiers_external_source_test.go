package downloader

import (
	"errors"
	"testing"
)

// Identifier-only fetch must not invent a DOI/arXiv identity for external
// originals. Issue #26 is implemented by the explicit source-register API,
// which requires bibliographic metadata and verified immutable PDF provenance.
func TestExternalSourceRegistrationBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://eprint.iacr.org/2026/1591",
		"https://eprint.iacr.org/2026/1693",
		"https://csd.cs.cmu.edu/sites/default/files/phd-thesis/CMU-CS-25-148.pdf",
	} {
		t.Run(raw, func(t *testing.T) {
			id, err := ParseIdentifier(raw)
			var unsupported *UnsupportedInputError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %v; expected unsupported external source", err)
			}
			if id.Ref.DOI != "" || id.Ref.ArxivID != "" || id.Ref.OpenAlexID != "" {
				t.Fatalf("unsupported source was assigned an invented identity: %+v", id.Ref)
			}
		})
	}
}
