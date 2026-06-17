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
