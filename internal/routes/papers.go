package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterPapers wires the /api/papers/* endpoints.
//
// rawStore is the abstracted asset backend (LocalStore for cfg.RawDir
// or S3Store for QATLAS_S3_* on RustFS). Every PDF / markdown / image
// touched by these handlers flows through this interface — never
// directly via os.*, so the same routes work against either backend.
//
// catalog is the Neo4j-backed papers catalog (papers.Store) that owns
// all collection-style metadata: aggregate stats, the needs-mineru
// queue, MinerU claim leases, and upload write-through. It degrades
// gracefully (ErrCatalogUnavailable) when Neo4j is unreachable — read
// endpoints report {available:false}; uploads still write the object
// and defer the catalog sync (X-Catalog-Sync: deferred).
//
// enforcer is the process-wide casbin enforcer used to gate endpoints by
// PAT scope: GET (stats / needs-mineru queue) requires papers:read,
// POST/DELETE require papers:write (which implies papers:read).
// Session-token callers bypass via the ScopeMaster short-circuit in
// pat.Allows.
//
// Compliance note (v0.9.0): the OSS server only serves collection
// metadata outbound. Endpoints that streamed PDF / markdown / image
// bytes (/share/*, GET /api/papers/{id}/{markdown,resources}, the whole
// /api/shares family) were removed — the OSS deployment holds papers
// for internal use but does not redistribute them. Contributors fetch
// PDFs from arxiv.org themselves (mineru-claim ships the arxiv URL +
// our reference sha256 so they can verify byte equality) and push
// MinerU output back via upload-mineru.
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
	catalog *papers.Store,
	enforcer *casbin.Enforcer,
) {
	se.Router.GET("/api/papers/{path...}", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		if raw == "needs-mineru" {
			return needsMineruHandler(re, catalog)
		}
		if raw == "stats" {
			return paperStatsHandler(re, catalog)
		}
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no GET handler for /api/papers/%s", raw),
		})
	}))

	se.Router.POST("/api/papers/{path...}", scopeGuard(enforcer, "papers", "write", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		arxiv, action := splitPapersPath(raw)
		switch action {
		case "upload-pdf":
			return uploadPDFHandler(re, cfg, rawStore, catalog, arxiv)
		case "upload-mineru":
			return uploadMinerUHandler(re, cfg, rawStore, catalog, arxiv)
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

// ---------------------------------------------------------------------------
// stats
// ---------------------------------------------------------------------------

// paperStatsHandler answers GET /api/papers/stats — exposing the
// catalog aggregate counters so the SPA can show "downloaded papers"
// (has_pdf) and "converted markdown" (has_md) tiles on the home/wiki
// pages.
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
// MinerU claim handlers
// ---------------------------------------------------------------------------

// mineruClaimHandler answers POST /api/papers/{arxiv_id}/mineru-claim.
//
// The lease lets a contributor reserve a paper for MinerU conversion
// without other contributors stepping on the same work. The response
// is a Claim record carrying:
//
//   - pdf_url: the canonical arxiv.org versioned URL the contributor
//     must fetch the PDF from. The OSS server never re-distributes
//     PDF bytes — this is the only sanctioned source. arxiv URLs with
//     an explicit version suffix are immutable (a published v1 keeps
//     the same bytes forever even after v2 supersedes it), so the
//     hash check on upload-mineru below is well-defined.
//   - pdf_sha256: the sha256 of the PDF currently stored in the
//     catalog's RustFS, read from the object's user metadata. The
//     contributor SHOULD verify the bytes they fetched from arxiv
//     match this hash before running MinerU; on upload-mineru the
//     server re-checks this hash and refuses 400 on mismatch. May
//     be empty for legacy objects (uploaded before sidecar metadata
//     persistence) — in that case verification is skipped and the
//     contributor's bytes are trusted as a backfill.
//
// The lease itself is granted atomically by the catalog (single
// MERGE/SET that only matches when the paper has a PDF, lacks
// markdown, and has no live claim).
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

	pdfURL := papers.ArxivVersionedURL(canonical)
	pdfSha256 := lookupStoredPDFSha256(ctx, store, canonical)

	claim, err := catalog.Claim(ctx, papers.CreateOptions{
		ArxivID:    canonical,
		Requester:  requester,
		TTLSeconds: ttl,
		PDFURL:     pdfURL,
		PDFSha256:  pdfSha256,
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
		"pdf_sha256_known", pdfSha256 != "",
	)
	return re.JSON(http.StatusCreated, claim)
}

// lookupStoredPDFSha256 returns the sha256 (lowercase hex) of the
// currently-stored PDF for canonical, read from object user metadata.
// Returns "" when the object has no sha256 metadata (legacy upload,
// LocalStore without sidecar, or backend that doesn't surface
// metadata) — callers MUST treat empty as "verification unavailable"
// rather than "verification failed".
func lookupStoredPDFSha256(ctx context.Context, store objstore.Store, canonical string) string {
	pdfKey := paperassets.AssetKey("pdf", canonical)
	info, exists, err := store.Stat(ctx, pdfKey)
	if err != nil || !exists {
		// Try the candidate-stem resolver as fallback so we still
		// surface the hash when the PDF lives under a non-canonical
		// key (e.g. a categoryless old-style id variant).
		if resolved := paperassets.ResolveAssetsViaStore(ctx, store, canonical); resolved.PDFPath != "" {
			if alt, ok, err := store.Stat(ctx, resolved.PDFPath); err == nil && ok {
				info = alt
			}
		}
	}
	if info.Metadata == nil {
		return ""
	}
	return strings.ToLower(info.Metadata["sha256"])
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
	// blow process RAM on a memory-tight VM (~1 GB class); spooling to
	// disk trades a few syscalls for predictable memory use. The head-
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
	// now lives in the Neo4j catalog (sourced from OpenAlex), so the
	// handler only accepts a single 'pdf' multipart part. Any other
	// parts in the request are ignored.
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
		"arxiv_id":      canonical,
		"key":           paperassets.StorageKey(canonical),
		"pdf_path":      pdfKey,
		"pdf_bytes":     pdfSize,
		"pdf_sha256":    pdfSha,
		"pdf_unchanged": pdfOutcome.kind == outcomeUnchanged,
		"uploaded_by":   nil,
		"overwritten":   overwrite,
		"unchanged":     overallUnchanged,
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

// uploadMinerUHandler accepts a MinerU output bundle (a zip containing
// `full.md` and optional `images/<file>` entries) and stores the
// markdown plus every extracted image to the asset backend.
//
// This endpoint replaces the v0.7.x `upload-markdown` route (which
// only accepted a single .md file and silently dropped any images).
// The new contract:
//
//   - Request: multipart/form-data, single part `mineru_zip` containing
//     the raw MinerU result zip exactly as returned by MinerU's
//     `full_zip_url`. Query params: overwrite=true (default false),
//     expected_sha256=<hex> (validates the zip bytes haven't been
//     corrupted in transit), pdf_sha256=<hex> (the sha256 of the
//     source PDF the contributor fed to MinerU; cross-checked against
//     the catalog's stored PDF metadata to catch contributors who ran
//     MinerU on the wrong arxiv version or a corrupted PDF — empty
//     when the stored PDF has no sha256 metadata or the contributor
//     opted out), source=<short label>.
//   - On success: markdown lands at AssetKey("markdown", canonical),
//     each image at AssetKey("images", canonical)+"/"+<name>. Catalog
//     write-through flips has_md=true and clears any pending claim.
//   - Order: images first, markdown last — markdown is the completion
//     marker (`papers sync` and detail-page readers use the markdown
//     object's presence to know "this paper is parsed"), so writing
//     every image before flipping that marker guarantees no reader
//     ever sees the md before its referenced images are stored.
//   - Conflict semantics: each object is uploaded via uploadOne's
//     race-safe conditional Put. Same-bytes re-upload short-circuits
//     to 200 unchanged (no S3 write); different bytes + no overwrite
//     returns 409 with both sha256 values. A single 409 on any one
//     image aborts the whole bundle — markdown is NOT written when
//     any image conflicts (the bundle is treated as atomic from the
//     contributor's POV).
//   - pdf_sha256 verification: when both the contributor's claimed
//     pdf_sha256 and the catalog's stored sha256 are present, mismatch
//     returns 400 (the contributor fetched / converted the wrong PDF
//     and would pollute the catalog with mismatched markdown). When
//     either side is empty we skip — the catalog has no reference
//     for legacy uploads, and the contributor may legitimately opt
//     out (e.g. backfilling old papers).
//
// Memory note: the zip is held in memory once (capped at
// MaxMineruZipBytes). After ExtractResult parses it into md + image
// bytes the raw zip slice is dropped, so peak memory per upload is
// ~zip_size during parsing then drops to ~sum(part_size). On a
// memory-tight VM (~1 GB class), concurrent contributors should keep
// total in-flight zip volume under ~800 MB to leave headroom for
// everything else.
func uploadMinerUHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog *papers.Store, arxivID string) error {
	ctx := re.Request.Context()
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for upload: %q. version suffix vN required.", arxivID),
		})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	expectedSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256"))
	claimedPDFSha := normaliseSha256Hex(re.Request.URL.Query().Get("pdf_sha256"))
	source := re.Request.URL.Query().Get("source")
	if len(source) > 64 {
		source = source[:64]
	}

	// Cross-check the contributor's claimed source-PDF sha256 against
	// the PDF currently stored in the catalog (read from object
	// metadata via the same helper mineru-claim uses). Mismatch ⇒
	// contributor ran MinerU on a different PDF than the catalog has
	// — refuse before we waste cycles parsing the zip and write the
	// wrong markdown. Both sides empty ⇒ skip (legacy / opt-out).
	if claimedPDFSha != "" {
		storedPDFSha := lookupStoredPDFSha256(ctx, store, canonical)
		if storedPDFSha != "" && storedPDFSha != claimedPDFSha {
			return re.JSON(http.StatusBadRequest, map[string]any{
				"detail":              "pdf_sha256 mismatch — the PDF you converted does not match the one in the catalog (wrong arxiv version, or corrupted source PDF). Re-fetch the PDF from the pdf_url returned by mineru-claim and try again.",
				"claimed_pdf_sha256":  claimedPDFSha,
				"catalog_pdf_sha256":  storedPDFSha,
			})
		}
	}

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxMineruZipBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}

	zipPart, _, err := re.Request.FormFile("mineru_zip")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "missing 'mineru_zip' multipart part: " + err.Error(),
		})
	}
	defer zipPart.Close()

	// Stage the entire zip in memory, validating the magic prefix as
	// the first 4 bytes arrive so obvious garbage (a stray .md, a PDF,
	// etc.) is rejected cheaply before paying the full read.
	zipStaged, vErr := stageInMemory(ctx, zipPart, paperassets.MaxMineruZipBytes, "mineru_zip",
		func(b []byte) *uploadError {
			if len(b) < 4 || b[0] != 'P' || b[1] != 'K' {
				return &uploadError{Status: http.StatusBadRequest, Detail: "payload is not a zip archive (missing PK signature)"}
			}
			return nil
		})
	if vErr != nil {
		return re.JSON(vErr.Status, map[string]string{"detail": vErr.Detail})
	}
	defer zipStaged.Close()
	zipSha := zipStaged.Sha256()
	zipSize := zipStaged.Size()
	if expectedSha != "" && expectedSha != zipSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":          "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256": expectedSha,
			"actual_sha256":   zipSha,
		})
	}

	// archive/zip needs the whole byte slice for random access. Open
	// the staged body once, slurp, then close — we don't need the
	// staged body again after extraction.
	zipR, err := zipStaged.Open()
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "open zip: " + err.Error()})
	}
	zipBytes, err := io.ReadAll(zipR)
	_ = zipR.Close()
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "read zip: " + err.Error()})
	}

	result, err := mineru.ExtractResult(zipBytes)
	if err != nil {
		// ExtractResult's errors wrap "open zip", "result zip did not
		// contain full.md", "result zip full.md was empty / unreadable"
		// — all client mistakes from the server's POV.
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
	}
	// Drop the raw zip bytes; from here on we only need result.Markdown
	// and result.Images.
	zipBytes = nil
	_ = zipStaged.Close()

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}

	// Images first, markdown last (see top-of-function comment for
	// rationale). Collect a per-image report for the response body and
	// for the slog completion line.
	imagesBase := paperassets.AssetKey("images", canonical)
	type imageOutcome struct {
		Key       string `json:"key"`
		Sha256    string `json:"sha256"`
		Bytes     int64  `json:"bytes"`
		Unchanged bool   `json:"unchanged"`
	}
	imageReport := make([]imageOutcome, 0, len(result.Images))
	for rel, data := range result.Images {
		// rel is e.g. "images/abc.jpg"; strip the leading "images/"
		// so we don't double the prefix under imagesBase (which
		// already contains the "images/<shard>/<stem>" path).
		name := strings.TrimPrefix(rel, "images/")
		if name == "" || strings.Contains(name, "..") {
			// Defensive: skip suspicious paths rather than fail the
			// whole upload — these would never come from a real
			// MinerU run.
			continue
		}
		imgKey := imagesBase + "/" + name
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		imgBody := newInMemoryBodyFromBytes(data)
		outcome, err := uploadOne(ctx, store, imgKey, imgBody, ct, overwrite, "image")
		imgSha := imgBody.Sha256()
		imgSize := imgBody.Size()
		_ = imgBody.Close()
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": fmt.Sprintf("upload image %s: %s", name, err.Error()),
			})
		}
		if outcome.kind == outcomeConflict {
			body := map[string]any{
				"detail":          fmt.Sprintf("image %s already exists at %s with different content; pass overwrite=true to replace", name, imgKey),
				"existing_path":   imgKey,
				"new_sha256":      imgSha,
				"existing_sha256": outcome.existingShaJSON(),
				"failed_image":    name,
			}
			return re.JSON(http.StatusConflict, body)
		}
		imageReport = append(imageReport, imageOutcome{
			Key:       imgKey,
			Sha256:    imgSha,
			Bytes:     imgSize,
			Unchanged: outcome.kind == outcomeUnchanged,
		})
	}

	mdKey := paperassets.AssetKey("markdown", canonical)
	mdBody := newInMemoryBodyFromBytes(result.Markdown)
	defer mdBody.Close()
	mdSha := mdBody.Sha256()
	mdSize := mdBody.Size()
	mdOutcome, err := uploadOne(ctx, store, mdKey, mdBody, "text/markdown; charset=utf-8", overwrite, "markdown")
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if mdOutcome.kind == outcomeConflict {
		body := map[string]any{
			"detail":        "markdown already exists at " + mdKey + " with different content; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
			"existing_path": mdKey,
			"new_sha256":    mdSha,
		}
		if mdOutcome.existingSha != "" {
			body["existing_sha256"] = mdOutcome.existingSha
		} else {
			body["existing_sha256"] = nil
			body["note"] = "existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return re.JSON(http.StatusConflict, body)
	}

	slog.Info("uploaded mineru bundle",
		"arxiv_id", canonical,
		"requester", requester,
		"source", source,
		"zip_bytes", zipSize,
		"zip_sha256", zipSha,
		"md_bytes", mdSize,
		"md_sha256", mdSha,
		"md_unchanged", mdOutcome.kind == outcomeUnchanged,
		"md_key", mdKey,
		"image_count", len(imageReport),
	)

	catalogDeferred := false
	if err := catalog.UpsertMD(ctx, canonical, mdSha, mdSize, mdOutcome.existingSha); err != nil {
		if !errors.Is(err, papers.ErrCatalogUnavailable) {
			slog.Warn("papers: UpsertMD write-through failed", "arxiv_id", canonical, "error", err)
		}
		catalogDeferred = true
	}

	resp := map[string]any{
		"arxiv_id":           canonical,
		"key":                paperassets.StorageKey(canonical),
		"markdown_path":      mdKey,
		"markdown_bytes":     mdSize,
		"markdown_sha256":    mdSha,
		"markdown_unchanged": mdOutcome.kind == outcomeUnchanged,
		"image_count":        len(imageReport),
		"images":             imageReport,
		"zip_bytes":          zipSize,
		"zip_sha256":         zipSha,
		"source":             nil,
		"uploaded_by":        nil,
		"overwritten":        overwrite,
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

	// Status code reflects "did anything new land?":
	//   - 200 OK   → every part (md + every image) was already present
	//                with matching sha256, zero writes
	//   - 201 Created → at least one new object was stored
	allUnchanged := mdOutcome.kind == outcomeUnchanged
	if allUnchanged {
		for _, img := range imageReport {
			if !img.Unchanged {
				allUnchanged = false
				break
			}
		}
	}
	if allUnchanged {
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
// and uploadMinerUHandler. It encodes the new race-safe contract:
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
