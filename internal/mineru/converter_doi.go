package mineru

import (
	"context"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

func doiJobKey(doi string) string { return "doi:" + doi }

// EnsureByDOI is a compatibility entry point into frozen-source parsing.
// Legacy markdown/JSON/images are never cache hits or write targets.
func (c *Converter) EnsureByDOI(ctx context.Context, doi, oaPDFURL string) *Job {
	return c.ensureCanonical(ctx, doi, oaPDFURL, true, false)
}

func (c *Converter) LookupDOI(doi string) (*Job, bool) {
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return nil, false
	}
	return c.Lookup(doiJobKey(norm))
}
