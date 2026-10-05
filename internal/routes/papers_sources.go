package routes

// papers_sources.go: Q1 block-comments originals surface (plan §12.2):
//
//	GET /api/papers/{paper_id}/sources                    — source PDF list
//	GET /api/papers/{paper_id}/sources/{source_id}/pdf    — source PDF bytes (Range OK)
//
// plus the shared plumbing this file owns for the whole block-originals
// family (parses / blocks live in papers_parses.go / papers_blocks.go):
// the canonical resolver (qa_ + alias → canonical qa_, merged_into
// chains followed, request alias surfaced via X-QAtlas-* headers) and
// the GET dispatcher hook for every "/sources" / "/parses" path.
//
// Auth: every endpoint rides the existing
// scopeGuard(papers, read) catch-all in RegisterPapers, so anonymous
// callers get 401, PATs need papers:read, and Range requests are
// authenticated identically (plan §12.2 "Range 同样鉴权"). Unlike the
// legacy /markdown surface these endpoints are NOT gated behind
// QATLAS_PAPER_ACCESS_ENABLED: §12.2 locks them as the normal
// logged-in reading surface for block comments. The legacy
// GET /api/papers/{id}/pdf route stays 410 — this is a different,
// source-pinned contract.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// blockCatalog is the slice of *registry.Store the block-originals
// endpoints need, factored out so tests can fake the catalog without a
// database (same pattern as paperCatalog). *registry.Store satisfies
// it implicitly.
type blockCatalog interface {
	Get(ctx context.Context, paperID string) (*registry.Paper, bool, error)
	GetPaperIDByIdentity(ctx context.Context, scheme, id string) (string, bool, error)
	ListPaperSources(ctx context.Context, paperID string) ([]registry.PaperSource, error)
	GetPaperSource(ctx context.Context, paperID, sourceID string) (registry.PaperSource, bool, error)
	ListParseRevisions(ctx context.Context, paperID string) ([]registry.ParseRevision, error)
	GetParseRevision(ctx context.Context, paperID, revisionID string) (registry.ParseRevision, bool, error)
}

var _ blockCatalog = (*registry.Store)(nil)

// blockOriginalsMarkerDepth guards against pathological paths: the
// dispatcher scans for the first "sources"/"parses" path segment, which
// cannot appear inside any real paper id (qa_ ULIDs, arXiv ids, DOIs).
const (
	segSources = "sources"
	segParses  = "parses"
)

// dispatchBlockOriginalsGET routes every GET path under
// /api/papers/{id}/(sources|parses)/... to the Q1 handlers. Returns
// handled=false when the path carries no sources/parses marker so the
// legacy dispatcher keeps its behaviour unchanged.
//
// Path grammar (paper id may itself contain slashes — old-style arXiv
// ids like quant-ph/9508027 — because the split anchors on the FIRST
// marker segment):
//
//	{id}/sources
//	{id}/sources/{source_id}/pdf[?version=vN]
//	{id}/parses
//	{id}/parses/{revision}/json
//	{id}/parses/{revision}/blocks[?page_idx=&cursor=&per_page=]
//	{id}/parses/{revision}/blocks/{page_idx}/{block_index}
//	{id}/parses/{revision}/blocks/{page_idx}/{block_index}/image
func dispatchBlockOriginalsGET(
	re *core.RequestEvent,
	cfg *config.Config,
	store objstore.Store,
	catalog blockCatalog,
	raw string,
) (handled bool, err error) {
	segs := strings.Split(strings.Trim(raw, "/"), "/")
	marker, kind := -1, ""
	for i, s := range segs {
		if s == segSources || s == segParses {
			marker, kind = i, s
			break
		}
	}
	if marker < 0 {
		return false, nil
	}
	id := strings.Join(segs[:marker], "/")
	rest := segs[marker+1:]
	if id == "" {
		return true, re.JSON(http.StatusNotFound, map[string]string{
			"detail": "block-originals path is missing the paper id",
		})
	}
	badShape := func() error {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no such block-originals action under /api/papers/%s/%s/%s", id, kind, strings.Join(rest, "/")),
		})
	}
	switch kind {
	case segSources:
		switch {
		case len(rest) == 0:
			return true, paperSourcesListHandler(re, catalog, id)
		case len(rest) == 2 && rest[1] == "pdf":
			return true, paperSourcePDFHandler(re, store, catalog, id, rest[0])
		}
		return true, badShape()
	case segParses:
		switch {
		case len(rest) == 0:
			return true, paperParsesListHandler(re, catalog, id)
		case len(rest) == 2 && rest[1] == "json":
			return true, paperParseJSONHandler(re, store, catalog, id, rest[0])
		case len(rest) == 2 && rest[1] == "blocks":
			return true, paperBlocksListHandler(re, store, catalog, id, rest[0])
		case len(rest) == 4 && rest[1] == "blocks":
			// {rev}/blocks/{page_idx}/{block_index}
			return true, paperBlockGetHandler(re, store, catalog, id, rest[0], rest[2]+"/"+rest[3])
		case len(rest) == 5 && rest[1] == "blocks" && rest[4] == "image":
			// {rev}/blocks/{page_idx}/{block_index}/image
			return true, paperBlockImageHandler(re, cfg, store, catalog, id, rest[0], rest[2]+"/"+rest[3])
		}
		return true, badShape()
	}
	return false, nil
}

// resolvedPaper is the canonical resolver's result: the surviving qa_
// id plus the alias trail for response headers.
type resolvedPaper struct {
	canonical  string
	requested  string
	resolution *idResolution
}

// resolveCanonicalPaper is the Q1 canonical resolver entry (plan §4.1):
// qa_ surrogate, bare/versioned arXiv id, or DOI all resolve to the
// canonical surviving qa_ paper. merged_into chains are followed (the
// oldest qa_ survives a merge; the requested alias is preserved and
// surfaced, never silently rewritten). Title-shaped input is NOT
// resolved — no fuzzy identity minting on this surface.
func resolveCanonicalPaper(ctx context.Context, catalog blockCatalog, requested string) (resolvedPaper, int, string) {
	requested = strings.Trim(requested, "/")
	if requested == "" {
		return resolvedPaper{}, http.StatusBadRequest, "empty paper id"
	}
	resolveQA := func(qa string) (string, int, string) {
		// Follow merged_into chains (bounded; a cycle is data corruption,
		// not something to loop on).
		cur := qa
		for hop := 0; hop < 8; hop++ {
			paper, found, err := catalog.Get(ctx, cur)
			if err != nil {
				return "", http.StatusServiceUnavailable, "catalog unavailable; retry shortly"
			}
			if !found {
				return "", http.StatusNotFound, fmt.Sprintf("no such paper: %s", qa)
			}
			if target, ok := strings.CutPrefix(paper.Status, "merged_into:"); ok && target != "" && target != cur {
				cur = target
				continue
			}
			return cur, 0, ""
		}
		return "", http.StatusInternalServerError, "merged_into chain too deep (data corruption?)"
	}

	switch {
	case strings.HasPrefix(requested, "qa_"):
		canonical, status, detail := resolveQA(requested)
		if status != 0 {
			return resolvedPaper{}, status, detail
		}
		return paperResolution(requested, canonical), 0, ""
	case isDOICandidate(requested):
		pid, found, err := catalog.GetPaperIDByIdentity(ctx, "doi", requested)
		if err != nil {
			return resolvedPaper{}, http.StatusServiceUnavailable, "catalog unavailable; retry shortly"
		}
		if !found {
			return resolvedPaper{}, http.StatusNotFound, fmt.Sprintf("no such paper: %s", requested)
		}
		canonical, status, detail := resolveQA(pid)
		if status != 0 {
			return resolvedPaper{}, status, detail
		}
		return paperResolution(requested, canonical), 0, ""
	default:
		if parsed, perr := paperassets.Parse(requested); perr == nil && parsed.IsValid() {
			pid, found, err := catalog.GetPaperIDByIdentity(ctx, "arxiv", requested)
			if err != nil {
				return resolvedPaper{}, http.StatusServiceUnavailable, "catalog unavailable; retry shortly"
			}
			if !found {
				return resolvedPaper{}, http.StatusNotFound, fmt.Sprintf("no such paper: %s", requested)
			}
			canonical, status, detail := resolveQA(pid)
			if status != 0 {
				return resolvedPaper{}, status, detail
			}
			return paperResolution(requested, canonical), 0, ""
		}
		return resolvedPaper{}, http.StatusBadRequest, fmt.Sprintf(
			"unrecognized paper id %q (want qa_..., an arXiv id, or a DOI)", requested)
	}
}

// paperResolution builds the alias-trail resolution (requested →
// canonical) reusing the X-QAtlas-Requested-Id / X-QAtlas-Resolved-Id
// header contract.
func paperResolution(requested, canonical string) resolvedPaper {
	r := resolvedPaper{canonical: canonical, requested: requested, resolution: &idResolution{
		RequestedID: requested,
		ResolvedID:  canonical,
	}}
	if requested != canonical {
		r.resolution.DefaultsApplied = append(r.resolution.DefaultsApplied,
			"paper_id_resolved ("+requested+" -> "+canonical+")")
	}
	return r
}

// blockResolveError maps the resolver's (status, detail) pair onto the
// shared error envelope.
func blockResolveError(re *core.RequestEvent, status int, detail string) error {
	return re.JSON(status, map[string]string{"detail": detail})
}

// paperSourcesListHandler answers GET /api/papers/{paper_id}/sources.
//
// The "current pointer" (plan §12.2) is derived from the paper's
// current parse revision — no separate mutable column exists on
// paper_sources, which stay append-only byte identities.
func paperSourcesListHandler(re *core.RequestEvent, catalog blockCatalog, requestedID string) error {
	ctx := re.Request.Context()
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		return blockResolveError(re, status, detail)
	}
	applyResolutionHeaders(re.Response, rp.resolution)

	sources, err := catalog.ListPaperSources(ctx, rp.canonical)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	// Derive the current source from the current parse revision.
	parses, err := catalog.ListParseRevisions(ctx, rp.canonical)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	currentSource := ""
	for _, p := range parses {
		if p.IsCurrent {
			currentSource = p.SourceID
			break
		}
	}

	items := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		items = append(items, map[string]any{
			"source_id":    src.SourceID,
			"origin":       src.Origin,
			"sha256":       src.Sha256,
			"size_bytes":   src.SizeBytes,
			"is_current":   src.SourceID == currentSource,
			"created_at":   src.CreatedAt.UTC().Format(time.RFC3339),
			"pdf_endpoint": "/api/papers/" + rp.canonical + "/sources/" + src.SourceID + "/pdf",
		})
	}
	body := map[string]any{
		"paper_id":          rp.canonical,
		"sources":           items,
		"current_source_id": nil,
	}
	if currentSource != "" {
		body["current_source_id"] = currentSource
	}
	embedResolutionInBody(body, rp.resolution)
	return re.JSON(http.StatusOK, body)
}

// paperSourcePDFHandler answers GET /api/papers/{paper_id}/sources/{source_id}/pdf
// with the ORIGINAL source PDF bytes. Range requests are honoured
// (http.ServeContent) under the same auth as full GETs.
//
// ?version=vN is a pin, not a hint: when supplied it must match the
// source's own arXiv origin version exactly; on mismatch the handler
// answers 404 and NEVER substitutes another source (plan §8 Q1:
// "指定vN失败不得换最新版/期刊版").
func paperSourcePDFHandler(re *core.RequestEvent, store objstore.Store, catalog blockCatalog, requestedID, sourceID string) error {
	ctx := re.Request.Context()
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		return blockResolveError(re, status, detail)
	}
	applyResolutionHeaders(re.Response, rp.resolution)

	src, found, err := catalog.GetPaperSource(ctx, rp.canonical, sourceID)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no such source %q under paper %s", sourceID, rp.canonical),
		})
	}
	if v := re.Request.URL.Query().Get("version"); v != "" {
		want := strings.TrimPrefix(v, "v")
		if n, cerr := strconv.Atoi(want); cerr != nil || fmt.Sprintf("arxiv:v%d", n) != src.Origin {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf(
					"requested source version %s does not match source %s (origin %s); refusing to substitute another version",
					v, src.SourceID, src.Origin),
				"source_id": src.SourceID,
				"origin":    src.Origin,
			})
		}
	}
	if store == nil {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "object store not configured on this server",
		})
	}
	// Small PDFs buffer whole + sha-verify server-side; larger ones
	// stream via ranged reads (see papers_source_stream.go).
	if err := serveSourcePDF(re, store, src, rp.canonical+"-"+src.SourceID+".pdf"); err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			// Row exists, bytes gone: honest missing, never a
			// "ready" pointer at a nonexistent object (plan §8 Q1).
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "source PDF object missing from the store (row exists, bytes absent): " + src.ObjstoreKey,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch source pdf: " + err.Error(),
		})
	}
	return nil
}

// blockReadVerified reads the whole object at key and verifies its
// sha256 against want (hex, empty → skip verification). Used by
// endpoints that must inspect bytes (parse JSON validation, image
// render) — buffering + hashing keeps "hash 可核" server-side honest.
// TODO(Q1): stream large artifacts instead of buffering when a
// sha-verifying streaming reader lands.
func blockReadVerified(ctx context.Context, store objstore.Store, key, want string) ([]byte, error) {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if want != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return nil, fmt.Errorf("object %s sha256 mismatch (row pins %s)", key, want)
		}
	}
	return data, nil
}
