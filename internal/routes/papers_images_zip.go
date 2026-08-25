package routes

// papers_images_zip.go: GET /api/papers/{id_or_doi}/images/zip — the
// explicit images-bundle download (plan §B). Images are a byproduct of
// the MinerU conversion: one zip per asset under the "images" key
// layout (see papers_images.go for the zip/dir/DOI key layouts). There
// is deliberately no LRO here — when no zip is stored the answer is a
// plain 404 telling the caller to fetch /markdown first (that endpoint
// owns the fetch+convert long-running operation).
//
// Transport follows the ADR 0011 ?format= mechanism: default (and
// ?format=bytes) streams application/zip; ?format=link returns a JSON
// {images_url} RustFS direct link (falling back to bytes on backends
// that cannot presign). An unrecognised ?format= value is a 400 —
// unlike serveReadyAsset this endpoint has no per-kind default
// ambiguity to silently absorb.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/pocketbase/pocketbase/core"
)

// imagesZipHandler serves the arxiv-form images zip, dispatched from
// the /api/papers/{path...} catch-all after the "/images/zip" action
// peel. Dual-read via paperassets.LocateAssetByID covers both the
// canonical and the legacy bare-stem layouts.
func imagesZipHandler(re *core.RequestEvent, store objstore.Store, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for images/zip: %q (version suffix vN required)", arxivID),
		})
	}
	ctx := re.Request.Context()
	key, _, exists, err := paperassets.LocateAssetByID(ctx, store, "images", canonical)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "locate images: " + err.Error(),
		})
	}
	if !exists {
		return imagesZipNotFound(re, "arxiv_id", canonical)
	}
	return serveImagesZip(re, store, key, "arxiv_id", canonical)
}

// imagesZipByDOIHandler serves the DOI-form images zip from the
// "images/doi/<reg>/<suffix>.zip" namespace. Same dispatch rationale
// as getMarkdownByDOIHandler.
func imagesZipByDOIHandler(re *core.RequestEvent, store objstore.Store, rawDOI string) error {
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for images/zip: %q", rawDOI),
		})
	}
	ctx := re.Request.Context()
	key := paperassets.DOIAssetKey("images", doi)
	if key == "" {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "could not compute images key for DOI",
		})
	}
	if _, exists, err := store.Stat(ctx, key); err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "stat images: " + err.Error(),
		})
	} else if !exists {
		return imagesZipNotFound(re, "doi", doi)
	}
	return serveImagesZip(re, store, key, "doi", doi)
}

// imagesZipNotFound is the shared 404 for both id forms. The detail
// points at the markdown endpoint because the MinerU conversion it
// triggers is what produces the images zip.
func imagesZipNotFound(re *core.RequestEvent, idField, id string) error {
	return re.JSON(http.StatusNotFound, map[string]string{
		"detail": fmt.Sprintf("no images available for %s; GET /api/papers/%s/markdown first to trigger the conversion that produces them", id, id),
		idField:  id,
	})
}

// serveImagesZip emits a located images zip under the ADR 0011
// ?format= transport: bytes by default, a presigned-link JSON for
// ?format=link (degrading to bytes when the backend cannot presign),
// 400 for anything else.
func serveImagesZip(re *core.RequestEvent, store objstore.Store, key, idField, id string) error {
	rawFormat := re.Request.URL.Query().Get("format")
	switch strings.ToLower(strings.TrimSpace(rawFormat)) {
	case "", "bytes", "stream":
		// stream below
	case "link":
		if url, ok, err := store.PresignGet(re.Request.Context(), key, assetLinkTTL); err == nil && ok && url != "" {
			return re.JSON(http.StatusOK, map[string]any{
				idField:      id,
				"format":     "link",
				"images_url": url,
				"expires_in": int(assetLinkTTL.Seconds()),
			})
		}
		// Backend can't presign (LocalStore) — fall through to bytes so
		// the caller still gets the content.
	default:
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid format %q for images/zip; expected link|bytes", rawFormat),
		})
	}
	return streamAssetBytes(re, store, "images", id, key)
}
