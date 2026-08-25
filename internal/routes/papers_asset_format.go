package routes

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/pocketbase/pocketbase/core"
)

// assetLinkTTL is how long a RustFS direct-link presign stays valid. 24h
// matches the MinerU-lease PDF URL — a fetch handle, not a distribution
// channel.
const assetLinkTTL = 24 * time.Hour

// assetFormat reads the ?format= query override (ADR 0011): "link" asks
// for a RustFS direct link, "bytes" (alias "stream") asks for a byte
// stream, "" means "use the per-kind default". The bytes-vs-link choice is
// a transport preference, not a compliance decision — compliance is the
// QATLAS_PAPER_ACCESS_ENABLED gate that decides whether the route is
// served at all.
func assetFormat(re *core.RequestEvent) string {
	switch strings.ToLower(strings.TrimSpace(re.Request.URL.Query().Get("format"))) {
	case "link":
		return "link"
	case "bytes", "stream":
		return "bytes"
	default:
		return ""
	}
}

// serveReadyAsset serves an asset that is confirmed present, either as a
// RustFS direct link or as a byte stream (ADR 0011). kind is "pdf" |
// "markdown" | "json"; defaultFormat is the per-kind default ("link" for
// pdf, "bytes" for markdown/json) applied when the caller does not force
// ?format=. A link request degrades to a byte stream when the backend
// cannot presign (LocalStore dev).
func serveReadyAsset(re *core.RequestEvent, store objstore.Store, kind, canonical, defaultFormat string) error {
	ctx := re.Request.Context()
	key, _, exists, err := paperassets.LocateAssetByID(ctx, store, kind, canonical)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "locate " + kind + ": " + err.Error(),
		})
	}
	if !exists {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail":   kind + " not found",
			"arxiv_id": canonical,
		})
	}

	format := assetFormat(re)
	if format == "" {
		format = defaultFormat
	}
	if format == "link" {
		if url, ok, perr := store.PresignGet(ctx, key, assetLinkTTL); perr == nil && ok && url != "" {
			return re.JSON(http.StatusOK, map[string]any{
				"arxiv_id":    canonical,
				"format":      "link",
				kind + "_url": url,
				"expires_in":  int(assetLinkTTL.Seconds()),
			})
		}
		// Backend can't presign (LocalStore) — fall through to bytes so the
		// caller still gets the content.
	}
	return streamAssetBytes(re, store, kind, canonical, key)
}

// streamAssetBytes copies a located asset's bytes to the response with the
// content type for its kind.
func streamAssetBytes(re *core.RequestEvent, store objstore.Store, kind, canonical, key string) error {
	ctx := re.Request.Context()
	rc, info, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail":   kind + " not found",
				"arxiv_id": canonical,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch " + kind + ": " + err.Error(),
		})
	}
	defer rc.Close()

	switch kind {
	case "pdf":
		re.Response.Header().Set("Content-Type", "application/pdf")
		re.Response.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.pdf"`, sanitizeFilename(canonical)))
	case "markdown":
		re.Response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	case "json":
		re.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
	case "images":
		re.Response.Header().Set("Content-Type", "application/zip")
		re.Response.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-images.zip"`, sanitizeFilename(canonical)))
	}
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(re.Response, rc); err != nil {
		slog.Warn("asset: stream copy failed", "kind", kind, "arxiv_id", canonical, "error", err)
	}
	return nil
}
