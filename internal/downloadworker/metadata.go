package downloadworker

import (
	"net/url"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func publicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	if len(u.String()) > 512 {
		return ""
	}
	return u.String()
}
func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func resultMetadata(out *downloader.FetchOutcome) workerprotocol.ResultMetadata {
	m := workerprotocol.ResultMetadata{DOI: short(out.DOI, 512), ArxivCanonical: short(out.ArxivCanonical, 128), ArxivVersion: out.ArxivVersion, SourceURL: publicURL(out.URL), Strategy: short(out.Strategy, 64)}
	for i, a := range out.Trace {
		if i >= 12 {
			break
		}
		trace := workerprotocol.Trace{Strategy: short(a.Strategy, 64), URL: publicURL(a.URL), Millis: a.Millis, Error: downloader.SafeAttemptDiagnostic(a)}
		m.Trace = append(m.Trace, trace)
	}
	return m
}

// Failure traces deliberately omit URLs and arbitrary error text. Keeping the
// last 12 steps preserves the browser/final strategy when OA yields many URLs.
// This uses the existing v2 Trace fields, so older masters can receive it too.
func failureTrace(out *downloader.FetchOutcome) []workerprotocol.Trace {
	if out == nil {
		return nil
	}
	attempts := out.Trace
	if len(attempts) > 12 {
		attempts = attempts[len(attempts)-12:]
	}
	trace := make([]workerprotocol.Trace, 0, len(attempts))
	for _, a := range attempts {
		trace = append(trace, workerprotocol.Trace{
			Strategy: diagnosticStrategy(a.Strategy),
			Millis:   max(0, a.Millis),
			Error:    downloader.SafeAttemptDiagnostic(a),
		})
	}
	return trace
}

func diagnosticStrategy(s string) string {
	switch s {
	case "arxiv", "pmc-resolve", "twin-resolve", "pattern", "landing", "browser",
		"oa:openalex", "oa:unpaywall", "oa:europepmc", "oa:semantic_scholar",
		"oa:openalex+landing", "oa:unpaywall+landing", "oa:europepmc+landing", "oa:semantic_scholar+landing":
		return s
	default:
		return "other"
	}
}
