package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineruclaim"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"

	"github.com/pocketbase/pocketbase/core"
)

// RegisterPapers wires the /api/papers/* endpoints. cfg supplies the
// RAW_DIR / DATA_DIR roots; shareStore + claimStore are pre-initialized
// by main.go and shared with the shares routes.
//
// We can't use a Go ServeMux pattern with the wildcard in the middle of
// the path (e.g. /api/papers/{arxiv_id...}/upload-pdf), but the Python
// FastAPI surface put arxiv_id (which can contain slashes for old-style
// ids like "quant-ph/9508027v1") right after /api/papers/. To keep the
// CLI wire-compatible we install three catch-all routes (GET / POST /
// DELETE) and dispatch on the trailing path segment(s) inside the
// handler. Special case: GET /api/papers/needs-mineru is path-only with
// no arxiv_id, dispatched first.
func RegisterPapers(se *core.ServeEvent, cfg *config.Config, shareStore *shares.Store, claimStore *mineruclaim.Store) {
	se.Router.GET("/api/papers/{path...}", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		// /api/papers/needs-mineru (no arxiv id)
		if raw == "needs-mineru" {
			return needsMineruHandler(re, cfg, claimStore)
		}
		arxiv, action := splitPapersPath(raw)
		switch action {
		case "resources":
			if arxiv == "" {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing arxiv_id"})
			}
			return paperResourcesHandler(re, cfg, shareStore, arxiv)
		}
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no GET handler for /api/papers/%s", raw),
		})
	})

	se.Router.POST("/api/papers/{path...}", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		arxiv, action := splitPapersPath(raw)
		switch action {
		case "upload-pdf":
			return uploadPDFHandler(re, cfg, arxiv)
		case "upload-markdown":
			return uploadMarkdownHandler(re, cfg, claimStore, arxiv)
		case "mineru-claim":
			ttl, _ := strconv.Atoi(re.Request.URL.Query().Get("ttl_seconds"))
			if ttl <= 0 {
				ttl = mineruclaim.DefaultTTLSeconds
			}
			return mineruClaimHandler(re, cfg, shareStore, claimStore, arxiv, ttl)
		}
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf("no POST handler for /api/papers/%s", raw),
		})
	})

	se.Router.DELETE("/api/papers/{path...}", func(re *core.RequestEvent) error {
		raw := re.Request.PathValue("path")
		// mineru-claim DELETE: <arxiv...>/mineru-claim/<claim_id>
		arxiv, claimID, ok := splitMineruClaimRelease(raw)
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("no DELETE handler for /api/papers/%s", raw),
			})
		}
		return mineruClaimReleaseHandler(re, cfg, claimStore, arxiv, claimID)
	})
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
		// No slash at all — first segment IS the action (e.g. "needs-mineru").
		return "", raw
	}
	return raw[:idx], raw[idx+1:]
}

// splitMineruClaimRelease parses "<arxiv_id>/mineru-claim/<claim_id>"
// and returns arxiv_id, claim_id, ok. Returns ok=false when the trailing
// "mineru-claim/<claim_id>" suffix is missing.
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

// needsMineruHandler implements GET /api/papers/needs-mineru.
func needsMineruHandler(re *core.RequestEvent, cfg *config.Config, claimStore *mineruclaim.Store) error {
	limit, _ := strconv.Atoi(re.Request.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}
	includeClaimed := re.Request.URL.Query().Get("include_claimed") == "true"

	if cfg.RawDir == "" {
		return re.JSON(http.StatusOK, map[string]any{
			"papers":          []any{},
			"returned":        0,
			"total_unclaimed": 0,
			"total_claimed":   0,
			"note":            "RAW_DIR not configured",
		})
	}

	papers, unclaimed, claimedCount := enumerateNeedsMineru(cfg, claimStore, limit, includeClaimed)
	return re.JSON(http.StatusOK, map[string]any{
		"papers":          papers,
		"returned":        len(papers),
		"total_unclaimed": unclaimed,
		"total_claimed":   claimedCount,
	})
}

// ---------------------------------------------------------------------------
// Paper resources
// ---------------------------------------------------------------------------

func paperResourcesHandler(re *core.RequestEvent, cfg *config.Config, shareStore *shares.Store, arxivID string) error {
	resolved := paperassets.ResolveAssets(cfg.RawDir, arxivID)

	// Collect the share paths that actually exist on disk so we can mint
	// a single share token covering all of them when the operator hasn't
	// configured a permanent one.
	var sharePaths []string
	if resolved.PDFPath != "" {
		sharePaths = append(sharePaths, paperassets.SharePathForAsset("pdf", resolved.Key, "", resolved.PDFPath, cfg.RawDir))
	}
	if resolved.MarkdownPath != "" {
		sharePaths = append(sharePaths, paperassets.SharePathForAsset("markdown", resolved.Key, "", resolved.MarkdownPath, cfg.RawDir))
	}
	if resolved.JSONPath != "" {
		sharePaths = append(sharePaths, paperassets.SharePathForAsset("json", resolved.Key, "", resolved.JSONPath, cfg.RawDir))
	}
	if resolved.ImagesDir != "" {
		sharePaths = append(sharePaths, paperassets.SharePathForAsset("images", resolved.Key, "", resolved.ImagesDir, cfg.RawDir))
	}

	shareToken := cfg.ShareAccessToken
	shareBaseURL := ""
	if shareToken != "" {
		shareBaseURL = cfg.PublicBaseURL
	} else if len(sharePaths) > 0 && shareStore != nil {
		// Mint a fresh share for these specific paths.
		rec, err := shares.CreateRecord(shareStore, cfg, shares.CreateOptions{
			Paths: sharePaths,
			Label: "paper assets: " + resolved.ArxivID,
		})
		if err == nil && rec != nil {
			shareToken = rec.Token
		}
	}

	asset := func(kind, path string) map[string]any {
		out := map[string]any{"exists": path != ""}
		if path != "" && shareToken != "" {
			rel := paperassets.SharePathForAsset(kind, resolved.Key, "", path, cfg.RawDir)
			out["url"] = shares.BuildURL(shareToken, rel, shareBaseURL)
			if info, err := os.Stat(path); err == nil {
				out["size"] = info.Size()
			}
		}
		return out
	}

	var imageAssets []map[string]any
	if resolved.ImagesDir != "" && shareToken != "" {
		if entries, err := os.ReadDir(resolved.ImagesDir); err == nil {
			sort.SliceStable(entries, func(i, j int) bool {
				return entries[i].Name() < entries[j].Name()
			})
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				imgPath := filepath.Join(resolved.ImagesDir, e.Name())
				info, err := os.Stat(imgPath)
				if err != nil {
					continue
				}
				rel := paperassets.SharePathForAsset("images", resolved.Key, "", imgPath, cfg.RawDir)
				imageAssets = append(imageAssets, map[string]any{
					"name": e.Name(),
					"url":  shares.BuildURL(shareToken, rel, shareBaseURL),
					"size": info.Size(),
				})
			}
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
// needs-mineru enumeration
// ---------------------------------------------------------------------------

func enumerateNeedsMineru(cfg *config.Config, claimStore *mineruclaim.Store, limit int, includeClaimed bool) ([]map[string]any, int, int) {
	pdfRoot := filepath.Join(cfg.RawDir, "pdf")
	mdRoot := filepath.Join(cfg.RawDir, "markdown")
	info, err := os.Stat(pdfRoot)
	if err != nil || !info.IsDir() {
		return nil, 0, 0
	}

	pdfFiles := collectPDFs(pdfRoot)
	sort.Strings(pdfFiles)

	now := time.Now().UTC()
	var papers []map[string]any
	totalUnclaimed := 0
	totalClaimed := 0
	seen := map[string]struct{}{}

	for _, pdfPath := range pdfFiles {
		key := stemNoExt(pdfPath)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		shard := paperassets.Shard(key)
		mdCandidates := []string{filepath.Join(mdRoot, key+".md")}
		if shard != "" {
			mdCandidates = append(mdCandidates, filepath.Join(mdRoot, shard, key+".md"))
		}
		mdExists := false
		for _, c := range mdCandidates {
			if isRegularFile(c) {
				mdExists = true
				break
			}
		}
		if mdExists {
			continue
		}

		canonical := key // see Python _canonical_arxiv_from_key — identity
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
			"arxiv_id":          canonical,
			"key":               key,
			"pdf_path":          paperassets.RelativeRawPath(pdfPath, cfg.RawDir),
			"claimed":           claimed,
			"claim_expires_at":  nil,
			"claim_requester":   nil,
		}
		if claim != nil && claimed {
			paper["claim_expires_at"] = claim.ExpiresAt
			paper["claim_requester"] = claim.Requester
		}
		papers = append(papers, paper)
	}
	return papers, totalUnclaimed, totalClaimed
}

// collectPDFs returns every *.pdf under root (one level deep + at root),
// matching the Python `pdf_root.glob("*/*.pdf") + pdf_root.glob("*.pdf")`.
func collectPDFs(root string) []string {
	var out []string
	out = append(out, globOne(root, "*.pdf")...)
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, globOne(filepath.Join(root, e.Name()), "*.pdf")...)
			}
		}
	}
	return out
}

func globOne(dir, pattern string) []string {
	m, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil
	}
	return m
}

func stemNoExt(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return base[:len(base)-len(ext)]
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// ---------------------------------------------------------------------------
// MinerU claim handlers
// ---------------------------------------------------------------------------

func mineruClaimHandler(re *core.RequestEvent, cfg *config.Config, shareStore *shares.Store, claimStore *mineruclaim.Store, arxivID string, ttl int) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for claim: %q (version suffix vN required)", arxivID),
		})
	}

	// PDF must already be in RAW_DIR — refuse to claim a paper we can't serve.
	pdfPath := paperassets.AssetPath(cfg.RawDir, "pdf", canonical)
	if !isRegularFile(pdfPath) {
		// Try the loose resolver before failing.
		resolved := paperassets.ResolveAssets(cfg.RawDir, canonical)
		if resolved.PDFPath == "" {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("no PDF in RAW_DIR for %s; upload it first via /api/papers/{arxiv_id}/upload-pdf", canonical),
			})
		}
		pdfPath = resolved.PDFPath
	}

	// No work if markdown already exists.
	mdPath := paperassets.AssetPath(cfg.RawDir, "markdown", canonical)
	resolved := paperassets.ResolveAssets(cfg.RawDir, canonical)
	if isRegularFile(mdPath) || resolved.MarkdownPath != "" {
		return re.JSON(http.StatusConflict, map[string]string{
			"detail": fmt.Sprintf("markdown already exists for %s; nothing to do", canonical),
		})
	}

	// Build the PDF share URL the claimant will hand to MinerU.
	relSharePath := paperassets.SharePathForAsset("pdf", paperassets.StorageKey(canonical), "", pdfPath, cfg.RawDir)
	shareToken := cfg.ShareAccessToken
	shareBaseURL := ""
	if shareToken != "" {
		shareBaseURL = cfg.PublicBaseURL
	} else {
		rec, err := shares.CreateRecord(shareStore, cfg, shares.CreateOptions{
			Paths: []string{relSharePath},
			Label: "mineru pdf: " + canonical,
		})
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
					"message":             fmt.Sprintf("%s is already claimed", canonical),
					"claim_id":            dupErr.Existing.ClaimID,
					"claim_expires_at":    dupErr.Existing.ExpiresAt,
					"claim_requester":     dupErr.Existing.Requester,
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

func mineruClaimReleaseHandler(re *core.RequestEvent, cfg *config.Config, claimStore *mineruclaim.Store, arxivID, claimID string) error {
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

func uploadPDFHandler(re *core.RequestEvent, cfg *config.Config, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for upload: %q. Expected new-style 'YYMM.NNNNNvN' (post April 2007, e.g. '2501.00010v1') or old-style 'category/YYMMNNNvN' (pre April 2007, e.g. 'quant-ph/9508027v1'). An explicit version suffix is required.", arxivID),
		})
	}
	if cfg.RawDir == "" {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "RAW_DIR not configured"})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"

	pdfPath := paperassets.AssetPath(cfg.RawDir, "pdf", canonical)
	jsonPath := paperassets.AssetPath(cfg.RawDir, "json", canonical)

	// Multipart parse with a generous limit; the body cap is enforced
	// per-part by streamUpload.
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
	if contentType != "" && !strings_ContainsAnyCase(contentType, "pdf") && contentType != "application/octet-stream" {
		return re.JSON(http.StatusUnsupportedMediaType, map[string]string{
			"detail": fmt.Sprintf("expected application/pdf for 'pdf' part, got %q", contentType),
		})
	}

	if isRegularFile(pdfPath) && !overwrite {
		return re.JSON(http.StatusConflict, map[string]string{
			"detail": fmt.Sprintf("PDF already exists at %s; pass overwrite=true to replace",
				paperassets.RelativeRawPath(pdfPath, cfg.RawDir)),
		})
	}

	pdfBytes, err := streamUpload(pdfPart, pdfPath, paperassets.MaxPDFBytes, "pdf")
	if err != nil {
		return jsonError(re, err)
	}

	// Re-open the file head to verify the PDF magic number.
	head, _ := os.ReadFile(pdfPath)
	if len(head) < 5 || string(head[:5]) != "%PDF-" {
		_ = os.Remove(pdfPath)
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "uploaded file does not look like a PDF (missing %PDF- header)",
		})
	}

	metaBytes := int64(0)
	metadataPath := ""
	if mdPart, mdHdr, err := re.Request.FormFile("metadata"); err == nil && mdPart != nil {
		defer mdPart.Close()
		if isRegularFile(jsonPath) && !overwrite {
			return re.JSON(http.StatusConflict, map[string]string{
				"detail": fmt.Sprintf("metadata already exists at %s; pass overwrite=true to replace",
					paperassets.RelativeRawPath(jsonPath, cfg.RawDir)),
			})
		}
		_ = mdHdr // Content-Type not enforced for metadata
		metaBytes, err = streamUpload(mdPart, jsonPath, paperassets.MaxMetadataBytes, "metadata")
		if err != nil {
			return jsonError(re, err)
		}
		// Validate JSON.
		body, readErr := os.ReadFile(jsonPath)
		if readErr == nil {
			var v any
			if json.Unmarshal(body, &v) != nil {
				_ = os.Remove(jsonPath)
				return re.JSON(http.StatusBadRequest, map[string]string{
					"detail": "metadata must be valid utf-8 JSON",
				})
			}
		}
		metadataPath = paperassets.RelativeRawPath(jsonPath, cfg.RawDir)
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
		"pdf_path", pdfPath,
	)

	resp := map[string]any{
		"arxiv_id":      canonical,
		"key":           paperassets.StorageKey(canonical),
		"pdf_path":      paperassets.RelativeRawPath(pdfPath, cfg.RawDir),
		"pdf_bytes":     pdfBytes,
		"metadata_path": nil,
		"metadata_bytes": nil,
		"uploaded_by":   nil,
		"overwritten":   overwrite,
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

func uploadMarkdownHandler(re *core.RequestEvent, cfg *config.Config, claimStore *mineruclaim.Store, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for upload: %q. version suffix vN required.", arxivID),
		})
	}
	if cfg.RawDir == "" {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "RAW_DIR not configured"})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	source := re.Request.URL.Query().Get("source")
	if len(source) > 64 {
		source = source[:64]
	}

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxMarkdownBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}

	mdPath := paperassets.AssetPath(cfg.RawDir, "markdown", canonical)
	if isRegularFile(mdPath) && !overwrite {
		return re.JSON(http.StatusConflict, map[string]string{
			"detail": fmt.Sprintf("markdown already exists at %s; pass overwrite=true to replace",
				paperassets.RelativeRawPath(mdPath, cfg.RawDir)),
		})
	}

	mdPart, _, err := re.Request.FormFile("markdown")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing 'markdown' multipart part: " + err.Error()})
	}
	defer mdPart.Close()

	mdBytes, err := streamUpload(mdPart, mdPath, paperassets.MaxMarkdownBytes, "markdown")
	if err != nil {
		return jsonError(re, err)
	}

	// Validate UTF-8.
	if body, readErr := os.ReadFile(mdPath); readErr == nil {
		if !isValidUTF8(body) {
			_ = os.Remove(mdPath)
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "markdown must be valid utf-8",
			})
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
		"md_bytes", mdBytes,
		"md_path", mdPath,
	)

	// Free the claim — best-effort, errors logged but not surfaced.
	if err := claimStore.Release(canonical); err != nil {
		slog.Warn("failed to release mineru claim", "arxiv_id", canonical, "error", err)
	}

	resp := map[string]any{
		"arxiv_id":       canonical,
		"key":            paperassets.StorageKey(canonical),
		"markdown_path":  paperassets.RelativeRawPath(mdPath, cfg.RawDir),
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

// streamUpload writes src to destination via dest.part, enforces a hard
// size cap, then renames atomically. Returns bytes written.
type uploadError struct {
	Status int
	Detail string
}

func (e *uploadError) Error() string { return e.Detail }

func streamUpload(src io.Reader, destination string, maxBytes int64, label string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, err
	}
	tmp := destination + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(tmp)
	}()

	written := int64(0)
	buf := make([]byte, 1<<20)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > maxBytes {
				return 0, &uploadError{
					Status: http.StatusRequestEntityTooLarge,
					Detail: fmt.Sprintf("%s exceeds maximum upload size of %d bytes", label, maxBytes),
				}
			}
			if _, werr := out.Write(buf[:n]); werr != nil {
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
	if written == 0 {
		return 0, &uploadError{
			Status: http.StatusBadRequest,
			Detail: fmt.Sprintf("%s upload was empty", label),
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, destination); err != nil {
		return 0, err
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

// strings_ContainsAnyCase is strings.Contains but case-insensitive on
// the second argument. The Python check is "pdf in content_type.lower()".
func strings_ContainsAnyCase(haystack, needle string) bool {
	hay := []byte(haystack)
	for i := range hay {
		if hay[i] >= 'A' && hay[i] <= 'Z' {
			hay[i] = hay[i] - 'A' + 'a'
		}
	}
	return contains(string(hay), needle)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// isValidUTF8 mirrors json.Valid but for plain text. Cheap enough we
// don't bother with utf8.Valid (which is the obvious thing) just to
// keep dependencies focused.
func isValidUTF8(b []byte) bool {
	// Defer to the standard library: import added only where used.
	return validUTF8(b)
}

func validUTF8(b []byte) bool {
	for len(b) > 0 {
		_, sz := decodeRune(b)
		if sz == 0 {
			return false
		}
		b = b[sz:]
	}
	return true
}

func decodeRune(b []byte) (rune, int) {
	const replacement = 0xFFFD
	if len(b) == 0 {
		return replacement, 0
	}
	c := b[0]
	switch {
	case c < 0x80:
		return rune(c), 1
	case c < 0xC0:
		return replacement, 0
	case c < 0xE0:
		if len(b) < 2 || b[1]&0xC0 != 0x80 {
			return replacement, 0
		}
		return rune(c&0x1F)<<6 | rune(b[1]&0x3F), 2
	case c < 0xF0:
		if len(b) < 3 || b[1]&0xC0 != 0x80 || b[2]&0xC0 != 0x80 {
			return replacement, 0
		}
		return rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F), 3
	case c < 0xF8:
		if len(b) < 4 || b[1]&0xC0 != 0x80 || b[2]&0xC0 != 0x80 || b[3]&0xC0 != 0x80 {
			return replacement, 0
		}
		return rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F), 4
	default:
		return replacement, 0
	}
}

// Compile-time stub to keep "context" / unused imports honest if we
// later refactor. Today it's a no-op.
var _ = context.Background
