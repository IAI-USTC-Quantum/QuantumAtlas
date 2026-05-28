package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
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

	if !overwrite {
		if _, exists, _ := store.Stat(ctx, pdfKey); exists {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": fmt.Sprintf("PDF already exists at %s; pass overwrite=true to replace", pdfKey),
			})
		}
	}

	pdfBytes, err := stagedUpload(ctx, pdfPart, store, pdfKey, paperassets.MaxPDFBytes, "pdf", "application/pdf",
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

	metaBytes := int64(0)
	metadataPath := ""
	if mdPart, _, err := re.Request.FormFile("metadata"); err == nil && mdPart != nil {
		defer mdPart.Close()
		if !overwrite {
			if _, exists, _ := store.Stat(ctx, jsonKey); exists {
				return re.JSON(http.StatusConflict, map[string]string{
					"detail": fmt.Sprintf("metadata already exists at %s; pass overwrite=true to replace", jsonKey),
				})
			}
		}
		metaBytes, err = stagedUpload(ctx, mdPart, store, jsonKey, paperassets.MaxMetadataBytes, "metadata", "application/json",
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
		metadataPath = jsonKey
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	slog.Info("uploaded pdf",
		"arxiv_id", canonical,
		"requester", requester,
		"pdf_bytes", pdfBytes,
		"metadata_bytes", metaBytes,
		"pdf_key", pdfKey,
	)

	resp := map[string]any{
		"arxiv_id":       canonical,
		"key":            paperassets.StorageKey(canonical),
		"pdf_path":       pdfKey,
		"pdf_bytes":      pdfBytes,
		"metadata_path":  nil,
		"metadata_bytes": nil,
		"uploaded_by":    nil,
		"overwritten":    overwrite,
	}
	if metadataPath != "" {
		resp["metadata_path"] = metadataPath
		resp["metadata_bytes"] = metaBytes
	}
	if requester != "" {
		resp["uploaded_by"] = requester
	}
	re.Response.WriteHeader(http.StatusCreated)
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
	source := re.Request.URL.Query().Get("source")
	if len(source) > 64 {
		source = source[:64]
	}

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxMarkdownBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}

	mdKey := paperassets.AssetKey("markdown", canonical)
	if !overwrite {
		if _, exists, _ := store.Stat(ctx, mdKey); exists {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": fmt.Sprintf("markdown already exists at %s; pass overwrite=true to replace", mdKey),
			})
		}
	}

	mdPart, _, err := re.Request.FormFile("markdown")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing 'markdown' multipart part: " + err.Error()})
	}
	defer mdPart.Close()

	mdBytes, err := stagedUpload(ctx, mdPart, store, mdKey, paperassets.MaxMarkdownBytes, "markdown", "text/markdown; charset=utf-8",
		func(b []byte) *uploadError {
			if !utf8.Valid(b) {
				return &uploadError{Status: http.StatusBadRequest, Detail: "markdown must be valid utf-8"}
			}
			return nil
		})
	if err != nil {
		return jsonError(re, err)
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	slog.Info("uploaded markdown",
		"arxiv_id", canonical,
		"requester", requester,
		"source", source,
		"md_bytes", mdBytes,
		"md_key", mdKey,
	)

	if err := claimStore.Release(canonical); err != nil {
		slog.Warn("failed to release mineru claim", "arxiv_id", canonical, "error", err)
	}

	resp := map[string]any{
		"arxiv_id":       canonical,
		"key":            paperassets.StorageKey(canonical),
		"markdown_path":  mdKey,
		"markdown_bytes": mdBytes,
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
	re.Response.WriteHeader(http.StatusCreated)
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

// stagedUpload streams src into a local temp file with a hard size cap,
// runs validate() on the staged bytes, and then uploads from the temp
// file to dst at dstKey. Returns the byte count actually written.
//
// Two passes (disk-stage + upload) buy us:
//   - synchronous content validation BEFORE anything hits the store
//     (S3 PutObject is atomic but expensive to roll back; failing in
//     the disk stage means zero remote state changes);
//   - exact size known at PutObject time, avoiding minio-go's
//     multipart upload path for small files.
//
// The validate callback receives the entire staged content (bounded by
// maxBytes) so it can inspect headers, parse JSON, check encoding —
// whatever the route needs. Returning a non-nil *uploadError aborts the
// upload and surfaces the status code to the client.
//
// For payloads larger than ~10 MiB the all-bytes validate call eats RAM
// proportional to the upload size; the current callers cap PDF at
// 100 MiB which is acceptable on RackNerd's 1.4 GiB host. If a future
// route uploads multi-GiB files, switch to incremental validation.
func stagedUpload(
	ctx context.Context,
	src io.Reader,
	dst objstore.Store,
	dstKey string,
	maxBytes int64,
	label string,
	contentType string,
	validate func([]byte) *uploadError,
) (int64, error) {
	stage, err := os.CreateTemp("", "qatlas-upload-*.bin")
	if err != nil {
		return 0, err
	}
	stagePath := stage.Name()
	// Ensure cleanup even on early returns.
	defer func() {
		_ = stage.Close()
		_ = os.Remove(stagePath)
	}()

	written, err := copyWithCap(stage, src, maxBytes, label)
	if err != nil {
		return 0, err
	}
	if written == 0 {
		return 0, &uploadError{Status: http.StatusBadRequest, Detail: fmt.Sprintf("%s upload was empty", label)}
	}

	// Validate. Need to re-read the staged bytes from the start; on
	// staged size > a few MiB this is a tolerable disk hit. For tiny
	// metadata uploads (JSON, markdown) it's negligible.
	if _, err := stage.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	body, err := io.ReadAll(stage)
	if err != nil {
		return 0, err
	}
	if vErr := validate(body); vErr != nil {
		return 0, vErr
	}

	// Upload. Rewind and PUT. We pass written (exact size) so the S3
	// backend can use single-PUT and avoid multipart overhead.
	if _, err := stage.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	// Use bytes.Reader from the already-loaded body to avoid a third
	// disk read. The body slice is bounded by maxBytes so this stays
	// within the documented memory envelope.
	if _, err := dst.Put(ctx, dstKey, bytes.NewReader(body), int64(len(body)), contentType); err != nil {
		return 0, err
	}
	return written, nil
}

// copyWithCap streams from src to dst, aborting once cap bytes have
// been read. Returns the byte count actually written.
func copyWithCap(dst io.Writer, src io.Reader, cap int64, label string) (int64, error) {
	buf := make([]byte, 1<<20)
	written := int64(0)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > cap {
				return 0, &uploadError{
					Status: http.StatusRequestEntityTooLarge,
					Detail: fmt.Sprintf("%s exceeds maximum upload size of %d bytes", label, cap),
				}
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return 0, werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, rerr
		}
	}
	return written, nil
}

func jsonError(re *core.RequestEvent, err error) error {
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
