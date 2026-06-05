package routes

// resolution.go: track and surface the "what defaults did we apply
// to your request?" metadata that the paper-access endpoints emit.
//
// When a caller hits `/api/papers/<id_or_doi>/{markdown,pdf}[/status]`
// with anything OTHER than a fully canonical, versioned arxiv id
// (e.g. they pass a DOI, a bare 7-digit old-style id, or a new-style
// id without `vN`), the dispatch layer transforms the input through
// one or more inference steps before calling the handler:
//
//   - DOI → bare arxiv id (via OpenAlex)
//   - bare arxiv id → versioned (via arxiv.Fetcher.ResolveLatestVersion)
//   - bare old-style id → quant-ph/ canonical (via
//     paperassets.DefaultOldStyleCategory)
//
// We tell the caller about every default we applied via two channels
// so both byte-streaming GETs (e.g. 200 with text/markdown body) and
// JSON status responses (202 / *status endpoints) carry the same info:
//
//   - HTTP headers `X-QAtlas-Resolved-Id` / `X-QAtlas-Requested-Id` /
//     `X-QAtlas-Defaults-Applied`. Header-based is always reachable
//     even for bytes-only responses.
//   - JSON body fields `requested_id`, `defaults_applied` (when the
//     response is JSON anyway).
//
// The qatlas client uses the headers to print a one-line "note: server
// applied these defaults" message after each call so the user is
// never surprised by what version / what category they got.

import (
	"context"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// idResolution captures the input → resolved-canonical transformation
// performed by the dispatch layer. Empty fields = no inference fired.
type idResolution struct {
	// RequestedID is the exact path component the caller supplied
	// (without leading/trailing slashes). For DOI input this is the
	// full DOI string; for arxiv input it's whatever the caller typed.
	RequestedID string

	// ResolvedID is the fully canonical, versioned arxiv id the
	// handlers actually use to read/write storage. Equals
	// RequestedID when no transformation was needed.
	ResolvedID string

	// DefaultsApplied is a list of human-readable strings describing
	// each inference step. Empty when the input was already canonical
	// (versioned + categorized).
	DefaultsApplied []string
}

// hasChanges reports whether any inference fired. Status / 202 / 200
// responses only include the metadata when this is true — no need to
// repeat the canonical id back to a caller who already typed it.
func (r *idResolution) hasChanges() bool {
	return r != nil && (r.ResolvedID != r.RequestedID || len(r.DefaultsApplied) > 0)
}

// resolutionKeyType is a private context key type so values stashed
// here can't collide with anything else stuffed into the request
// context by middleware.
type resolutionKeyType struct{}

var resolutionKey = resolutionKeyType{}

// withResolution returns a derived context that carries the given
// resolution; pair with resolutionFromContext to read it back inside
// handlers / response builders.
func withResolution(ctx context.Context, r *idResolution) context.Context {
	return context.WithValue(ctx, resolutionKey, r)
}

// resolutionFromContext returns the resolution attached to ctx, or
// nil when no transformation was tracked (e.g. a non-paper-access
// endpoint). Callers should defensively handle the nil case.
func resolutionFromContext(ctx context.Context) *idResolution {
	if v, ok := ctx.Value(resolutionKey).(*idResolution); ok {
		return v
	}
	return nil
}

// computeResolution walks the original requestedID and the final
// arxivPart that dispatch produced, plus a hint about whether DOI
// resolution fired, and emits a structured description of every
// inference step that happened along the way.
//
// doiResolved is non-empty when the input was a DOI — it carries the
// bare canonical arxiv id OpenAlex returned (post-StripVersion).
// bareIDPostDOI is the same as doiResolved in that case, or
// requestedID itself when no DOI was involved.
func computeResolution(requestedID, bareIDPostDOI, finalID string) *idResolution {
	r := &idResolution{RequestedID: requestedID, ResolvedID: finalID}
	if requestedID == "" || finalID == "" {
		return r
	}

	// 1. DOI resolution
	if bareIDPostDOI != requestedID {
		r.DefaultsApplied = append(r.DefaultsApplied,
			"doi_resolved_via_openalex (DOI → arxiv id "+bareIDPostDOI+")")
	}

	// 2. Latest-version inference. We can detect this by parsing
	//    bareIDPostDOI and comparing to finalID's version suffix.
	bareParsed, bErr := paperassets.Parse(bareIDPostDOI)
	finalParsed, fErr := paperassets.Parse(finalID)
	if bErr == nil && fErr == nil && bareParsed.Version == "" && finalParsed.Version != "" {
		r.DefaultsApplied = append(r.DefaultsApplied,
			"version="+finalParsed.Version+" (no version specified; latest published version used)")
	}

	// 3. Default category inference: bareParsed was old-style + bare
	//    AND the AssetKey path routes it to quant-ph by default. Note
	//    we surface this when the INPUT (bareIDPostDOI) was bare;
	//    DOI-resolved bare ids also get the category default applied
	//    silently downstream.
	if bErr == nil && bareParsed.IsValid() && bareParsed.IsOldStyle && bareParsed.IsBare {
		r.DefaultsApplied = append(r.DefaultsApplied,
			"category="+paperassets.DefaultOldStyleCategory+
				" (no category prefix; server default per docs/reference/arxiv-ids.md §3.1)")
	}

	return r
}

// applyResolutionHeaders stamps the resolution onto the response
// headers. Safe to call before WriteHeader. No-op when r is nil or
// nothing actually changed.
func applyResolutionHeaders(w http.ResponseWriter, r *idResolution) {
	if !r.hasChanges() {
		return
	}
	w.Header().Set("X-QAtlas-Requested-Id", r.RequestedID)
	w.Header().Set("X-QAtlas-Resolved-Id", r.ResolvedID)
	if len(r.DefaultsApplied) > 0 {
		w.Header().Set("X-QAtlas-Defaults-Applied", strings.Join(r.DefaultsApplied, "; "))
	}
}

// embedResolutionInBody mutates body to include `requested_id` and
// `defaults_applied` keys when the resolution carried any change.
// No-op when r is nil or hasChanges() returns false. Body is the
// JSON map produced by snapshotBody / inline-built JSON dicts.
func embedResolutionInBody(body map[string]any, r *idResolution) {
	if !r.hasChanges() {
		return
	}
	body["requested_id"] = r.RequestedID
	if r.ResolvedID != r.RequestedID {
		body["resolved_id"] = r.ResolvedID
	}
	if len(r.DefaultsApplied) > 0 {
		body["defaults_applied"] = r.DefaultsApplied
	}
}
