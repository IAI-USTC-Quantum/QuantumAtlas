package registry

import (
	"errors"
	"testing"
)

func TestExternalSourceIdentity(t *testing.T) {
	for _, tc := range []struct{ url, normalized, identity, pdf string }{
		{"https://EPRINT.IACR.ORG/2026/1591", "https://eprint.iacr.org/2026/1591", "eprint:2026/1591", "https://eprint.iacr.org/2026/1591.pdf"},
		{"https://eprint.iacr.org/2026/1591.pdf", "https://eprint.iacr.org/2026/1591.pdf", "eprint:2026/1591", "https://eprint.iacr.org/2026/1591.pdf"},
		{"https://eprint.iacr.org/2026/1591/pdf", "https://eprint.iacr.org/2026/1591/pdf", "eprint:2026/1591", "https://eprint.iacr.org/2026/1591.pdf"},
		{"https://EXAMPLE.ORG./paper.pdf?v=2", "https://example.org/paper.pdf?v=2", "source_url:https://example.org/paper.pdf?v=2", "https://example.org/paper.pdf?v=2"},
		{"https://example.org", "https://example.org/", "source_url:https://example.org/", "https://example.org/"},
	} {
		t.Run(tc.url, func(t *testing.T) {
			normalized, identity, _, err := ExternalSourceIdentity(tc.url)
			if err != nil || normalized != tc.normalized || identity != tc.identity {
				t.Fatalf("identity = %q, %q, %v", normalized, identity, err)
			}
			pdf, err := ExternalSourcePDFURL(tc.url)
			if err != nil || pdf != tc.pdf {
				t.Fatalf("pdf = %q, %v", pdf, err)
			}
		})
	}
}

func TestExternalSourceURLRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		"", "http://example.org/p.pdf", "https:example.org/p.pdf", "/paper.pdf",
		"https://user:secret@example.org/a.pdf", "https://example.org:443/a.pdf", "https://example.org:/a.pdf",
		"https://example.org/a.pdf#fragment", "https://example.org/a.pdf#", " https://example.org/a.pdf",
		"https://example.org/a.pdf\n", "https://example.org\\@localhost/a.pdf", "https://例子.org/a.pdf",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NormalizeExternalSourceURL(raw); !errors.Is(err, ErrExternalSourceInvalid) {
				t.Errorf("accepted %q: %v", raw, err)
			}
		})
	}
}

func TestExternalSourceUnavailable(t *testing.T) {
	if _, err := NewStore(nil).RegisterExternalSource(t.Context(), ExternalSourceRef{}); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("error=%v", err)
	}
}
