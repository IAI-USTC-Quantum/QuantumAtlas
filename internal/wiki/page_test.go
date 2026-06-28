package wiki

import "testing"

func TestParsePageAcceptsTheoremVerificationFrontmatter(t *testing.T) {
	page, err := ParseMarkdown(`---
id: thm-test
title: Test theorem
type: concept
category: theorem
status: published
verification:
  status: verified
  latest_verification_id: vrf-1
  last_updated: 2026-06-28T10:00:00Z
---

## Statement

Example.
`)
	if err != nil {
		t.Fatalf("ParsePage returned error: %v", err)
	}
	if page.Frontmatter.Category != "theorem" {
		t.Fatalf("category = %q, want theorem", page.Frontmatter.Category)
	}
	if page.Frontmatter.Verification == nil {
		t.Fatal("verification frontmatter was not parsed")
	}
	if got := page.Frontmatter.Verification.Status; got != "verified" {
		t.Fatalf("verification.status = %q, want verified", got)
	}
	if got := page.Frontmatter.Verification.LatestVerificationID; got != "vrf-1" {
		t.Fatalf("verification.latest_verification_id = %q, want vrf-1", got)
	}
	if page.Frontmatter.Verification.LastUpdated == nil {
		t.Fatal("verification.last_updated was not parsed")
	}
}

func TestParsePageAcceptsPyYAMLSpaceOffsetTimestamp(t *testing.T) {
	page, err := ParseMarkdown(`---
id: thm-test
title: Test theorem
type: concept
category: theorem
verification:
  status: verified
  last_updated: 2026-06-28 10:00:00+00:00
---

## Statement

Example.
`)
	if err != nil {
		t.Fatalf("ParseMarkdown returned error: %v", err)
	}
	if page.Frontmatter.Verification == nil || page.Frontmatter.Verification.LastUpdated == nil {
		t.Fatal("verification.last_updated was not parsed")
	}
}
