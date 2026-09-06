package downloader

import (
	"net/url"
	"strings"
)

// PatternCandidates returns direct-PDF URL candidates constructed from
// the DOI alone, keyed on the registrant prefix. Only patterns that are
// genuinely derivable without per-article ids are listed; publishers
// whose PDF paths embed file ids (OUP, AIP, CUP, RSC, MDPI, Nature,
// APS journal paths, …) are covered by the landing-page strategy
// instead. All candidates still go through the validation pipeline —
// a constructed URL is a guess, not a promise.
func PatternCandidates(doi string) []string {
	prefix := doiPrefix(doi)
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	switch prefix {
	case "10.1007": // Springer Nature Link
		// The DOI must be path-encoded (%2F) on the content/pdf route;
		// the raw form is kept as a fallback since both shapes appear
		// in the wild.
		add("https://link.springer.com/content/pdf/" + url.PathEscape(doi) + ".pdf")
		add("https://link.springer.com/content/pdf/" + doi + ".pdf")
	case "10.1111", "10.1002", "10.1529", "10.1890", "10.1111/j.": // Wiley family
		add("https://onlinelibrary.wiley.com/doi/pdf-direct/" + doi)
		add("https://onlinelibrary.wiley.com/doi/pdf/" + doi + "?download=true")
	case "10.1080": // Taylor & Francis
		add("https://www.tandfonline.com/doi/pdf/" + doi + "?download=true")
	case "10.1177": // SAGE
		add("https://journals.sagepub.com/doi/pdf/" + doi)
	case "10.1021": // ACS
		add("https://pubs.acs.org/doi/pdf/" + doi)
	case "10.1145": // ACM DL
		add("https://dl.acm.org/doi/pdf/" + doi)
	case "10.3389": // Frontiers
		add("https://www.frontiersin.org/articles/" + doi + "/pdf")
	case "10.1371": // PLOS
		if j := plosJournal(doi); j != "" {
			add("https://journals.plos.org/" + j + "/article/file?id=" + url.QueryEscape(doi) + "&type=printable")
		}
	case "10.7554": // eLife
		add("https://elifesciences.org/articles/" + strings.TrimPrefix(doi, "10.7554/") + "/pdf")
	case "10.1101": // bioRxiv / medRxiv preprints
		// Version-less content path redirects to the current version's
		// full-text PDF.
		add("https://www.biorxiv.org/content/" + doi + ".full.pdf")
	}
	return out
}

// doiPrefix returns the registrant prefix of a DOI ("10.1109/x" →
// "10.1109"). Empty when the shape is unexpected.
func doiPrefix(doi string) string {
	i := strings.IndexByte(doi, '/')
	if i <= 0 {
		return ""
	}
	return doi[:i]
}

// plosJournal extracts the journal token from a PLOS DOI suffix
// ("10.1371/journal.pone.0123456" → "journal.pone").
func plosJournal(doi string) string {
	suffix := strings.TrimPrefix(doi, "10.1371/")
	parts := strings.Split(suffix, ".")
	if len(parts) < 2 {
		return ""
	}
	j := parts[0] + "." + parts[1]
	if !strings.HasPrefix(j, "journal.") {
		return ""
	}
	return j
}
