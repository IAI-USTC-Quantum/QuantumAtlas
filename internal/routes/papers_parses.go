package routes

// papers_parses.go: Q1 block-comments parse-revision surface (plan §12.2):
//
//	GET /api/papers/{paper_id}/parses                 — parse revision list
//	GET /api/papers/{paper_id}/parses/{revision}/json — original JSON bytes (hash-verifiable)
//
// Revisions are immutable: re-parsing a paper INSERTS a new revision
// and flips is_current; the old artifact (and every comment anchored to
// it) keeps serving unchanged bytes forever (plan §4.2).

import (
	"errors"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// paperParsesListHandler answers GET /api/papers/{paper_id}/parses.
func paperParsesListHandler(re *core.RequestEvent, catalog blockCatalog, requestedID string) error {
	ctx := re.Request.Context()
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		return blockResolveError(re, status, detail)
	}
	applyResolutionHeaders(re.Response, rp.resolution)

	revs, err := catalog.ListParseRevisions(ctx, rp.canonical)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	items := make([]map[string]any, 0, len(revs))
	current := ""
	for _, rev := range revs {
		if rev.IsCurrent {
			current = rev.RevisionID
		}
		items = append(items, map[string]any{
			"revision_id":     rev.RevisionID,
			"source_id":       rev.SourceID,
			"schema":          rev.Schema,
			"schema_version":  rev.SchemaVersion,
			"artifact_sha256": rev.ArtifactSha256,
			"is_current":      rev.IsCurrent,
			"created_at":      rev.CreatedAt.UTC().Format(time.RFC3339),
			"json_endpoint":   "/api/papers/" + rp.canonical + "/parses/" + rev.RevisionID + "/json",
			"blocks_endpoint": "/api/papers/" + rp.canonical + "/parses/" + rev.RevisionID + "/blocks",
		})
	}
	body := map[string]any{
		"paper_id":            rp.canonical,
		"parses":              items,
		"current_revision_id": nil,
	}
	if current != "" {
		body["current_revision_id"] = current
	}
	embedResolutionInBody(body, rp.resolution)
	return re.JSON(http.StatusOK, body)
}

// paperParseJSONHandler answers GET /api/papers/{paper_id}/parses/{revision}/json
// with the ORIGINAL parse artifact bytes — not a re-serialization.
// The response ETag / X-QAtlas-Sha256 carry the artifact sha256 the
// revision row pins, so callers can verify byte identity end to end.
func paperParseJSONHandler(re *core.RequestEvent, store objstore.Store, catalog blockCatalog, requestedID, revisionID string) error {
	ctx := re.Request.Context()
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		return blockResolveError(re, status, detail)
	}
	applyResolutionHeaders(re.Response, rp.resolution)

	rev, found, err := catalog.GetParseRevision(ctx, rp.canonical, revisionID)
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
			"detail": "no such parse revision " + revisionID + " under paper " + rp.canonical,
		})
	}
	if store == nil {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "object store not configured on this server",
		})
	}
	data, err := blockReadVerified(ctx, store, rev.ObjstoreKey, rev.ArtifactSha256)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "parse artifact object missing from the store (row exists, bytes absent): " + rev.ObjstoreKey,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch parse artifact: " + err.Error(),
		})
	}
	re.Response.Header().Set("ETag", `"`+rev.ArtifactSha256+`"`)
	re.Response.Header().Set("X-QAtlas-Sha256", rev.ArtifactSha256)
	re.Response.Header().Set("Cache-Control", "private, max-age=86400")
	re.Response.Header().Set("Content-Type", "application/json")
	// No Range semantics needed for the JSON artifact; bytes are small
	// and hash-verified as a whole.
	return re.Blob(http.StatusOK, "application/json", data)
}
