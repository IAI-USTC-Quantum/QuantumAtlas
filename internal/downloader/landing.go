package downloader

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// Landing extraction: fetch the DOI's publisher landing page (doi.org
// redirect, cookie jar active, browser headers) and mine it for direct
// PDF links. This is the Zotero-translator move and covers every
// publisher whose PDF URL embeds per-article file ids (OUP, AIP, CUP,
// RSC, IOP, Nature, APS, MDPI …) via the de-facto standard
// citation_pdf_url meta tag, plus the IEEE stamp interstitial.

var (
	citationPDFURLRe  = regexp.MustCompile(`(?is)<meta[^>]+name=["']citation_pdf_url["'][^>]+content=["']([^"']+)["']`)
	citationPDFURLRe2 = regexp.MustCompile(`(?is)<meta[^>]+content=["']([^"']+)["'][^>]+name=["']citation_pdf_url["']`)
	pdfHrefRe         = regexp.MustCompile(`(?is)(?:href|src)=["']([^"'\s<>]+\.pdf(?:\?[^"'\s<>]*)?)["']`)
	ieeeDocRe         = regexp.MustCompile(`(?i)ieeexplore\.ieee\.org/document/(\d+)`)
	ieeeFrameSrcRe    = regexp.MustCompile(`(?is)(?:iframe[^>]+src|window\.open\(["'])\s*(/stamp/stampPDF\.jsp[^"'\s)]+|/iel[0-9x]+/[^"'\s)]+\.pdf)`)
	// jsonPDFURLRe matches inline-JSON pdf links ("pdfUrl": "/…"),
	// how IEEE Xplore embeds the stamp URL in its document pages.
	jsonPDFURLRe = regexp.MustCompile(`(?i)"(?:pdfUrl|pdf_url|pdfLink)"\s*:\s*"([^"]+)"`)
)

// LandingInfo is the outcome of a landing-page fetch: the candidates
// found plus the page itself (bounded) for the agent fallback.
type LandingInfo struct {
	FinalURL   string
	HTML       []byte
	Candidates []string
}

// FetchLanding fetches the landing page for a DOI and extracts PDF
// candidates. IEEE document pages additionally resolve the stamp.jsp
// interstitial into the real PDF endpoint (one extra bounded fetch,
// cookies shared).
func (d *Downloader) FetchLanding(ctx context.Context, doi string) (*LandingInfo, error) {
	base := d.fetch.cfg.LandingBaseURL
	if base == "" {
		base = "https://doi.org/"
	}
	landingURL := strings.TrimRight(base, "/") + "/" + doi
	return d.scrapeLanding(ctx, landingURL)
}

// scrapeLanding is the landing-page miner: bounded GET, meta/href
// extraction, IEEE stamp resolution. Also used on repository landing
// pages surfaced by the OA APIs (green-OA hosts like HAL or
// repositorio.* whose "pdf" candidate is actually an article page).
func (d *Downloader) scrapeLanding(ctx context.Context, landingURL string) (*LandingInfo, error) {
	html, finalURL, _, err := d.fetch.FetchText(ctx, landingURL, d.fetch.cfg.LandingMaxBytes)
	if err != nil {
		return nil, err
	}
	info := &LandingInfo{FinalURL: finalURL, HTML: html}
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		abs := absolutize(raw, finalURL)
		if abs != "" && !seen[abs] {
			seen[abs] = true
			info.Candidates = append(info.Candidates, abs)
		}
	}
	for _, re := range []*regexp.Regexp{citationPDFURLRe, citationPDFURLRe2} {
		if m := re.FindStringSubmatch(string(html)); m != nil {
			add(htmlUnescape(m[1]))
		}
	}
	// Bare .pdf hrefs are a WEAK signal: article pages cross-link
	// related-content PDFs on entirely different hosts (observed: a
	// Nature Medicine page linking a TensorFlow whitepaper). Only (a)
	// when no citation_pdf_url meta exists at all, and (b) restricted
	// to the landing page's own host, do we mine the raw anchors.
	if len(info.Candidates) == 0 {
		for _, m := range pdfHrefRe.FindAllStringSubmatch(string(html), 16) {
			raw := htmlUnescape(m[1])
			if !sameHostOrRelative(raw, finalURL) {
				continue
			}
			add(raw)
		}
	}
	// IEEE: the document page's PDF lives behind stamp.jsp; resolve the
	// frameset now so the candidate list has the real endpoint.
	if m := ieeeDocRe.FindStringSubmatch(finalURL); m != nil {
		add("https://ieeexplore.ieee.org/stamp/stamp.jsp?tp=&arnumber=" + m[1])
		if stamp, ok := findString(info.Candidates, func(s string) bool {
			return strings.Contains(s, "/stamp/stamp.jsp")
		}); ok {
			if real := d.resolveIEEEStamp(ctx, stamp); real != "" {
				// The resolved endpoint outranks the interstitial.
				info.Candidates = append([]string{real}, info.Candidates...)
			}
		}
	}
	return info, nil
}

// resolveIEEEStamp fetches a stamp.jsp interstitial and returns the
// inner PDF endpoint (stampPDF.jsp or a direct /ielX/…pdf path),
// absolutized against the stamp URL. Empty string = nothing found.
func (d *Downloader) resolveIEEEStamp(ctx context.Context, stampURL string) string {
	html, finalURL, _, err := d.fetch.FetchText(ctx, stampURL, d.fetch.cfg.LandingMaxBytes)
	if err != nil {
		return ""
	}
	m := ieeeFrameSrcRe.FindStringSubmatch(string(html))
	if m == nil {
		return ""
	}
	return absolutize(m[1], finalURL)
}

// absolutize resolves raw against base; empty when hopeless.
func absolutize(raw, base string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, httpsPrefix) {
		return raw
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

const httpsPrefix = "https://"

// sameHostOrRelative reports whether a raw link target stays on the
// landing page's host (relative URLs resolve onto it by definition).
func sameHostOrRelative(raw, base string) bool {
	if raw == "" {
		return false
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, httpsPrefix) {
		return true // relative
	}
	bu, err := url.Parse(base)
	if err != nil {
		return false
	}
	ru, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(ru.Host, bu.Host)
}

func findString(list []string, pred func(string) bool) (string, bool) {
	for _, s := range list {
		if pred(s) {
			return s, true
		}
	}
	return "", false
}

// htmlUnescape decodes the handful of entities that appear inside
// citation meta values (&amp; most of all). Full entity handling is
// unnecessary for URL extraction.
func htmlUnescape(s string) string {
	replacements := []struct{ from, to string }{
		{"&amp;", "&"},
		{"&#38;", "&"},
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&quot;", "\""},
		{"&#39;", "'"},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	return s
}
