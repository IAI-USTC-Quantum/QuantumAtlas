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
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineruclaim"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperindex"
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
// shareStore + claimStore are the on-disk JSON stores for share token
// records and MinerU claim leases respectively; they remain local
// (DataDir) regardless of rawStore backend.
//
// paperIndex is the in-process Parquet+DuckDB catalog (paperindex.Store)
// used by collection-style endpoints (needs-mineru, future stats /
// range queries). MAY BE nil if the deployment hasn't set up the
// catalog yet — handlers fall back to the legacy store.ListPrefix
// path in that case (slow but correct). See docs/architecture.md for
// the lakehouse rationale.
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
	claimStore *mineruclaim.Store,
	paperIndex *paperindex.Store,
	converter *mineru.Converter,
	enforcer *casbin.Enforcer,
) {
	se.Router.GET("/api/papers/{path...}", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		if raw == "needs-mineru" {
			return needsMineruHandler(re, rawStore, claimStore, paperIndex)
		}
		if raw == "stats" {
			return paperStatsHandler(re, paperIndex)
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
			return uploadPDFHandler(re, cfg, rawStore, arxiv)
		case "upload-markdown":
			return uploadMarkdownHandler(re, cfg, rawStore, claimStore, arxiv)
		case "mineru-claim":
			ttl, _ := strconv.Atoi(re.Request.URL.Query().Get("ttl_seconds"))
			if ttl <= 0 {
				ttl = mineruclaim.DefaultTTLSeconds
			}
			return mineruClaimHandler(re, cfg, rawStore, shareStore, claimStore, arxiv, ttl)
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
		return mineruClaimReleaseHandler(re, claimStore, arxiv, claimID)
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
// exposing the paperindex aggregate counters so the SPA can show
// "downloaded papers" (has_pdf) and "converted markdown" (has_md) tiles
// on the home/wiki pages.
//
// When paperIndex is nil (deployment hasn't configured the Parquet+DuckDB
// catalog, e.g. local dev with no S3) we degrade to {available:false}
// rather than 500 — the frontend simply hides the tiles.
func paperStatsHandler(re *core.RequestEvent, paperIndex *paperindex.Store) error {
	if paperIndex == nil {
		return re.JSON(http.StatusOK, map[string]any{"available": false})
	}
	ctx := re.Request.Context()
	stats, err := paperIndex.QueryStats(ctx)
	if err != nil {
		slog.Warn("paperindex: QueryStats failed for /api/papers/stats", "error", err)
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
// Two implementations live behind this entry point:
//
//  1. paperindex-backed (fast path): when paperIndex != nil and the
//     catalog has been bootstrapped, queries hit the in-memory DuckDB
//     table in <1ms. This is the production code path on edges
//     where docs/architecture.md's lakehouse model is enabled.
//
//  2. enumerateNeedsMineru (legacy fallback): when paperIndex is nil
//     OR the catalog reports zero rows (parquet not bootstrapped yet),
//     fall through to the original two-LIST + in-memory-diff impl.
//     This used to be the only impl and is documented as slow /
//     prone to RustFS-beta 500s — it's kept as a safety net for
//     fresh deployments that haven't yet run the bootstrap-index
//     subcommand.
//
// Response shape is identical across both paths so existing CLI /
// dashboard consumers don't have to branch.
func needsMineruHandler(re *core.RequestEvent, store objstore.Store, claimStore *mineruclaim.Store, paperIndex *paperindex.Store) error {
	limit, _ := strconv.Atoi(re.Request.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}
	includeClaimed := re.Request.URL.Query().Get("include_claimed") == "true"

	// Fast path: paperindex catalog.
	if paperIndex != nil && paperIndex.RowCount() > 0 {
		ctx := re.Request.Context()
		stats, err := paperIndex.QueryStats(ctx)
		if err == nil {
			// Fetch a window of MinerU candidates that we then
			// merge with live claim state (claims live in
			// claimStore, NOT in the parquet — they're short-
			// lived and would always be stale in the catalog).
			//
			// We over-fetch a bit (limit*3, capped at 100) so
			// that even when many candidates are already
			// claimed we usually return `limit` actionable
			// rows without paging through the parquet.
			fetchN := limit * 3
			if fetchN > 100 {
				fetchN = 100
			}
			candidates, err := paperIndex.NeedsMineru(ctx, fetchN)
			if err == nil {
				return re.JSON(http.StatusOK, buildNeedsMineruResponse(
					ctx, candidates, claimStore, limit, includeClaimed, stats.NeedsMineru))
			}
			slog.Warn("paperindex: NeedsMineru query failed; falling back to legacy LIST",
				"error", err)
		} else {
			slog.Warn("paperindex: QueryStats failed; falling back to legacy LIST", "error", err)
		}
	}

	// Legacy fallback: full S3 LIST + in-memory diff. Documented as
	// slow but correct.
	papers, unclaimed, claimedCount := enumerateNeedsMineru(re.Request.Context(), store, claimStore, limit, includeClaimed)
	return re.JSON(http.StatusOK, map[string]any{
		"papers":          papers,
		"returned":        len(papers),
		"total_unclaimed": unclaimed,
		"total_claimed":   claimedCount,
	})
}

// buildNeedsMineruResponse merges paperindex candidate rows with live
// claim-store state and projects the result into the legacy
// needs-mineru JSON shape. totalNeedsMineru is the global count from
// QueryStats (used to populate total_unclaimed + total_claimed
// approximately without scanning the whole claim store).
//
// Caveat: total_claimed here is *estimated* as (candidates we saw
// that were claimed) — we don't enumerate all claims globally because
// the claim store is a per-file dir scan and would be slow at 10⁴+
// papers. For dashboard purposes this is good enough; if precise
// numbers are required, a future endpoint can scan claimStore
// explicitly.
func buildNeedsMineruResponse(
	ctx context.Context,
	candidates []paperindex.NeedsMineruRow,
	claimStore *mineruclaim.Store,
	limit int,
	includeClaimed bool,
	totalNeedsMineru int,
) map[string]any {
	now := time.Now().UTC()
	out := make([]map[string]any, 0, limit)
	claimedSeen := 0
	for _, c := range candidates {
		claim, _ := claimStore.Read(c.ArxivID)
		isClaimed := mineruclaim.IsActive(claim, now)
		if isClaimed {
			claimedSeen++
			if !includeClaimed {
				continue
			}
		}
		if len(out) >= limit {
			continue
		}
		paper := map[string]any{
			"arxiv_id":         c.ArxivID,
			"key":              c.ArxivID,
			"pdf_path":         c.PDFKey,
			"claimed":          isClaimed,
			"claim_expires_at": nil,
			"claim_requester":  nil,
		}
		if claim != nil && isClaimed {
			paper["claim_expires_at"] = claim.ExpiresAt
			paper["claim_requester"] = claim.Requester
		}
		out = append(out, paper)
	}
	// total_unclaimed is the global "has_pdf AND NOT has_md" count
	// minus the claimed-seen subset (lower bound; precise number would
	// require enumerating all claims). For most dashboards this is
	// close enough.
	unclaimed := totalNeedsMineru - claimedSeen
	if unclaimed < 0 {
		unclaimed = 0
	}
	return map[string]any{
		"papers":          out,
		"returned":        len(out),
		"total_unclaimed": unclaimed,
		"total_claimed":   claimedSeen,
	}
}

// enumerateNeedsMineru lists every pdf/* object and surfaces the ones
// that don't have a sibling markdown/*. Bounded by limit (returned set)
// but always tallies the full totals for the dashboard counter.
//
// One ListPrefix call per kind (pdf, markdown). Bounded set of basenames
// is held in memory — fine for the ~10⁴-10⁵ paper count we expect on
// any one bucket.
func enumerateNeedsMineru(ctx context.Context, store objstore.Store, claimStore *mineruclaim.Store, limit int, includeClaimed bool) ([]map[string]any, int, int) {
	pdfs, err := store.ListPrefix(ctx, "pdf/", 0)
	if err != nil || len(pdfs) == 0 {
		return nil, 0, 0
	}
	// Set of stems that already have markdown — drives the "needs work" filter.
	mdObjs, _ := store.ListPrefix(ctx, "markdown/", 0)
	mdStems := map[string]struct{}{}
	for _, o := range mdObjs {
		base := path.Base(o.Key)
		mdStems[strings.TrimSuffix(base, path.Ext(base))] = struct{}{}
	}

	// Stable iteration order so dashboards aren't randomized between
	// calls (S3 list order is unspecified across S3-compatible vendors).
	sort.Slice(pdfs, func(i, j int) bool { return pdfs[i].Key < pdfs[j].Key })

	now := time.Now().UTC()
	var papers []map[string]any
	totalUnclaimed := 0
	totalClaimed := 0
	seen := map[string]struct{}{}

	for _, pdf := range pdfs {
		base := path.Base(pdf.Key)
		key := strings.TrimSuffix(base, path.Ext(base))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, hasMD := mdStems[key]; hasMD {
			continue
		}

		canonical := key
		claim, _ := claimStore.Read(canonical)
		claimed := mineruclaim.IsActive(claim, now)
		if claimed {
			totalClaimed++
		} else {
			totalUnclaimed++
		}
		if claimed && !includeClaimed {
			continue
		}
		if len(papers) >= limit {
			continue
		}

		paper := map[string]any{
			"arxiv_id":         canonical,
			"key":              key,
			"pdf_path":         pdf.Key,
			"claimed":          claimed,
			"claim_expires_at": nil,
			"claim_requester":  nil,
		}
		if claim != nil && claimed {
			paper["claim_expires_at"] = claim.ExpiresAt
			paper["claim_requester"] = claim.Requester
		}
		papers = append(papers, paper)
	}
	return papers, totalUnclaimed, totalClaimed
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

func mineruClaimHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, shareStore *shares.Store, claimStore *mineruclaim.Store, arxivID string, ttl int) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for claim: %q (version suffix vN required)", arxivID),
		})
	}

	// PDF must already be present — refuse to claim a paper we can't serve.
	pdfKey := paperassets.AssetKey("pdf", canonical)
	_, exists, err := store.Stat(ctx, pdfKey)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "stat pdf: " + err.Error()})
	}
	if !exists {
		resolved := paperassets.ResolveAssetsViaStore(ctx, store, canonical)
		if resolved.PDFPath == "" {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("no PDF in raw storage for %s; upload it first via /api/papers/{arxiv_id}/upload-pdf", canonical),
			})
		}
		pdfKey = resolved.PDFPath
	}

	// No work if markdown already exists (both exact + resolved variants).
	mdKey := paperassets.AssetKey("markdown", canonical)
	if _, mdExists, _ := store.Stat(ctx, mdKey); mdExists {
		return re.JSON(http.StatusConflict, map[string]string{
			"detail": fmt.Sprintf("markdown already exists for %s; nothing to do", canonical),
		})
	}
	if resolved := paperassets.ResolveAssetsViaStore(ctx, store, canonical); resolved.MarkdownPath != "" {
		return re.JSON(http.StatusConflict, map[string]string{
			"detail": fmt.Sprintf("markdown already exists for %s; nothing to do", canonical),
		})
	}

	// Build the PDF share URL the claimant will hand to MinerU.
	relSharePath := paperassets.ShareRelPathForKey(pdfKey)
	shareToken := cfg.ShareAccessToken
	shareBaseURL := ""
	if shareToken != "" {
		shareBaseURL = cfg.PublicBaseURL
	} else {
		rec, err := shares.CreateRecord(shareStore, cfg, shares.CreateOptions{
			Paths: []string{relSharePath},
			Label: "mineru pdf: " + canonical,
		}, store)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "failed to build share URL for PDF: " + err.Error(),
			})
		}
		shareToken = rec.Token
	}
	shareURL := shares.BuildURL(shareToken, relSharePath, shareBaseURL)

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}

	claim, err := claimStore.Create(mineruclaim.CreateOptions{
		ArxivID:    canonical,
		Requester:  requester,
		TTLSeconds: ttl,
		PDFURL:     shareURL,
	})
	if err != nil {
		var dupErr *mineruclaim.ErrAlreadyClaimed
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

func mineruClaimReleaseHandler(re *core.RequestEvent, claimStore *mineruclaim.Store, arxivID, claimID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for claim release: %q", arxivID),
		})
	}
	_, err := claimStore.ReleaseWithID(canonical, claimID)
	if err != nil {
		if errors.Is(err, mineruclaim.ErrIDMismatch) {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": "claim_id does not match the active claim",
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

func uploadPDFHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, arxivID string) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for upload: %q. Expected new-style 'YYMM.NNNNNvN' (post April 2007, e.g. '2501.00010v1') or old-style 'category/YYMMNNNvN' (pre April 2007, e.g. 'quant-ph/9508027v1'). An explicit version suffix is required.", arxivID),
		})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	expectedPdfSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256"))
	expectedMetaSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_metadata_sha256"))

	pdfKey := paperassets.AssetKey("pdf", canonical)
	jsonKey := paperassets.AssetKey("json", canonical)

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxPDFBytes) + int64(paperassets.MaxMetadataBytes) + 1<<20); err != nil {
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

	// Stage metadata JSON if present, BEFORE any store writes — so a
	// metadata-side validation failure doesn't even attempt a PDF PUT.
	// Metadata is small (cap 2 MiB) and json.Unmarshal needs the full
	// body, so we keep this one in memory.
	var metaStaged stagedBody
	var metaSha string
	hasMetadata := false
	if mdPart, _, err := re.Request.FormFile("metadata"); err == nil && mdPart != nil {
		defer mdPart.Close()
		hasMetadata = true
		var mvErr *uploadError
		metaStaged, mvErr = stageInMemory(ctx, mdPart, paperassets.MaxMetadataBytes, "metadata",
			func(b []byte) *uploadError {
				var v any
				if json.Unmarshal(b, &v) != nil {
					return &uploadError{Status: http.StatusBadRequest, Detail: "metadata must be valid utf-8 JSON"}
				}
				return nil
			})
		if mvErr != nil {
			return re.JSON(mvErr.Status, map[string]string{"detail": mvErr.Detail})
		}
		defer metaStaged.Close()
		metaSha = metaStaged.Sha256()
		if expectedMetaSha != "" && expectedMetaSha != metaSha {
			return re.JSON(http.StatusBadRequest, map[string]any{
				"detail":                   "expected_metadata_sha256 mismatch — metadata may be corrupt in transit",
				"expected_metadata_sha256": expectedMetaSha,
				"actual_metadata_sha256":   metaSha,
			})
		}
	}

	// Each part runs the conditional-write flow independently. We do
	// the PDF first so a metadata-side conflict can still leave the
	// PDF in place (the prior contract — "metadata conflict shouldn't
	// leave PDF written" — is intentionally relaxed; under conditional
	// writes each key's content is idempotent + race-safe on its own,
	// and a client that gets a metadata 409 can retry idempotently
	// because the PDF write was content-addressed via sha256 metadata).
	pdfOutcome, err := uploadOne(ctx, store, pdfKey, pdfStaged, "application/pdf", overwrite, "PDF")
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	var metaOutcome uploadOutcome
	if hasMetadata {
		metaOutcome, err = uploadOne(ctx, store, jsonKey, metaStaged, "application/json", overwrite, "metadata")
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
	}

	// If either part conflicted, surface a 409 with both per-part
	// hashes so the client knows exactly which part(s) need attention.
	if pdfOutcome.kind == outcomeConflict || (hasMetadata && metaOutcome.kind == outcomeConflict) {
		body := map[string]any{
			"detail":     "upload conflict; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
			"new_sha256": pdfSha,
		}
		if pdfOutcome.kind == outcomeConflict {
			body["pdf_conflict"] = true
			body["pdf_existing_sha256"] = pdfOutcome.existingShaJSON()
			body["existing_path"] = pdfKey
		}
		if hasMetadata && metaOutcome.kind == outcomeConflict {
			body["metadata_conflict"] = true
			body["metadata_existing_sha256"] = metaOutcome.existingShaJSON()
			body["metadata_new_sha256"] = metaSha
			body["metadata_path"] = jsonKey
		}
		// Note when either side lacked sha256 metadata so the operator
		// knows it's a legacy / LocalStore object, not a hash mismatch.
		if (pdfOutcome.kind == outcomeConflict && pdfOutcome.existingSha == "") ||
			(hasMetadata && metaOutcome.kind == outcomeConflict && metaOutcome.existingSha == "") {
			body["note"] = "at least one existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return re.JSON(http.StatusConflict, body)
	}

	metadataPath := ""
	if hasMetadata {
		metadataPath = jsonKey
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	var metaSize int64
	if hasMetadata {
		metaSize = metaStaged.Size()
	}
	overallUnchanged := pdfOutcome.kind == outcomeUnchanged && (!hasMetadata || metaOutcome.kind == outcomeUnchanged)
	slog.Info("uploaded pdf",
		"arxiv_id", canonical,
		"requester", requester,
		"pdf_bytes", pdfSize,
		"pdf_sha256", pdfSha,
		"pdf_unchanged", pdfOutcome.kind == outcomeUnchanged,
		"metadata_bytes", metaSize,
		"metadata_sha256", metaSha,
		"metadata_unchanged", hasMetadata && metaOutcome.kind == outcomeUnchanged,
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
	if metadataPath != "" {
		resp["metadata_path"] = metadataPath
		resp["metadata_bytes"] = metaSize
		resp["metadata_sha256"] = metaSha
		resp["metadata_unchanged"] = metaOutcome.kind == outcomeUnchanged
	}
	if requester != "" {
		resp["uploaded_by"] = requester
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

func uploadMarkdownHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, claimStore *mineruclaim.Store, arxivID string) error {
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

	if err := claimStore.Release(canonical); err != nil {
		slog.Warn("failed to release mineru claim", "arxiv_id", canonical, "error", err)
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
//        sha256 metadata. Match → Unchanged (idempotent re-upload).
//        Mismatch (or missing) → Conflict.
//     3. On the rare "412 then key disappears" race, retry the
//        create-only Put once; if even that 412s, return Conflict.
//
//   - With overwrite:
//     1. Stat first. If sha256 matches, return Unchanged (zero writes —
//        same content-aware idempotency the old code had, preserved).
//     2. Otherwise, unconditional Put. We deliberately do NOT use
//        If-Match here: paper assets are mostly single-writer per
//        arxiv_id, and the CAS-retry-loop adds complexity without much
//        benefit when the operator already opted in to "replace". The
//        S3 backend's bucket versioning preserves the prior version so
//        the loser of a true concurrent overwrite stays recoverable.
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
