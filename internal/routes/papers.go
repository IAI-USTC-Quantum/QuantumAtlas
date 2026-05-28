package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineruclaim"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
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
	enforcer *casbin.Enforcer,
) {
	se.Router.GET("/api/papers/{path...}", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		if raw == "needs-mineru" {
			return needsMineruHandler(re, rawStore, claimStore)
		}
		arxiv, action := splitPapersPath(raw)
		switch action {
		case "resources":
			if arxiv == "" {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing arxiv_id"})
			}
			return paperResourcesHandler(re, cfg, rawStore, shareStore, arxiv)
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

// ---------------------------------------------------------------------------
// needs-mineru
// ---------------------------------------------------------------------------

func needsMineruHandler(re *core.RequestEvent, store objstore.Store, claimStore *mineruclaim.Store) error {
	limit, _ := strconv.Atoi(re.Request.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}
	includeClaimed := re.Request.URL.Query().Get("include_claimed") == "true"

	papers, unclaimed, claimedCount := enumerateNeedsMineru(re.Request.Context(), store, claimStore, limit, includeClaimed)
	return re.JSON(http.StatusOK, map[string]any{
		"papers":          papers,
		"returned":        len(papers),
		"total_unclaimed": unclaimed,
		"total_claimed":   claimedCount,
	})
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

	// Stage PDF (memory + sha256). No store I/O yet, so the entire
	// upload can be rolled back by simply returning early.
	pdfBody, pdfSha, err := stageBody(ctx, pdfPart, paperassets.MaxPDFBytes, "pdf",
		func(b []byte) *uploadError {
			if len(b) < 5 || string(b[:5]) != "%PDF-" {
				return &uploadError{Status: http.StatusBadRequest,
					Detail: "uploaded file does not look like a PDF (missing %PDF- header)"}
			}
			return nil
		})
	if err != nil {
		return jsonError(re, err)
	}
	if expectedPdfSha != "" && expectedPdfSha != pdfSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":            "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256":   expectedPdfSha,
			"actual_sha256":     pdfSha,
		})
	}

	// Stage metadata JSON if present, BEFORE any store writes — so a
	// metadata-side conflict doesn't leave a half-uploaded PDF.
	var metaBody []byte
	var metaSha string
	hasMetadata := false
	if mdPart, _, err := re.Request.FormFile("metadata"); err == nil && mdPart != nil {
		defer mdPart.Close()
		hasMetadata = true
		metaBody, metaSha, err = stageBody(ctx, mdPart, paperassets.MaxMetadataBytes, "metadata",
			func(b []byte) *uploadError {
				var v any
				if json.Unmarshal(b, &v) != nil {
					return &uploadError{Status: http.StatusBadRequest, Detail: "metadata must be valid utf-8 JSON"}
				}
				return nil
			})
		if err != nil {
			return jsonError(re, err)
		}
		if expectedMetaSha != "" && expectedMetaSha != metaSha {
			return re.JSON(http.StatusBadRequest, map[string]any{
				"detail":                     "expected_metadata_sha256 mismatch — metadata may be corrupt in transit",
				"expected_metadata_sha256":   expectedMetaSha,
				"actual_metadata_sha256":     metaSha,
			})
		}
	}

	// Stat existing for both keys + decide each independently.
	pdfDecision, err := decideUpload(ctx, store, pdfKey, pdfSha, overwrite, "PDF")
	if err != nil {
		return jsonError(re, err)
	}
	var metaDecision uploadDecision
	if hasMetadata {
		metaDecision, err = decideUpload(ctx, store, jsonKey, metaSha, overwrite, "metadata")
		if err != nil {
			return jsonError(re, err)
		}
	}

	// All conflict checks passed — now we can write.
	if !pdfDecision.unchanged {
		if _, err := store.PutWithMeta(ctx, pdfKey, bytes.NewReader(pdfBody),
			int64(len(pdfBody)), "application/pdf",
			map[string]string{"sha256": pdfSha}); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "put pdf: " + err.Error()})
		}
	}
	metadataPath := ""
	if hasMetadata {
		metadataPath = jsonKey
		if !metaDecision.unchanged {
			if _, err := store.PutWithMeta(ctx, jsonKey, bytes.NewReader(metaBody),
				int64(len(metaBody)), "application/json",
				map[string]string{"sha256": metaSha}); err != nil {
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "put metadata: " + err.Error()})
			}
		}
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	overallUnchanged := pdfDecision.unchanged && (!hasMetadata || metaDecision.unchanged)
	slog.Info("uploaded pdf",
		"arxiv_id", canonical,
		"requester", requester,
		"pdf_bytes", len(pdfBody),
		"pdf_sha256", pdfSha,
		"pdf_unchanged", pdfDecision.unchanged,
		"metadata_bytes", len(metaBody),
		"metadata_sha256", metaSha,
		"metadata_unchanged", hasMetadata && metaDecision.unchanged,
		"pdf_key", pdfKey,
	)

	resp := map[string]any{
		"arxiv_id":         canonical,
		"key":              paperassets.StorageKey(canonical),
		"pdf_path":         pdfKey,
		"pdf_bytes":        int64(len(pdfBody)),
		"pdf_sha256":       pdfSha,
		"pdf_unchanged":    pdfDecision.unchanged,
		"metadata_path":    nil,
		"metadata_bytes":   nil,
		"metadata_sha256":  nil,
		"uploaded_by":      nil,
		"overwritten":      overwrite,
		"unchanged":        overallUnchanged,
	}
	if metadataPath != "" {
		resp["metadata_path"] = metadataPath
		resp["metadata_bytes"] = int64(len(metaBody))
		resp["metadata_sha256"] = metaSha
		resp["metadata_unchanged"] = metaDecision.unchanged
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

	mdBody, mdSha, err := stageBody(ctx, mdPart, paperassets.MaxMarkdownBytes, "markdown",
		func(b []byte) *uploadError {
			if !utf8.Valid(b) {
				return &uploadError{Status: http.StatusBadRequest, Detail: "markdown must be valid utf-8"}
			}
			return nil
		})
	if err != nil {
		return jsonError(re, err)
	}
	if expectedSha != "" && expectedSha != mdSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":            "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256":   expectedSha,
			"actual_sha256":     mdSha,
		})
	}

	decision, err := decideUpload(ctx, store, mdKey, mdSha, overwrite, "markdown")
	if err != nil {
		return jsonError(re, err)
	}

	if !decision.unchanged {
		if _, err := store.PutWithMeta(ctx, mdKey, bytes.NewReader(mdBody),
			int64(len(mdBody)), "text/markdown; charset=utf-8",
			map[string]string{"sha256": mdSha}); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "put markdown: " + err.Error()})
		}
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	slog.Info("uploaded markdown",
		"arxiv_id", canonical,
		"requester", requester,
		"source", source,
		"md_bytes", len(mdBody),
		"md_sha256", mdSha,
		"md_unchanged", decision.unchanged,
		"md_key", mdKey,
	)

	if err := claimStore.Release(canonical); err != nil {
		slog.Warn("failed to release mineru claim", "arxiv_id", canonical, "error", err)
	}

	resp := map[string]any{
		"arxiv_id":       canonical,
		"key":            paperassets.StorageKey(canonical),
		"markdown_path":  mdKey,
		"markdown_bytes": int64(len(mdBody)),
		"sha256":         mdSha,
		"unchanged":      decision.unchanged,
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
	if decision.unchanged {
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

// uploadDecision is the result of an idempotency check against the
// store: should the handler skip writing this object?
type uploadDecision struct {
	unchanged      bool   // existing object has same sha256 → no-op
	existingSha256 string // metadata sha256 from existing object; "" when missing or LocalStore
}

// decideUpload runs the content-aware idempotency check used by both
// upload-pdf and upload-markdown:
//
//   - object absent → write proceeds (unchanged=false)
//   - object present + metadata sha256 matches → skip write (unchanged=true)
//     This catches "same client retried the same upload" — a normal,
//     benign case that previously returned 409.
//   - object present + sha256 differs (or metadata missing) + !overwrite
//     → 409 with both hashes in the response so the caller can decide
//     whether to overwrite or stop.
//   - object present + overwrite → write proceeds; under bucket
//     versioning the prior object becomes a noncurrent version
//     (recoverable via ListObjectVersions; ops only).
//
// When the existing object has no sha256 metadata at all (legacy
// objects uploaded before this change, or LocalStore which never stores
// metadata), we treat the content as "unknown" — overwrite must be
// explicit. We deliberately do NOT fall back to a Get + hash because:
// it costs a download for every conflict check, and for legacy objects
// we can't tell whether the bytes really match without it anyway. The
// safer default is "force the caller to confirm with overwrite=true".
//
// label is the human-readable kind ("PDF" / "metadata" / "markdown")
// used only in the conflict error message.
func decideUpload(ctx context.Context, store objstore.Store, key, newSha string, overwrite bool, label string) (uploadDecision, error) {
	info, exists, err := store.Stat(ctx, key)
	if err != nil {
		return uploadDecision{}, &uploadError{Status: http.StatusInternalServerError,
			Detail: fmt.Sprintf("stat %s: %s", key, err.Error())}
	}
	if !exists {
		return uploadDecision{}, nil
	}
	existingSha := ""
	if info.Metadata != nil {
		existingSha = info.Metadata["sha256"]
	}
	if existingSha != "" && existingSha == newSha {
		return uploadDecision{unchanged: true, existingSha256: existingSha}, nil
	}
	if !overwrite {
		detail := map[string]any{
			"detail":        fmt.Sprintf("%s already exists at %s with different content; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)", label, key),
			"existing_path": key,
			"new_sha256":    newSha,
		}
		if existingSha != "" {
			detail["existing_sha256"] = existingSha
		} else {
			detail["existing_sha256"] = nil
			detail["note"] = "existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return uploadDecision{}, &uploadConflictError{
			Status: http.StatusConflict,
			Body:   detail,
		}
	}
	return uploadDecision{existingSha256: existingSha}, nil
}

// uploadConflictError is uploadError's richer cousin — carries a
// structured JSON body so we can surface both sha256 hashes on a 409.
type uploadConflictError struct {
	Status int
	Body   map[string]any
}

func (e *uploadConflictError) Error() string {
	if s, ok := e.Body["detail"].(string); ok {
		return s
	}
	return "upload conflict"
}

// stageBody reads up to maxBytes from src, validates the bytes, and
// returns (body, sha256_hex). Computes sha256 inline via io.MultiWriter
// so we don't re-walk the payload. Empty uploads are rejected.
//
// We hold the entire body in memory (bounded by maxBytes). For the
// current caps (PDF 100 MiB, markdown 25 MiB, metadata 2 MiB) this is
// safe on every supported deploy; the old stagedUpload code path
// already eats the same memory (it io.ReadAll'd the staged temp file).
// If a future route needs multi-GiB files, swap in an io.TeeReader
// directly into the store PUT, dropping the materialised body.
func stageBody(ctx context.Context, src io.Reader, maxBytes int64, label string, validate func([]byte) *uploadError) ([]byte, string, error) {
	// LimitReader to maxBytes+1 so we can distinguish "exactly at cap"
	// from "over cap".
	hash := sha256.New()
	var buf bytes.Buffer
	n, err := io.Copy(io.MultiWriter(&buf, hash), io.LimitReader(src, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if n > maxBytes {
		return nil, "", &uploadError{
			Status: http.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("%s upload exceeds limit of %d bytes", label, maxBytes),
		}
	}
	if n == 0 {
		return nil, "", &uploadError{Status: http.StatusBadRequest, Detail: fmt.Sprintf("%s upload was empty", label)}
	}
	body := buf.Bytes()
	if vErr := validate(body); vErr != nil {
		return nil, "", vErr
	}
	return body, hex.EncodeToString(hash.Sum(nil)), nil
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

func jsonError(re *core.RequestEvent, err error) error {
	var uce *uploadConflictError
	if errors.As(err, &uce) {
		return re.JSON(uce.Status, uce.Body)
	}
	var ue *uploadError
	if errors.As(err, &ue) {
		return re.JSON(ue.Status, map[string]string{"detail": ue.Detail})
	}
	return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
}

func jsonBody(re *core.RequestEvent, payload any) error {
	re.Response.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(re.Response)
	return enc.Encode(payload)
}
