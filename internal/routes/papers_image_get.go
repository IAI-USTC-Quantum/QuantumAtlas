package routes

// papers_image_get.go: GET /api/papers/{id}/images/{name} — the
// single-image download. Figures index entries point here, so a client
// can fetch one panel without pulling the whole images zip.
//
// name must be the MinerU image file name (sha256 hex + known image
// extension) — anything else is a 400 rather than a store probe. The
// bytes come from the paper's default asset's images object: the zip
// layout serves the member out of the fetched zip (in-memory), the
// legacy per-paper directory layout serves the object directly. Images
// are immutable content-addressed bytes, so responses carry a day of
// shared-cache.

import (
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"

	"github.com/pocketbase/pocketbase/core"
)

// imageFileNameRE is the whitelist for single-image downloads: a MinerU
// image file name (sha256 hex + known raster extension).
var imageFileNameRE = regexp.MustCompile(`^[0-9a-f]{64}\.(jpg|jpeg|png|gif|webp)$`)

// isImageFileName reports whether name is a MinerU image file name.
func isImageFileName(name string) bool {
	return imageFileNameRE.MatchString(name)
}

// isImagesMemberAction reports whether the dispatcher's action names one
// image member ("<id>/images/<name>" after the member peel).
func isImagesMemberAction(action string) bool {
	name, ok := strings.CutPrefix(action, "images/")
	return ok && isImageFileName(name)
}

// imageContentType maps the whitelisted extension to its MIME type.
func imageContentType(name string) string {
	switch path.Ext(name) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// paperImageGetHandler answers GET /api/papers/{id}/images/{name} for
// any id form (qa_ surrogate, arXiv id, DOI).
func paperImageGetHandler(re *core.RequestEvent, catalog paperCatalog, store objstore.Store, requestedID, name string) error {
	if !isImageFileName(name) {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "invalid image name " + strconv.Quote(name) + " (expected <sha256-hex>.<jpg|jpeg|png|gif|webp>)",
		})
	}
	ctx := re.Request.Context()
	detail, err := resolvePaperDetail(ctx, catalog, requestedID)
	if err != nil {
		return figuresResolveError(re, requestedID, err)
	}
	asset, hasAsset := defaultPaperAsset(detail)
	if !hasAsset {
		return imageGetNotFound(re, requestedID, name)
	}
	for _, cand := range imageListingCandidates(detail.Paper, asset) {
		switch {
		case strings.HasSuffix(cand, ".zip"):
			zr, err := getZipReader(ctx, store, cand)
			if err != nil {
				return re.JSON(http.StatusInternalServerError, map[string]string{
					"detail": "open images: " + err.Error(),
				})
			}
			if zr == nil {
				continue
			}
			for _, f := range zr.File {
				if f.FileInfo().IsDir() || strings.TrimPrefix(f.Name, "images/") != name {
					continue
				}
				mrc, merr := f.Open()
				if merr != nil {
					return re.JSON(http.StatusInternalServerError, map[string]string{
						"detail": "open image member: " + merr.Error(),
					})
				}
				defer mrc.Close()
				return serveImageBytes(re, mrc, name, int64(f.UncompressedSize64))
			}
			// Member not in this zip — try the next candidate layout.
		case strings.HasSuffix(cand, "/"):
			rc, info, err := store.Get(ctx, cand+name)
			if err != nil {
				if errors.Is(err, objstore.ErrNotFound) {
					continue
				}
				return re.JSON(http.StatusInternalServerError, map[string]string{
					"detail": "fetch image: " + err.Error(),
				})
			}
			defer rc.Close()
			return serveImageBytes(re, rc, name, info.Size)
		}
	}
	return imageGetNotFound(re, requestedID, name)
}

// imageGetNotFound is the shared 404 for a paper whose images hold no
// such member.
func imageGetNotFound(re *core.RequestEvent, requestedID, name string) error {
	return re.JSON(http.StatusNotFound, map[string]string{
		"detail": "no image " + name + " for paper " + requestedID,
		"name":   name,
	})
}

// serveImageBytes streams image bytes with the per-extension content
// type and a day of shared caching (content-addressed, immutable files).
func serveImageBytes(re *core.RequestEvent, r io.Reader, name string, size int64) error {
	h := re.Response.Header()
	h.Set("Content-Type", imageContentType(name))
	h.Set("Cache-Control", "public, max-age=86400")
	if size > 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	re.Response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(re.Response, r); err != nil {
		// Client went away mid-stream — nothing left to report.
		_ = err
	}
	return nil
}

// splitQAImagesMember parses the qa_ three-segment form
// "qa_<ulid>/images/<name>" into (id, name), but only for a whitelisted
// image file name — ".../images/zip" and anything else belong to the
// gated asset dispatcher further down (the qa_ resolution inside the
// PaperAccessEnabled block). ok=false for any other shape.
func splitQAImagesMember(raw string) (id, name string, ok bool) {
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) != 3 || len(parts[0]) <= len("qa_") ||
		!strings.HasPrefix(parts[0], "qa_") || parts[1] != "images" ||
		!isImageFileName(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// peelImagesMemberAction rewrites the "<id>/images/<name>" path parse
// into a single action, mirroring peelImagesZipAction: splitPapersPath
// anchors on the last slash, so "<id>/images/<name>.jpg" parses into
// ("<id>/images", "<name>.jpg") — glue the trailing "/images" back off
// the id and report action "images/<name>". Anything else (including a
// name that is not a whitelisted image file name) passes through
// unchanged.
func peelImagesMemberAction(arxivID, action string) (string, string) {
	if isImageFileName(action) && strings.HasSuffix(arxivID, "/images") {
		return strings.TrimSuffix(arxivID, "/images"), "images/" + action
	}
	return arxivID, action
}
