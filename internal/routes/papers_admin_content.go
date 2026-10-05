package routes

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/pocketbase/pocketbase/core"
)

// Ops metadata remains available, but admin byte/link surfaces obey the same
// gate and frozen-source rules as papers:read. They never expose bucket presigns.
func adminFrozenAsset(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter *mineru.Converter, link bool) error {
	if !contentAccessEnabled(cfg) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "paper access disabled"})
	}
	id, kind := re.Request.PathValue("paper_id"), re.Request.PathValue("kind")
	if kind == "pdf" {
		if !link {
			return contentPDFHandler(re, cfg, store, catalog, converter, id)
		}
		rp, src, err := sourceForAccess(re.Request.Context(), store, catalog, id, re.Request.URL.Query().Get("source_id"), re.Request.URL.Query().Get("version"))
		if err != nil {
			return contentAccessErrorResponse(re, err)
		}
		setContentSourceHeaders(re, rp, src)
		return re.JSON(http.StatusOK, map[string]any{"kind": "pdf", "url": "/api/papers/" + rp.canonical + "/pdf?source_id=" + url.QueryEscape(src.SourceID), "paper_id": rp.canonical, "source_id": src.SourceID, "sha256": src.Sha256})
	}
	if kind != "md" && kind != "markdown" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "kind must be pdf or markdown"})
	}
	if !link {
		return contentDerivativeHandler(re, cfg, store, catalog, converter, id, "markdown")
	}
	rp, src, b, m, handled, err := readyContent(re, cfg, store, catalog, converter, id)
	if handled || err != nil {
		return err
	}
	return re.JSON(http.StatusOK, map[string]any{"kind": "markdown", "url": bundleMemberURL(rp.canonical, b.RevisionID, m.MarkdownPath), "paper_id": rp.canonical, "source_id": src.SourceID, "revision": b.RevisionID})
}

func adminFrozenBatch(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter *mineru.Converter) error {
	if !contentAccessEnabled(cfg) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "paper access disabled"})
	}
	ids := strings.Split(re.Request.URL.Query().Get("paper_ids"), ",")
	if len(ids) == 0 || len(ids) > assetBatchZipMax {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "paper_ids must contain 1..20 paper IDs"})
	}
	kind := re.Request.URL.Query().Get("kind")
	if kind == "" {
		kind = "pdf"
	}
	if kind != "pdf" && kind != "markdown" && kind != "md" {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "kind must be pdf or markdown"})
	}
	// Validate every selected item before emitting any ZIP bytes; failures are
	// never hidden as a successful incomplete archive. Bound total buffered data.
	members := make(map[string][]byte)
	total := 0
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "empty paper ID"})
		}
		var data []byte
		var name string
		if kind == "pdf" {
			rp, src, err := sourceForAccess(re.Request.Context(), store, catalog, id, "", "")
			if err != nil {
				return contentAccessErrorResponse(re, err)
			}
			data, err = paperbundle.New(store).ReadPDF(re.Request.Context(), rp.canonical, src.SourceID, src.Sha256, src.SizeBytes)
			if err != nil {
				return contentAccessErrorResponse(re, err)
			}
			name = rp.canonical + "-" + src.SourceID + ".pdf"
		} else {
			rp, _, b, m, handled, err := readyContent(re, cfg, store, catalog, converter, id)
			if handled || err != nil {
				return err
			}
			data, err = verifiedBundleMember(re.Request.Context(), store, b, m, m.MarkdownPath)
			if err != nil {
				return contentAccessErrorResponse(re, err)
			}
			name = rp.canonical + "-" + b.RevisionID + ".md"
		}
		if _, already := members[name]; already {
			continue
		}
		total += len(data)
		if total > 128<<20 {
			return re.JSON(http.StatusRequestEntityTooLarge, map[string]string{"detail": "batch exceeds 128 MiB; download selected sources individually"})
		}
		members[name] = data
	}
	archive, err := buildOriginalMembersZIP(members)
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	// A batch archive is not any one member's source or revision.
	for _, key := range []string{"X-QAtlas-Paper-Id", "X-QAtlas-Resolved-Id", "X-QAtlas-Requested-Id", "X-QAtlas-Source-Id", "X-QAtlas-Source-Origin", "X-QAtlas-PDF-SHA256", "X-QAtlas-Source-SHA256", "X-QAtlas-Parse-Revision", "X-QAtlas-Manifest-SHA256"} {
		re.Response.Header().Del(key)
	}
	digest := paperbundle.SHA256(archive)
	re.Response.Header().Set("X-QAtlas-Sha256", digest)
	re.Response.Header().Set("X-QAtlas-Artifact-SHA256", digest)
	re.Response.Header().Set("ETag", `"`+digest+`"`)
	re.Response.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	re.Response.Header().Set("Content-Disposition", `attachment; filename="qatlas-frozen-assets.zip"`)
	return re.Blob(http.StatusOK, "application/zip", archive)
}
