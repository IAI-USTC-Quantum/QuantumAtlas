package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterPapers wires the /api/papers/* endpoints.
//
// rawStore is the abstracted asset backend (LocalStore for cfg.RawDir
// or S3Store for QATLAS_S3_* on RustFS). Every PDF / markdown / JSON /
// image touched by these handlers flows through this interface — never
// directly via os.*, so the same routes work against either backend.
//
// shareStore is the share-token record store (Neo4j-backed in
// production, local-file fallback in dev) used to mint asset share URLs.
//
// catalog is the Neo4j-backed papers catalog (papers.Store) that owns
// all collection-style metadata: aggregate stats, the needs-mineru
// queue, MinerU claim leases, and upload write-through. It degrades
// gracefully (ErrCatalogUnavailable) when Neo4j is unreachable — read
// endpoints report {available:false}; uploads still write the object
// and defer the catalog sync (X-Catalog-Sync: deferred).
//
// enforcer is the process-wide casbin enforcer used to gate write
// endpoints by PAT scope. Session-token callers bypass via the
// ScopeMaster short-circuit in pat.Allows.
//
// Routing: we install three catch-all routes (GET / POST / DELETE) and
// dispatch on the trailing path segment(s) inside the handler. This is
// because arxiv_id can contain slashes for old-style ids and
// net/http's mux can't express "{prefix...}/{action}" cleanly.
// Special case: GET /api/papers/needs-mineru is path-only with no
// arxiv_id, dispatched first.
func RegisterPapers(
	se *core.ServeEvent,
	cfg *config.Config,
	rawStore objstore.Store,
	shareStore *shares.Store,
	catalog *papers.Store,
	converter *mineru.Converter,
	enforcer *casbin.Enforcer,
) {
	se.Router.GET("/api/papers/{path...}", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		if raw == "needs-mineru" {
			return needsMineruHandler(re, catalog)
		}
		if raw == "stats" {
			return paperStatsHandler(re, catalog)
		}
		// Two-segment action "<arxiv>/markdown/status" is the side-effect-free
		// operation resource; check it before the single-segment dispatch
		// (otherwise splitPapersPath would peel "status" off as the action).
		if statusArxiv, ok := splitMarkdownStatus(raw); ok {
			if statusArxiv == "" {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing arxiv_id"})
			}
			return markdownStatusHandler(re, rawStore, converter, statusArxiv)
		}
		arxiv, action := splitPapersPath(raw)
		switch action {
		case "resources":
			if arxiv == "" {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing arxiv_id"})
			}
			return paperResourcesHandler(re, cfg, rawStore, shareStore, arxiv)
		case "markdown":
			if arxiv == "" {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing arxiv_id"})
			}
			return markdownHandler(re, rawStore, converter, arxiv)
		}
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no GET handler for /api/papers/%s", raw),
		})
	})

	se.Router.POST("/api/papers/{path...}", scopeGuard(enforcer, "papers", "write", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		arxiv, action := splitPapersPath(raw)
		switch action {
		case "upload-pdf":
			return uploadPDFHandler(re, cfg, rawStore, catalog, arxiv)
		case "upload-markdown":
			return uploadMarkdownHandler(re, cfg, rawStore, catalog, arxiv)
		case "mineru-claim":
			ttl, _ := strconv.Atoi(re.Request.URL.Query().Get("ttl_seconds"))
			if ttl <= 0 {
				ttl = papers.DefaultTTLSeconds
			}
			return mineruClaimHandler(re, cfg, rawStore, catalog, arxiv, ttl)
		}
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no POST handler for /api/papers/%s", raw),
		})
	}))

	se.Router.DELETE("/api/papers/{path...}", scopeGuard(enforcer, "papers", "write", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		arxiv, claimID, ok := splitMineruClaimRelease(raw)
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("no DELETE handler for /api/papers/%s", raw),
			})
		}
		return mineruClaimReleaseHandler(re, catalog, arxiv, claimID)
	}))
}

// splitPapersPath splits "<arxiv_id>/<action>" into the parts. arxiv_id
// may contain slashes (old-style ids), so we anchor on the last segment
// which must be one of the known action names.
func splitPapersPath(raw string) (arxivID, action string) {
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return "", ""
	}
	idx := strings.LastIndex(raw, "/")
	if idx < 0 {
		return "", raw
	}
	return raw[:idx], raw[idx+1:]
}

// splitMineruClaimRelease parses "<arxiv_id>/mineru-claim/<claim_id>"
// and returns arxiv_id, claim_id, ok.
func splitMineruClaimRelease(raw string) (arxivID, claimID string, ok bool) {
	raw = strings.Trim(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) < 3 || parts[len(parts)-2] != "mineru-claim" {
		return "", "", false
	}
	claimID = parts[len(parts)-1]
	arxivID = strings.Join(parts[:len(parts)-2], "/")
	return arxivID, claimID, true
}

// splitMarkdownStatus parses "<arxiv_id>/markdown/status" and returns the
// arxiv_id + ok. arxiv_id may contain slashes (old-style ids), so we anchor
// on the trailing two fixed segments.
func splitMarkdownStatus(raw string) (arxivID string, ok bool) {
	raw = strings.Trim(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) < 3 || parts[len(parts)-1] != "status" || parts[len(parts)-2] != "markdown" {
		return "", false
	}
	return strings.Join(parts[:len(parts)-2], "/"), true
}

// ---------------------------------------------------------------------------
// stats
// ---------------------------------------------------------------------------

// paperStatsHandler answers GET /api/papers/stats — a read-open endpoint
// exposing the catalog aggregate counters so the SPA can show
// "downloaded papers" (has_pdf) and "converted markdown" (has_md) tiles
// on the home/wiki pages.
//
// When the catalog is unreachable (Neo4j down, or NEO4J_URI unset in
// local dev) we degrade to {available:false} rather than 500 — the
// frontend simply hides the tiles.
func paperStatsHandler(re *core.RequestEvent, catalog *papers.Store) error {
	ctx := re.Request.Context()
	stats, err := catalog.QueryStats(ctx)
	if err != nil {
		if !errors.Is(err, papers.ErrCatalogUnavailable) {
			slog.Warn("papers: QueryStats failed for /api/papers/stats", "error", err)
		}
		return re.JSON(http.StatusOK, map[string]any{"available": false})
	}
	out := map[string]any{
		"available":    true,
		"total":        stats.Total,
		"has_pdf":      stats.HasPDF,
		"has_md":       stats.HasMD,
		"has_json":     stats.HasJSON,
		"needs_mineru": stats.NeedsMineru,
		"total_images": stats.TotalImages,
	}
	if !stats.LoadedAt.IsZero() {
		out["loaded_at"] = stats.LoadedAt.UTC().Format(time.RFC3339)
	}
	return re.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// needs-mineru
// ---------------------------------------------------------------------------

// needsMineruHandler answers GET /api/papers/needs-mineru.
//
// The catalog query already filters out papers with an active claim
// (claims are inlined on the :PaperWork node), so the response is the
// list of papers with a PDF, no markdown, and no live lease — ready to
// be claimed and converted. When the catalog is unreachable we return
// an empty list with available:false rather than 500.
func needsMineruHandler(re *core.RequestEvent, catalog *papers.Store) error {
	limit, _ := strconv.Atoi(re.Request.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}
	ctx := re.Request.Context()
	rows, err := catalog.NeedsMineru(ctx, limit)
	if err != nil {
		if errors.Is(err, papers.ErrCatalogUnavailable) {
			return re.JSON(http.StatusOK, map[string]any{
				"papers": []any{}, "returned": 0, "available": false,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"arxiv_id":         r.ArxivID,
			"key":              r.ArxivID,
			"pdf_path":         r.PDFKey,
			"claimed":          false,
			"claim_expires_at": nil,
			"claim_requester":  nil,
		})
	}
	return re.JSON(http.StatusOK, map[string]any{
		"papers":    out,
		"returned":  len(out),
		"available": true,
	})
}

// ---------------------------------------------------------------------------
// Paper resources
// ---------------------------------------------------------------------------

func paperResourcesHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, shareStore *shares.Store, arxivID string) error {
	ctx := re.Request.Context()
	resolved := paperassets.ResolveAssetsViaStore(ctx, store, arxivID)

	var sharePaths []string
	if resolved.PDFPath != "" {
		sharePaths = append(sharePaths, paperassets.ShareRelPathForKey(resolved.PDFPath))
	}
	if resolved.MarkdownPath != "" {
		sharePaths = append(sharePaths, paperassets.ShareRelPathForKey(resolved.MarkdownPath))
	}
	if resolved.JSONPath != "" {
		sharePaths = append(sharePaths, paperassets.ShareRelPathForKey(resolved.JSONPath))
	}
	if resolved.ImagesDir != "" {
		sharePaths = append(sharePaths, paperassets.ShareRelPathForKey(resolved.ImagesDir))
	}

	shareToken := cfg.ShareAccessToken
	shareBaseURL := ""
	if shareToken != "" {
		shareBaseURL = cfg.PublicBaseURL
	} else if len(sharePaths) > 0 && shareStore != nil {
		rec, err := shares.CreateRecord(shareStore, cfg, shares.CreateOptions{
			Paths: sharePaths,
			Label: "paper assets: " + resolved.ArxivID,
		}, store)
		if err == nil && rec != nil {
			shareToken = rec.Token
		}
	}

	asset := func(kind, key string) map[string]any {
		out := map[string]any{"exists": key != ""}
		if key != "" && shareToken != "" {
			rel := paperassets.ShareRelPathForKey(key)
			out["url"] = shares.BuildURL(shareToken, rel, shareBaseURL)
			if info, exists, err := store.Stat(ctx, key); err == nil && exists {
				out["size"] = info.Size
			}
		}
		return out
	}

	var imageAssets []map[string]any
	if resolved.ImagesDir != "" && shareToken != "" {
		// One ListPrefix per resource lookup; bounded by typical
		// MinerU image counts (~tens). Cap at 500 to avoid runaway
		// response sizes for pathological inputs.
		listed, _ := store.ListPrefix(ctx, resolved.ImagesDir+"/", 500)
		sort.SliceStable(listed, func(i, j int) bool { return listed[i].Key < listed[j].Key })
		for _, info := range listed {
			rel := paperassets.ShareRelPathForKey(info.Key)
			imageAssets = append(imageAssets, map[string]any{
				"name": path.Base(info.Key),
				"url":  shares.BuildURL(shareToken, rel, shareBaseURL),
				"size": info.Size,
			})
		}
	}

	return re.JSON(http.StatusOK, map[string]any{
		"arxiv_id": resolved.ArxivID,
		"assets": map[string]any{
			"pdf":      asset("pdf", resolved.PDFPath),
			"markdown": asset("markdown", resolved.MarkdownPath),
			"json":     asset("json", resolved.JSONPath),
		},
		"images": imageAssets,
	})
}

// ---------------------------------------------------------------------------
// Markdown (silent server-side conversion) handler
// ---------------------------------------------------------------------------

// markdownHandler answers GET /api/papers/{arxiv_id}/markdown.
//
// Silent server-side conversion semantics (the "have → give, none → wait"
// contract): the client asks for a paper's markdown and either gets it
// immediately from cache, or the server transparently kicks off a MinerU
// conversion (using its own MINERU_API_TOKEN) and tells the client to come
// back. The work runs in the background so this request never blocks for
// the minutes MinerU can take.
//
//   - cached markdown present → 200 text/markdown with the content.
//   - absent + conversion enabled → start a background job, return 202
//     with `Operation-Location` pointing at the status resource and a
//     `Retry-After` hint; clients poll the status resource until done.
//   - absent + conversion disabled (no token) → 503 (cache-only mode).
//   - no PDF to convert from → 404.
//   - a recent conversion failed → 502 with the error (auto-retried after
//     a cooldown on a later request).
//
// This is an open read endpoint (no authGuard): browsing users and the
// SPA hit it directly. The conversion it may trigger spends the server's
// MinerU quota, but it's gated on the PDF already existing in the store
// and deduped per paper, so it can't be used to convert arbitrary URLs or
// double-spend on the same paper.
//
// The GET-with-side-effect (it may start a job) is a deliberate tradeoff
// for the "just give me the markdown" UX: callers that only want to
// observe state without triggering work use the side-effect-free
// GET /api/papers/{arxiv_id}/markdown/status resource instead.
func markdownHandler(re *core.RequestEvent, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id: %q (version suffix vN required)", arxivID),
		})
	}

	// Cache hit: stream the stored markdown verbatim.
	if mdKey := resolveMarkdownKey(ctx, store, canonical); mdKey != "" {
		return streamMarkdown(re, store, mdKey)
	}

	if converter == nil || !converter.Enabled() {
		return re.JSON(http.StatusServiceUnavailable, map[string]any{
			"arxiv_id": canonical,
			"status":   "unavailable",
			"detail":   "server-side MinerU conversion is not configured (MINERU_API_TOKEN unset); markdown is only served from cache",
		})
	}

	// Need a PDF to convert from.
	if err := requirePDF(ctx, store, canonical); err != nil {
		return re.JSON(http.StatusNotFound, map[string]any{
			"arxiv_id": canonical,
			"status":   "no_pdf",
			"detail":   err.Error(),
		})
	}

	job := converter.Ensure(canonical)

	// The job may have finished between our cache check and Ensure;
	// re-resolve so we don't make the client poll once more for nothing.
	if job.State == mineru.StateDone {
		if mdKey := resolveMarkdownKey(ctx, store, canonical); mdKey != "" {
			return streamMarkdown(re, store, mdKey)
		}
	}
	if job.State == mineru.StateFailed {
		return re.JSON(http.StatusBadGateway, map[string]any{
			"arxiv_id": canonical,
			"status":   "failed",
			"error":    job.Err,
			"detail":   "MinerU conversion failed; it will be retried on a later request",
		})
	}

	return markdownProcessing(re, canonical, job)
}

// markdownRetryAfterSeconds is the baseline Retry-After hint sent on a 202
// (and on a "processing" status response). The client treats it as a lower
// bound and layers its own capped exponential backoff + jitter on top, so
// this only needs to be a sane floor — small enough that a quick conversion
// is picked up promptly, large enough not to hammer the API.
const markdownRetryAfterSeconds = 5

// markdownContentPath / markdownStatusPath build the relative URLs the
// client resolves against its own base URL. arxiv_id is already validated
// (no control chars / spaces) so it's safe to embed unescaped, consistent
// with the other /api/papers/* handlers.
func markdownContentPath(canonical string) string {
	return "/api/papers/" + canonical + "/markdown"
}

func markdownStatusPath(canonical string) string {
	return "/api/papers/" + canonical + "/markdown/status"
}

// markdownProcessing writes the 202 Accepted response for an in-flight
// conversion, including the Operation-Location + Retry-After headers.
func markdownProcessing(re *core.RequestEvent, canonical string, job *mineru.Job) error {
	re.Response.Header().Set("Operation-Location", markdownStatusPath(canonical))
	re.Response.Header().Set("Retry-After", strconv.Itoa(markdownRetryAfterSeconds))
	return re.JSON(http.StatusAccepted, map[string]any{
		"arxiv_id":   canonical,
		"status":     "processing",
		"state":      job.State,
		"started_at": job.StartedAt.UTC().Format(time.RFC3339),
		"status_url": markdownStatusPath(canonical),
		"detail":     "markdown is being generated by MinerU; poll the status_url (or Operation-Location header) until status==done, then GET the markdown endpoint",
	})
}

// markdownStatusHandler answers GET /api/papers/{arxiv_id}/markdown/status.
//
// This is the side-effect-free operation resource (Azure/Google AIP style):
// it never starts a conversion and never requires a PDF — it only reports
// the current state. It therefore *always* returns HTTP 200; the outcome is
// carried in the body's "status" field:
//
//   - done         → markdown is cached; "markdown_url" points at it.
//   - processing   → a conversion is queued/running (+ Retry-After header).
//   - failed       → the last conversion attempt failed ("error" set).
//   - not_started  → no conversion has been requested yet (GET the markdown
//     endpoint to start one).
//   - unavailable  → server-side conversion isn't configured (no token).
//
// 200-wrapping a "failed" state is intentional: the GET on the operation
// resource itself succeeded, so the HTTP status reflects the query, not the
// operation outcome (which lives in the body). The markdown content
// endpoint keeps the louder 502 for "I asked for markdown and couldn't get
// it".
func markdownStatusHandler(re *core.RequestEvent, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id: %q (version suffix vN required)", arxivID),
		})
	}

	// Cache hit wins regardless of in-process job state (covers the case
	// where markdown was produced in a previous process / by a contributor
	// and our job map is empty after a restart).
	if mdKey := resolveMarkdownKey(ctx, store, canonical); mdKey != "" {
		return re.JSON(http.StatusOK, map[string]any{
			"arxiv_id":     canonical,
			"status":       "done",
			"markdown_url": markdownContentPath(canonical),
		})
	}

	if converter != nil {
		if job := converter.Lookup(canonical); job != nil {
			switch job.State {
			case mineru.StateDone:
				// Job says done but cache missed above — unusual, but
				// report done + point at the content endpoint anyway.
				return re.JSON(http.StatusOK, map[string]any{
					"arxiv_id":     canonical,
					"status":       "done",
					"markdown_url": markdownContentPath(canonical),
					"finished_at":  job.FinishedAt.UTC().Format(time.RFC3339),
					"image_count":  job.ImageCount,
				})
			case mineru.StateFailed:
				return re.JSON(http.StatusOK, map[string]any{
					"arxiv_id":    canonical,
					"status":      "failed",
					"error":       job.Err,
					"finished_at": job.FinishedAt.UTC().Format(time.RFC3339),
					"detail":      "MinerU conversion failed; it will be retried on a later request to the markdown endpoint",
				})
			default: // queued / running
				re.Response.Header().Set("Retry-After", strconv.Itoa(markdownRetryAfterSeconds))
				return re.JSON(http.StatusOK, map[string]any{
					"arxiv_id":   canonical,
					"status":     "processing",
					"state":      job.State,
					"started_at": job.StartedAt.UTC().Format(time.RFC3339),
				})
			}
		}
	}

	if converter == nil || !converter.Enabled() {
		return re.JSON(http.StatusOK, map[string]any{
			"arxiv_id": canonical,
			"status":   "unavailable",
			"detail":   "server-side MinerU conversion is not configured (MINERU_API_TOKEN unset); markdown is only served from cache",
		})
	}

	return re.JSON(http.StatusOK, map[string]any{
		"arxiv_id": canonical,
		"status":   "not_started",
		"detail":   "no conversion has been requested; GET /api/papers/{arxiv_id}/markdown to start one",
	})
}

// resolveMarkdownKey returns the object key of the paper's cached markdown,
// or "" when none exists. It tries the canonical key first (cheap Stat)
// then falls back to the candidate-stem resolver.
func resolveMarkdownKey(ctx context.Context, store objstore.Store, canonical string) string {
	mdKey := paperassets.AssetKey("markdown", canonical)
	if _, exists, err := store.Stat(ctx, mdKey); err == nil && exists {
		return mdKey
	}
	if resolved := paperassets.ResolveAssetsViaStore(ctx, store, canonical); resolved.MarkdownPath != "" {
		return resolved.MarkdownPath
	}
	return ""
}

// requirePDF returns nil when a PDF for canonical is present in the store,
// else a descriptive error.
func requirePDF(ctx context.Context, store objstore.Store, canonical string) error {
	pdfKey := paperassets.AssetKey("pdf", canonical)
	if _, exists, err := store.Stat(ctx, pdfKey); err == nil && exists {
		return nil
	}
	if resolved := paperassets.ResolveAssetsViaStore(ctx, store, canonical); resolved.PDFPath != "" {
		return nil
	}
	return fmt.Errorf("no PDF in raw storage for %s; upload it first via /api/papers/{arxiv_id}/upload-pdf", canonical)
}

// streamMarkdown copies the stored markdown object to the response with the
// correct content type.
func streamMarkdown(re *core.RequestEvent, store objstore.Store, mdKey string) error {
	rc, info, err := store.Get(re.Request.Context(), mdKey)
	if err != nil {
		if objstore.IsNotFound(err) {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "markdown not found"})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "read markdown: " + err.Error()})
	}
	defer rc.Close()
	re.Response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.WriteHeader(http.StatusOK)
	_, err = io.Copy(re.Response, rc)
	return err
}

// ---------------------------------------------------------------------------
// MinerU claim handlers
// ---------------------------------------------------------------------------

func mineruClaimHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog *papers.Store, arxivID string, ttl int) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for claim: %q (version suffix vN required)", arxivID),
		})
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}

	// The lease is granted atomically by the catalog (single MERGE/SET
	// that only matches when the paper has a PDF, lacks markdown, and has
	// no live claim). The PDF URL handed to MinerU is the public arxiv
	// abstract URL — compliance-safe and avoids minting a share token per
	// claim.
	claim, err := catalog.Claim(ctx, papers.CreateOptions{
		ArxivID:    canonical,
		Requester:  requester,
		TTLSeconds: ttl,
		PDFURL:     papers.ArxivAbsURL(canonical),
	})
	if err != nil {
		switch {
		case errors.Is(err, papers.ErrCatalogUnavailable):
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (Neo4j unreachable); retry shortly",
			})
		case errors.Is(err, papers.ErrNotClaimable):
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("%s cannot be claimed: no PDF in catalog, or markdown already exists. Upload the PDF first via /api/papers/{arxiv_id}/upload-pdf", canonical),
			})
		}
		var dupErr *papers.ErrAlreadyClaimed
		if errors.As(err, &dupErr) {
			return re.JSON(http.StatusConflict, map[string]any{
				"detail": map[string]any{
					"message":          fmt.Sprintf("%s is already claimed", canonical),
					"claim_id":         dupErr.Existing.ClaimID,
					"claim_expires_at": dupErr.Existing.ExpiresAt,
					"claim_requester":  dupErr.Existing.Requester,
				},
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}

	slog.Info("mineru claim granted",
		"arxiv_id", canonical,
		"requester", requester,
		"claim_id", claim.ClaimID,
		"ttl_seconds", claim.TTLSeconds,
	)
	return re.JSON(http.StatusCreated, claim)
}

func mineruClaimReleaseHandler(re *core.RequestEvent, catalog *papers.Store, arxivID, claimID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for claim release: %q", arxivID),
		})
	}
	_, err := catalog.ReleaseClaim(re.Request.Context(), canonical, claimID)
	if err != nil {
		if errors.Is(err, papers.ErrIDMismatch) {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": "claim_id does not match the active claim",
			})
		}
		if errors.Is(err, papers.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (Neo4j unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	re.Response.WriteHeader(http.StatusNoContent)
	return nil
}

// ---------------------------------------------------------------------------
// Upload handlers
// ---------------------------------------------------------------------------

func uploadPDFHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog *papers.Store, arxivID string) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for upload: %q. Expected new-style 'YYMM.NNNNNvN' (post April 2007, e.g. '2501.00010v1') or old-style 'category/YYMMNNNvN' (pre April 2007, e.g. 'quant-ph/9508027v1'). An explicit version suffix is required.", arxivID),
		})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	expectedPdfSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256"))

	pdfKey := paperassets.AssetKey("pdf", canonical)

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxPDFBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}

	if _, has := re.Request.MultipartForm.File["pdf"]; !has {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing 'pdf' multipart part"})
	}
	pdfPart, hdr, err := re.Request.FormFile("pdf")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "open pdf part: " + err.Error()})
	}
	defer pdfPart.Close()

	contentType := hdr.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "pdf") && contentType != "application/octet-stream" {
		return re.JSON(http.StatusUnsupportedMediaType, map[string]string{
			"detail": fmt.Sprintf("expected application/pdf for 'pdf' part, got %q", contentType),
		})
	}

	// Stage PDF to a tmp file. The 100 MiB cap × concurrency would
	// blow process RAM on the 1.4 GB RackNerd VM; spooling to disk
	// trades a few syscalls for predictable memory use. The head-
	// peek validates %PDF- before we commit to copying the rest.
	pdfStaged, vErr := stageToTmpFile(ctx, pdfPart, paperassets.MaxPDFBytes, "pdf",
		5, // peek the first 5 bytes for the %PDF- magic
		func(head []byte) *uploadError {
			if len(head) < 5 || string(head[:5]) != "%PDF-" {
				return &uploadError{Status: http.StatusBadRequest,
					Detail: "uploaded file does not look like a PDF (missing %PDF- header)"}
			}
			return nil
		})
	if vErr != nil {
		return re.JSON(vErr.Status, map[string]string{"detail": vErr.Detail})
	}
	defer pdfStaged.Close()
	pdfSha := pdfStaged.Sha256()
	pdfSize := pdfStaged.Size()

	if expectedPdfSha != "" && expectedPdfSha != pdfSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":          "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256": expectedPdfSha,
			"actual_sha256":   pdfSha,
		})
	}

	// v0.7.0: the json/metadata sidecar bucket was cut — paper metadata
	// now lives in the Neo4j catalog (sourced from OpenAlex), so we no
	// longer accept or write a "metadata" multipart part. Any such part
	// in the request is ignored.
	pdfOutcome, err := uploadOne(ctx, store, pdfKey, pdfStaged, "application/pdf", overwrite, "PDF")
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}

	if pdfOutcome.kind == outcomeConflict {
		body := map[string]any{
			"detail":        "upload conflict; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
			"new_sha256":    pdfSha,
			"existing_path": pdfKey,
		}
		body["existing_sha256"] = pdfOutcome.existingShaJSON()
		if pdfOutcome.existingSha == "" {
			body["note"] = "existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return re.JSON(http.StatusConflict, body)
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	overallUnchanged := pdfOutcome.kind == outcomeUnchanged

	// Catalog write-through: flip has_pdf=true on the :PaperWork node
	// (creating a minimal arxiv-fallback node if the paper predates the
	// OpenAlex bootstrap). When Neo4j is down we still return success —
	// the object is durably written; `papers sync` reconciles later.
	catalogDeferred := false
	if err := catalog.UpsertPDF(ctx, canonical, pdfSha, pdfSize, pdfOutcome.existingSha); err != nil {
		if errors.Is(err, papers.ErrCatalogUnavailable) {
			catalogDeferred = true
		} else {
			slog.Warn("papers: UpsertPDF write-through failed", "arxiv_id", canonical, "error", err)
			catalogDeferred = true
		}
	}

	slog.Info("uploaded pdf",
		"arxiv_id", canonical,
		"requester", requester,
		"pdf_bytes", pdfSize,
		"pdf_sha256", pdfSha,
		"pdf_unchanged", pdfOutcome.kind == outcomeUnchanged,
		"catalog_deferred", catalogDeferred,
		"pdf_key", pdfKey,
	)

	resp := map[string]any{
		"arxiv_id":        canonical,
		"key":             paperassets.StorageKey(canonical),
		"pdf_path":        pdfKey,
		"pdf_bytes":       pdfSize,
		"pdf_sha256":      pdfSha,
		"pdf_unchanged":   pdfOutcome.kind == outcomeUnchanged,
		"metadata_path":   nil,
		"metadata_bytes":  nil,
		"metadata_sha256": nil,
		"uploaded_by":     nil,
		"overwritten":     overwrite,
		"unchanged":       overallUnchanged,
	}
	if requester != "" {
		resp["uploaded_by"] = requester
	}
	if catalogDeferred {
		re.Response.Header().Set("X-Catalog-Sync", "deferred")
	}
	// Status: 200 OK if everything was a no-op (idempotent re-upload
	// of identical content), 201 Created otherwise.
	if overallUnchanged {
		re.Response.WriteHeader(http.StatusOK)
	} else {
		re.Response.WriteHeader(http.StatusCreated)
	}
	return jsonBody(re, resp)
}

func uploadMarkdownHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog *papers.Store, arxivID string) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for upload: %q. version suffix vN required.", arxivID),
		})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	expectedSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256"))
	source := re.Request.URL.Query().Get("source")
	if len(source) > 64 {
		source = source[:64]
	}

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxMarkdownBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}

	mdKey := paperassets.AssetKey("markdown", canonical)

	mdPart, _, err := re.Request.FormFile("markdown")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing 'markdown' multipart part: " + err.Error()})
	}
	defer mdPart.Close()

	mdStaged, vErr := stageInMemory(ctx, mdPart, paperassets.MaxMarkdownBytes, "markdown",
		func(b []byte) *uploadError {
			if !utf8.Valid(b) {
				return &uploadError{Status: http.StatusBadRequest, Detail: "markdown must be valid utf-8"}
			}
			return nil
		})
	if vErr != nil {
		return re.JSON(vErr.Status, map[string]string{"detail": vErr.Detail})
	}
	defer mdStaged.Close()
	mdSha := mdStaged.Sha256()
	mdSize := mdStaged.Size()
	if expectedSha != "" && expectedSha != mdSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":          "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256": expectedSha,
			"actual_sha256":   mdSha,
		})
	}

	outcome, err := uploadOne(ctx, store, mdKey, mdStaged, "text/markdown; charset=utf-8", overwrite, "markdown")
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if outcome.kind == outcomeConflict {
		body := map[string]any{
			"detail":        "markdown already exists at " + mdKey + " with different content; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
			"existing_path": mdKey,
			"new_sha256":    mdSha,
		}
		if outcome.existingSha != "" {
			body["existing_sha256"] = outcome.existingSha
		} else {
			body["existing_sha256"] = nil
			body["note"] = "existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return re.JSON(http.StatusConflict, body)
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	slog.Info("uploaded markdown",
		"arxiv_id", canonical,
		"requester", requester,
		"source", source,
		"md_bytes", mdSize,
		"md_sha256", mdSha,
		"md_unchanged", outcome.kind == outcomeUnchanged,
		"md_key", mdKey,
	)

	// Catalog write-through: flip has_md=true and clear any active claim
	// (markdown done ⇒ lease no longer needed). Deferred when Neo4j down.
	catalogDeferred := false
	if err := catalog.UpsertMD(ctx, canonical, mdSha, mdSize, outcome.existingSha); err != nil {
		if !errors.Is(err, papers.ErrCatalogUnavailable) {
			slog.Warn("papers: UpsertMD write-through failed", "arxiv_id", canonical, "error", err)
		}
		catalogDeferred = true
	}

	resp := map[string]any{
		"arxiv_id":       canonical,
		"key":            paperassets.StorageKey(canonical),
		"markdown_path":  mdKey,
		"markdown_bytes": mdSize,
		"sha256":         mdSha,
		"unchanged":      outcome.kind == outcomeUnchanged,
		"source":         nil,
		"uploaded_by":    nil,
		"overwritten":    overwrite,
	}
	if source != "" {
		resp["source"] = source
	}
	if requester != "" {
		resp["uploaded_by"] = requester
	}
	if catalogDeferred {
		re.Response.Header().Set("X-Catalog-Sync", "deferred")
	}
	if outcome.kind == outcomeUnchanged {
		re.Response.WriteHeader(http.StatusOK)
	} else {
		re.Response.WriteHeader(http.StatusCreated)
	}
	return jsonBody(re, resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// uploadError is the typed error from the upload helpers — carries the
// HTTP status code so the route handler can faithfully surface it.
type uploadError struct {
	Status int
	Detail string
}

func (e *uploadError) Error() string { return e.Detail }

// uploadOutcome captures what happened to a single store key after the
// upload-one helper ran. The handler uses kind to map onto HTTP status
// (written / unchanged → 200/201; conflict → 409); existingSha and the
// response builder use it to populate the JSON body.
type uploadOutcome struct {
	kind        outcomeKind
	existingSha string
}

// existingShaJSON returns the existing-object sha256 in a form suitable
// for a JSON response: a hex string when known, nil ("null" on the wire)
// when the existing object had no sha256 metadata.
func (o uploadOutcome) existingShaJSON() any {
	if o.existingSha == "" {
		return nil
	}
	return o.existingSha
}

type outcomeKind int

const (
	outcomeWritten   outcomeKind = iota // object freshly stored (or replaced via overwrite)
	outcomeUnchanged                    // existing object has the same sha256 — zero writes
	outcomeConflict                     // existing differs and overwrite was not requested
)

// uploadOne is the conditional-write driver shared by uploadPDFHandler
// and uploadMarkdownHandler. It encodes the new race-safe contract:
//
//   - Without overwrite:
//     1. Try `Put + If-None-Match: "*"`. On success, return Written.
//     2. On 412 PreconditionFailed: Stat the existing object, compare
//     sha256 metadata. Match → Unchanged (idempotent re-upload).
//     Mismatch (or missing) → Conflict.
//     3. On the rare "412 then key disappears" race, retry the
//     create-only Put once; if even that 412s, return Conflict.
//
//   - With overwrite:
//     1. Stat first. If sha256 matches, return Unchanged (zero writes —
//     same content-aware idempotency the old code had, preserved).
//     2. Otherwise, unconditional Put. We deliberately do NOT use
//     If-Match here: paper assets are mostly single-writer per
//     arxiv_id, and the CAS-retry-loop adds complexity without much
//     benefit when the operator already opted in to "replace". The
//     S3 backend's bucket versioning preserves the prior version so
//     the loser of a true concurrent overwrite stays recoverable.
//
//   - LocalStore fallback: LocalStore implements If-None-Match="*" via
//     atomic os.Link (so create-only really is race-safe even on local
//     dev). If a future backend returns ErrPreconditionUnsupported we
//     fall through to the Stat+classify path — same correctness story
//     as the old non-conditional code on that backend.
//
// body is a stagedBody (in-memory for small uploads, tmp-file for
// large ones — see stagedupload.go). uploadOne opens it up to twice:
// once for the initial Put, once on the rare retry path. body itself
// is owned by the caller (handler defers Close); uploadOne does not
// close it.
//
// label is the human-readable kind ("PDF" / "metadata" / "markdown")
// embedded in the wrapped error message for ops triage.
func uploadOne(
	ctx context.Context,
	store objstore.Store,
	key string,
	body stagedBody,
	contentType string,
	overwrite bool,
	label string,
) (uploadOutcome, error) {
	sha := body.Sha256()
	size := body.Size()
	metadata := map[string]string{"sha256": sha}

	// putOnce opens the staged body once, fires a single conditional
	// Put, and ensures the reader is closed. Each retry of uploadOne
	// calls putOnce fresh so multiple Open() calls don't share an fd.
	putOnce := func(opts objstore.PutOptions) error {
		r, err := body.Open()
		if err != nil {
			return fmt.Errorf("open staged body: %w", err)
		}
		defer r.Close()
		_, err = store.PutWithOptions(ctx, key, r, size, opts)
		return err
	}

	if !overwrite {
		err := putOnce(objstore.PutOptions{
			ContentType: contentType,
			Metadata:    metadata,
			IfNoneMatch: "*",
		})
		if err == nil {
			return uploadOutcome{kind: outcomeWritten}, nil
		}
		if !objstore.IsPreconditionFailed(err) {
			// Anything other than 412 is a real failure (network, auth,
			// backend doesn't speak preconditions, etc.). Bubble up.
			// Both backends we support (S3/RustFS and LocalStore) DO
			// implement If-None-Match="*", so ErrPreconditionUnsupported
			// here would mean a misconfigured / new backend — louder
			// failure is better than silently degrading to the racy
			// non-conditional path.
			return uploadOutcome{}, fmt.Errorf("put %s (%s): %w", key, label, err)
		}
		// 412 → object exists; fall through to Stat+classify.
	}

	info, exists, err := store.Stat(ctx, key)
	if err != nil {
		return uploadOutcome{}, fmt.Errorf("stat %s (%s): %w", key, label, err)
	}
	existingSha := ""
	if exists && info.Metadata != nil {
		existingSha = info.Metadata["sha256"]
	}

	if exists && existingSha != "" && existingSha == sha {
		return uploadOutcome{kind: outcomeUnchanged, existingSha: existingSha}, nil
	}

	if !overwrite {
		if exists {
			return uploadOutcome{kind: outcomeConflict, existingSha: existingSha}, nil
		}
		// Object got created (we saw 412) and then deleted before our
		// Stat. Retry the create-only Put exactly once; if it 412s
		// again, give up and report Conflict. Looping further would
		// risk livelock against an aggressive concurrent writer.
		err := putOnce(objstore.PutOptions{
			ContentType: contentType,
			Metadata:    metadata,
			IfNoneMatch: "*",
		})
		if err == nil {
			return uploadOutcome{kind: outcomeWritten}, nil
		}
		if objstore.IsPreconditionFailed(err) {
			return uploadOutcome{kind: outcomeConflict}, nil
		}
		return uploadOutcome{}, fmt.Errorf("put %s (%s) after race: %w", key, label, err)
	}

	// Overwrite path: unconditional Put. Bucket versioning preserves
	// the prior version when enabled on the S3 backend.
	if err := putOnce(objstore.PutOptions{
		ContentType: contentType,
		Metadata:    metadata,
	}); err != nil {
		return uploadOutcome{}, fmt.Errorf("put %s (%s): %w", key, label, err)
	}
	return uploadOutcome{kind: outcomeWritten, existingSha: existingSha}, nil
}

// normaliseSha256Hex returns the lower-case hex digest when v looks
// like a 64-char sha256 hex string, else "". Used to scrub the
// expected_sha256 query param into a comparable form (or drop it).
func normaliseSha256Hex(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 64 {
		return ""
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return v
}

func jsonBody(re *core.RequestEvent, payload any) error {
	re.Response.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(re.Response)
	return enc.Encode(payload)
}
