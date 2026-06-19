package paperassets

import "testing"

func TestNormalizeDOI(t *testing.T) {
	cases := map[string]string{
		"10.1103/PhysRevLett.123.070501":            "10.1103/physrevlett.123.070501",
		"  10.1103/PhysRevLett.123.070501  ":        "10.1103/physrevlett.123.070501",
		"https://doi.org/10.1103/PhysRevLett.123":   "10.1103/physrevlett.123",
		"http://dx.doi.org/10.1103/PhysRevLett.123": "10.1103/physrevlett.123",
		"doi:10.1103/PhysRevLett.123":               "10.1103/physrevlett.123",
		"DOI.ORG/10.1103/PhysRevLett.123":           "10.1103/physrevlett.123",
	}
	for in, want := range cases {
		if got := NormalizeDOI(in); got != want {
			t.Errorf("NormalizeDOI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateDOI(t *testing.T) {
	valid := []string{
		"10.1103/PhysRevLett.123.070501",
		"https://doi.org/10.7717/peerj.4375",
		"10.1234/foo/bar",
		"10.1000/182",
	}
	for _, v := range valid {
		if norm, ok := ValidateDOI(v); !ok || norm == "" {
			t.Errorf("ValidateDOI(%q) = (%q,%v), want valid", v, norm, ok)
		}
	}
	invalid := []string{
		"",
		"   ",
		"2501.00010v1",       // arxiv, not a DOI
		"quant-ph/9508027v1", // old-style arxiv
		"11.1103/x",          // wrong directory indicator
		"10./missing-reg",    // no registrant digits
		"10.1103/",           // empty suffix
		"10.1103",            // no slash
		// Suffix containing literal "__" — DOISafeStem encodes "/"
		// → "__", so a suffix that already carries "__" would not
		// round-trip and would regenerate the PR #19 phantom-node
		// bug (sync's reverse path would synthesize a different
		// node key than the upsert path wrote).
		"10.5555/foo__bar",
		"10.5555/double__under__score",
		"10.5555/foo/bar__baz",
	}
	for _, v := range invalid {
		if norm, ok := ValidateDOI(v); ok {
			t.Errorf("ValidateDOI(%q) = (%q,true), want invalid", v, norm)
		}
	}
}

func TestValidateDOIRejectsControlChars(t *testing.T) {
	if _, ok := ValidateDOI("10.1103/foo\x00bar"); ok {
		t.Fatal("ValidateDOI should reject control chars")
	}
}

// TestValidateDOIRejectsDoubleUnderscoreSuffix locks in the PR #19
// review-3 fix: because DOISafeStem encodes "/" → "__" and DOIDecodeStem
// inverts "__" → "/", any DOI whose suffix already carries a literal
// "__" cannot round-trip through the storage-key ↔ node-key path. If
// such a DOI ever reached the upload pipeline it would regenerate the
// same phantom-node bug commit 5b84111 fixed for nested-slash DOIs —
// UpsertPDFByDOI would write node key "doi:10.x/foo__bar" while sync's
// reverse path would synthesize "doi:10.x/foo/bar", and the MERGE in
// sync.go would create a phantom :PaperWork on every run. Rejecting at
// the validate boundary is safer than silently corrupting the graph.
//
// (DOIs with "__" do occur in the wild — some publishers use "__" as an
// internal separator — but uniqueness of the storage-stem encoding is
// the stronger constraint here. The alternative, swapping in a properly
// bijective encoding like percent-escape, would invalidate every
// already-stored storage key.)
func TestValidateDOIRejectsDoubleUnderscoreSuffix(t *testing.T) {
	cases := []string{
		"10.5555/foo__bar",
		"10.5555/double__under__score",
		"10.5555/foo/bar__baz",                          // nested slash + literal __ together
		"https://doi.org/10.5555/foo__bar",              // URL-prefixed form
		"10.1103/PhysRevLett.123__supplement",           // realistic-looking suffix
	}
	for _, doi := range cases {
		if norm, ok := ValidateDOI(doi); ok {
			t.Errorf("ValidateDOI(%q) = (%q,true), want invalid (suffix contains \"__\" which would break DOISafeStem round-trip)", doi, norm)
		}
	}

	// Sanity: DOIs with "__" ONLY in the registrant are impossible
	// (doiShapeRE forbids it), and DOIs without "__" in the suffix
	// still validate fine.
	stillValid := []string{
		"10.1103/PhysRevLett.123.070501", // no underscores
		"10.1234/foo_bar",                // single underscore is fine
		"10.1234/foo/bar",                // nested slash, no "__"
	}
	for _, doi := range stillValid {
		if _, ok := ValidateDOI(doi); !ok {
			t.Errorf("ValidateDOI(%q) rejected a DOI that should still be valid (only suffix-\"__\" should be rejected)", doi)
		}
	}
}

func TestDOIAssetKey(t *testing.T) {
	cases := []struct {
		kind, doi, want string
	}{
		{"pdf", "10.1103/PhysRevLett.123.070501", "pdf/doi/10.1103/physrevlett.123.070501.pdf"},
		{"markdown", "10.7717/peerj.4375", "markdown/doi/10.7717/peerj.4375.md"},
		{"images", "10.1103/PhysRevLett.123", "images/doi/10.1103/physrevlett.123.zip"},
		{"json", "10.1234/foo/bar", "json/doi/10.1234/foo__bar.json"},
		{"pdf", "https://doi.org/10.1000/182", "pdf/doi/10.1000/182.pdf"},
		{"pdf", "not-a-doi", ""},
		{"bogus", "10.1103/x", ""},
	}
	for _, c := range cases {
		if got := DOIAssetKey(c.kind, c.doi); got != c.want {
			t.Errorf("DOIAssetKey(%q,%q) = %q, want %q", c.kind, c.doi, got, c.want)
		}
	}
}

func TestDOIKeyNamespaceDisjointFromArxiv(t *testing.T) {
	// A DOI and an arxiv id must never collide on the same object key.
	doiKey := DOIAssetKey("pdf", "10.1103/2501.00010")
	arxivKey := AssetKey("pdf", "2501.00010v1")
	if doiKey == "" || arxivKey == "" {
		t.Fatalf("unexpected empty key: doi=%q arxiv=%q", doiKey, arxivKey)
	}
	if doiKey == arxivKey {
		t.Fatalf("DOI key %q collides with arxiv key %q", doiKey, arxivKey)
	}
}

// TestDOIDecodeStem locks in the inverse-of-DOISafeStem contract that
// internal/papers/sync.go relies on for the nested-slash phantom-node
// fix: any "__" in a stored stem must round-trip back to "/" so the
// reverse path (storage key → node key) lands on the original DOI
// node UpsertPDFByDOI / UpsertMDByDOI created.
func TestDOIDecodeStem(t *testing.T) {
	cases := map[string]string{
		"":             "",
		"foo":          "foo",
		"foo__bar":     "foo/bar",
		"a__b__c":      "a/b/c",
		"already/slash": "already/slash", // pass-through; sync feeds us the post-extension-strip stem
	}
	for in, want := range cases {
		if got := DOIDecodeStem(in); got != want {
			t.Errorf("DOIDecodeStem(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDOISafeStemDecodeRoundTrip is the linchpin: for every legitimate
// DOI suffix, encoding for storage and decoding back must reproduce the
// original suffix exactly. Without this guarantee, sync's reverse path
// for nested-slash DOIs produces a node key that never matches the
// :PaperWork node, regenerating the phantom-node bug at a different
// layer (reported during PR #19 review).
func TestDOISafeStemDecodeRoundTrip(t *testing.T) {
	dois := []string{
		"10.1103/physrevlett.123.070501",
		"10.1234/foo/bar",
		"10.1234/foo/bar/baz",
		"10.7717/peerj.4375",
		"10.1000/182",
	}
	for _, doi := range dois {
		safe := DOISafeStem(doi)
		// The original suffix is everything after the first "/".
		idx := -1
		for i := 0; i < len(doi); i++ {
			if doi[i] == '/' {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("test bug: DOI %q has no slash", doi)
		}
		want := doi[idx+1:]
		if got := DOIDecodeStem(safe); got != want {
			t.Errorf("round-trip for DOI %q: DOISafeStem=%q DOIDecodeStem=%q, want %q", doi, safe, got, want)
		}
	}
}

// TestDOIURLPrefixesExported guards the PR #19 follow-up: the DOI URL
// prefix list is the canonical list for the whole codebase and must be
// importable (capitalized) from other packages, not just used
// internally. The other two sites that hard-code a subset —
// internal/openalex/lookup.go (normalizeDOI) and internal/openalex/parse.go
// (shortDOI) — are expected to import this; removing the export would
// force them to fork yet another inline slice.
func TestDOIURLPrefixesExported(t *testing.T) {
	if DOIURLPrefixes == nil {
		t.Fatal("DOIURLPrefixes is nil; expected exported canonical DOI URL prefix list")
	}
	want := []string{
		"https://doi.org/",
		"http://doi.org/",
		"https://dx.doi.org/",
		"http://dx.doi.org/",
		"doi.org/",
		"dx.doi.org/",
		"doi:",
	}
	if len(DOIURLPrefixes) != len(want) {
		t.Errorf("DOIURLPrefixes has %d entries, want %d: %v", len(DOIURLPrefixes), len(want), DOIURLPrefixes)
	}
	for i, p := range want {
		if i >= len(DOIURLPrefixes) || DOIURLPrefixes[i] != p {
			t.Errorf("DOIURLPrefixes[%d] = %q, want %q (canonical list must be stable for import sites)",
				i, safeAt(DOIURLPrefixes, i), p)
		}
	}
	// Sanity: NormalizeDOI must consume the same exported list
	// (i.e. the internal use was updated alongside the export).
	norm := NormalizeDOI("https://doi.org/10.1103/PhysRevLett.123")
	if norm != "10.1103/physrevlett.123" {
		t.Errorf("NormalizeDOI after DOIURLPrefixes export: got %q, want canonical form", norm)
	}
}

func safeAt(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<out-of-range>"
}
