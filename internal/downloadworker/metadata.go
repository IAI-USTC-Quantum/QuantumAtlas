package downloadworker

import (
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"net/url"
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
		trace := workerprotocol.Trace{Strategy: short(a.Strategy, 64), URL: publicURL(a.URL), Millis: a.Millis}
		if a.Error != "" {
			trace.Error = "strategy failed"
		}
		m.Trace = append(m.Trace, trace)
	}
	return m
}
