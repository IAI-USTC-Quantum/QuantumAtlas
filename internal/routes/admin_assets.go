package routes

// admin_assets.go: admin asset browser endpoints — list, preview,
// download, presign, batch, and search. All adminGuard-ed (admin
// allowlist required). These are independent of paper_access.enabled:
// the admin surface is an ops channel, not a user redistribution lane.
//
//	GET /api/admin/assets/{paper_id}
//	    → asset listing (object keys, sizes, sha256s, content types)
//	GET /api/admin/assets/{paper_id}/{kind}
//	    → single asset detail (optional ?presign=true&ttl=1h)
//	GET /api/admin/assets/{paper_id}/{kind}/download
//	    → proxy stream (Content-Disposition: attachment)
//	GET /api/admin/assets/{paper_id}/{kind}/inline
//	    → proxy stream (Content-Disposition: inline; for browser preview)
//	GET /api/admin/assets/{paper_id}/{kind}/url
//	    → presigned URL JSON (browser-direct S3 access)
//	GET /api/admin/assets/batch?paper_ids=a,b,c
//	    → batch asset listing (max 50)
//	GET /api/admin/assets/batch/download?paper_ids=a,b,c&kind=pdf
//	    → ZIP archive (streaming; max 20 papers)
//	GET /api/admin/assets/search?q=...&kind=pdf&limit=50
//	    → find papers with assets by title/DOI/arXiv ID

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// assetKindTTL bounds for presigned URL TTL.
const (
	assetPresignMinTTL = time.Minute
	assetPresignMaxTTL = 24 * time.Hour
	assetPresignDefTTL = time.Hour
	assetBatchMax      = 50
	assetBatchZipMax   = 20
)

// adminAssetEntry is one asset in the listing.
type adminAssetEntry struct {
	Kind             string `json:"kind"`
	ObjectKey        string `json:"object_key"`
	Size             int64  `json:"size"`
	Sha256           string `json:"sha256,omitempty"`
	ContentType      string `json:"content_type,omitempty"`
	PresignedURL     string `json:"presigned_url,omitempty"`
	PresignSupported bool   `json:"presign_supported"`
}

// adminAssetListResponse is the wire shape of the asset listing.
type adminAssetListResponse struct {
	PaperID string            `json:"paper_id"`
	Title   string            `json:"title,omitempty"`
	ArxivID string            `json:"arxiv_id,omitempty"`
	DOI     string            `json:"doi,omitempty"`
	Status  string            `json:"status,omitempty"`
	Assets  []adminAssetEntry `json:"assets"`
}

// RegisterAdminAssets wires the admin asset browser surface.
func RegisterAdminAssets(se *core.ServeEvent, cfg *config.Config, rawStore objstore.Store, catalog *registry.Store) {
	se.Router.GET("/api/admin/assets/{paper_id}", adminGuard(cfg, adminAssetListHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/{paper_id}/{kind}", adminGuard(cfg, adminAssetDetailHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/{paper_id}/{kind}/download", adminGuard(cfg, adminAssetDownloadHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/{paper_id}/{kind}/inline", adminGuard(cfg, adminAssetInlineHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/{paper_id}/{kind}/url", adminGuard(cfg, adminAssetURLHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/batch", adminGuard(cfg, adminAssetBatchHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/batch/download", adminGuard(cfg, adminAssetBatchDownloadHandler(rawStore, catalog)))
	se.Router.GET("/api/admin/assets/search", adminGuard(cfg, adminAssetSearchHandler(catalog)))
}

// buildAssetEntries collects asset entries for a paper detail.
func buildAssetEntries(store objstore.Store, detail *registry.PaperDetail, presign bool, ttl time.Duration) []adminAssetEntry {
	var entries []adminAssetEntry
	addAsset := func(kind, path string, size int64, sha string) {
		if path == "" {
			return
		}
		e := adminAssetEntry{
			Kind:      kind,
			ObjectKey: path,
			Size:      size,
			Sha256:    sha,
		}
		switch kind {
		case "pdf":
			e.ContentType = "application/pdf"
		case "markdown":
			e.ContentType = "text/markdown; charset=utf-8"
		}
		if presign {
			fullKey := kind + "/" + path
			if u, ok, err := store.PresignGet(context.Background(), fullKey, ttl); err == nil && ok && u != "" {
				e.PresignedURL = u
				e.PresignSupported = true
			}
		}
		entries = append(entries, e)
	}
	for _, a := range detail.Assets {
		addAsset("pdf", a.PDFPath, a.PDFSize, a.PDFSha256)
		addAsset("markdown", a.MinerUMDPath, 0, "")
	}
	return entries
}

// getPaperDetail fetches paper + assets, 404 if not found.
func getPaperDetail(re *core.RequestEvent, catalog *registry.Store) (*registry.PaperDetail, error) {
	id := re.Request.PathValue("paper_id")
	if id == "" {
		return nil, re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing paper_id"})
	}
	detail, found, err := catalog.GetWithAssets(re.Request.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return nil, re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "registry unavailable"})
		}
		return nil, re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if !found {
		return nil, re.JSON(http.StatusNotFound, map[string]string{"detail": "no such paper: " + id})
	}
	return detail, nil
}

func adminAssetListHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		detail, err := getPaperDetail(re, catalog)
		if err != nil {
			return err
		}
		p := detail.Paper
		return re.JSON(http.StatusOK, adminAssetListResponse{
			PaperID: p.PaperID,
			Title:   p.Title,
			ArxivID: p.ArxivID,
			DOI:     p.DOI,
			Status:  p.Status,
			Assets:  buildAssetEntries(store, detail, false, 0),
		})
	}
}

func adminAssetDetailHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		detail, err := getPaperDetail(re, catalog)
		if err != nil {
			return err
		}
		presign := re.Request.URL.Query().Get("presign") == "true"
		ttl := parseTTL(re.Request.URL.Query().Get("ttl"), assetPresignDefTTL)
		p := detail.Paper
		return re.JSON(http.StatusOK, adminAssetListResponse{
			PaperID: p.PaperID,
			Title:   p.Title,
			ArxivID: p.ArxivID,
			DOI:     p.DOI,
			Status:  p.Status,
			Assets:  buildAssetEntries(store, detail, presign, ttl),
		})
	}
}

// resolveAssetKey finds the full S3 key for a paper + kind.
func resolveAssetKey(re *core.RequestEvent, store objstore.Store, detail *registry.PaperDetail) (kind, fullKey string, err error) {
	kind = re.Request.PathValue("kind")
	if kind == "" {
		return "", "", fmt.Errorf("missing kind")
	}
	var relPath string
	var size int64
	for _, a := range detail.Assets {
		switch kind {
		case "pdf":
			if a.PDFPath != "" {
				relPath, size = a.PDFPath, a.PDFSize
			}
		case "markdown", "md":
			if a.MinerUMDPath != "" {
				relPath, size = a.MinerUMDPath, 0
				kind = "markdown"
			}
		}
		if relPath != "" {
			break
		}
	}
	if relPath == "" {
		return "", "", fmt.Errorf("no %s asset for %s", kind, detail.Paper.PaperID)
	}
	_ = size
	return kind, kind + "/" + relPath, nil
}

func adminAssetDownloadHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		detail, err := getPaperDetail(re, catalog)
		if err != nil {
			return err
		}
		kind, key, kerr := resolveAssetKey(re, store, detail)
		if kerr != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": kerr.Error()})
		}
		ext := "pdf"
		if kind == "markdown" {
			ext = "md"
		}
		re.Response.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s.%s"`, detail.Paper.PaperID, ext))
		return streamAsset(re, store, key, kind)
	}
}

func adminAssetInlineHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		detail, err := getPaperDetail(re, catalog)
		if err != nil {
			return err
		}
		_, key, kerr := resolveAssetKey(re, store, detail)
		if kerr != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": kerr.Error()})
		}
		re.Response.Header().Set("Content-Disposition", "inline")
		return streamAsset(re, store, key, re.Request.PathValue("kind"))
	}
}

func adminAssetURLHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		detail, err := getPaperDetail(re, catalog)
		if err != nil {
			return err
		}
		kind, key, kerr := resolveAssetKey(re, store, detail)
		if kerr != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": kerr.Error()})
		}
		ttl := parseTTL(re.Request.URL.Query().Get("ttl"), assetPresignDefTTL)
		u, ok, perr := store.PresignGet(re.Request.Context(), key, ttl)
		if perr != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "presign: " + perr.Error()})
		}
		if !ok {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "presign not supported by the current object-store backend (LocalStore); use /download for proxy streaming",
			})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"kind":       kind,
			"url":        u,
			"expires_at": time.Now().Add(ttl).UTC().Format(time.RFC3339),
			"object_key": key,
		})
	}
}

func adminAssetBatchHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		ids := strings.Split(re.Request.URL.Query().Get("paper_ids"), ",")
		if len(ids) == 0 || len(ids) > assetBatchMax {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": fmt.Sprintf("paper_ids must be 1..%d comma-separated", assetBatchMax),
			})
		}
		out := make([]adminAssetListResponse, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			detail, found, err := catalog.GetWithAssets(re.Request.Context(), id)
			if err != nil || !found {
				continue
			}
			p := detail.Paper
			out = append(out, adminAssetListResponse{
				PaperID: p.PaperID,
				Title:   p.Title,
				ArxivID: p.ArxivID,
				DOI:     p.DOI,
				Status:  p.Status,
				Assets:  buildAssetEntries(store, detail, false, 0),
			})
		}
		return re.JSON(http.StatusOK, map[string]any{"papers": out})
	}
}

func adminAssetBatchDownloadHandler(store objstore.Store, catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		ids := strings.Split(re.Request.URL.Query().Get("paper_ids"), ",")
		if len(ids) == 0 || len(ids) > assetBatchZipMax {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": fmt.Sprintf("paper_ids must be 1..%d comma-separated", assetBatchZipMax),
			})
		}
		kindParam := re.Request.URL.Query().Get("kind")
		if kindParam == "" {
			kindParam = "pdf"
		}

		re.Response.Header().Set("Content-Type", "application/zip")
		re.Response.Header().Set("Content-Disposition", `attachment; filename="qatlas-assets.zip"`)
		re.Response.WriteHeader(http.StatusOK)

		zw := zip.NewWriter(re.Response)
		defer zw.Close()
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			detail, found, err := catalog.GetWithAssets(re.Request.Context(), id)
			if err != nil || !found {
				continue
			}
			kind, key, kerr := resolveAssetKey(re, store, detail)
			if kerr != nil {
				continue
			}
			if kindParam != "" && kind != kindParam && !(kindParam == "md" && kind == "markdown") {
				continue
			}
			rdr, _, Gerr := store.Get(re.Request.Context(), key)
			if Gerr != nil {
				continue
			}
			ext := "pdf"
			if kind == "markdown" {
				ext = "md"
			}
			name := fmt.Sprintf("%s.%s", id, ext)
			w, werr := zw.Create(name)
			if werr != nil {
				rdr.Close()
				continue
			}
			_, _ = io.Copy(w, rdr)
			rdr.Close()
		}
		return nil
	}
}

func adminAssetSearchHandler(catalog *registry.Store) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		q := strings.TrimSpace(re.Request.URL.Query().Get("q"))
		if q == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "q is required"})
		}
		limit := 50
		if raw := re.Request.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		papers, err := catalog.SearchPapersWithAssets(re.Request.Context(), q, limit)
		if err != nil {
			if errors.Is(err, registry.ErrCatalogUnavailable) {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "registry unavailable"})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		out := make([]map[string]any, 0, len(papers))
		for _, p := range papers {
			out = append(out, map[string]any{
				"paper_id": p.PaperID,
				"arxiv_id": p.ArxivID,
				"doi":      p.DOI,
				"title":    p.Title,
				"status":   p.Status,
			})
		}
		return re.JSON(http.StatusOK, map[string]any{"papers": out})
	}
}

// streamAsset reads bytes from the object store and writes them to the
// response with the correct content type.
func streamAsset(re *core.RequestEvent, store objstore.Store, key, kind string) error {
	rdr, info, err := store.Get(re.Request.Context(), key)
	if err != nil {
		if objstore.IsNotFound(err) {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "object not found: " + key})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	defer rdr.Close()

	ct := "application/octet-stream"
	switch kind {
	case "pdf":
		ct = "application/pdf"
	case "markdown", "md":
		ct = "text/markdown; charset=utf-8"
	}
	re.Response.Header().Set("Content-Type", ct)
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.WriteHeader(http.StatusOK)
	_, _ = io.Copy(re.Response, rdr)
	return nil
}

// parseTTL parses a duration string with a default and clamps to range.
func parseTTL(raw string, def time.Duration) time.Duration {
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < assetPresignMinTTL {
		return def
	}
	if d > assetPresignMaxTTL {
		return assetPresignMaxTTL
	}
	return d
}
