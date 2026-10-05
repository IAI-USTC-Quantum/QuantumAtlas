package routes

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/pocketbase/pocketbase/core"
)

// dispatchContentGET owns all unpinned convenience content paths. Source and
// revision originals are dispatched separately, under the same master gate.
// Identity selection occurs before the old DOI-canonical dispatcher can run.
func dispatchContentGET(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter *mineru.Converter, raw string) (bool, error) {
	// Immutable originals own their entire nested namespace. In particular,
	// /parses/{revision}/files/images/{name} must not be mistaken for the
	// convenience /{paper}/images/{name} endpoint merely by its member path.
	if strings.Contains(raw, "/parses/") || strings.Contains(raw, "/sources/") {
		return false, nil
	}
	id, action := "", ""
	for _, suffix := range []string{"/read/status", "/markdown/status", "/pdf/status", "/images/zip", "/read", "/markdown", "/pdf", "/images", "/figures"} {
		if prefix, ok := strings.CutSuffix(raw, suffix); ok {
			id, action = prefix, strings.TrimPrefix(suffix, "/")
			break
		}
	}
	if action == "" {
		if prefix, member, ok := strings.Cut(raw, "/images/"); ok {
			id, action = prefix, "images/"+member
		}
	}
	if action == "" {
		return false, nil
	}
	if !contentAccessEnabled(cfg) {
		return true, re.JSON(http.StatusNotFound, map[string]string{"detail": "paper access disabled"})
	}
	id = normalizeIDForDispatch(id)
	if id == "" {
		return true, re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing paper id"})
	}
	switch action {
	case "pdf":
		return true, contentPDFHandler(re, cfg, store, catalog, converter, id)
	case "pdf/status":
		return true, contentPDFStatusHandler(re, cfg, store, catalog, converter, id)
	case "read":
		return true, contentReadHandler(re, cfg, store, catalog, converter, id)
	case "read/status", "markdown/status":
		return true, contentReadStatusHandler(re, cfg, store, catalog, converter, id)
	default:
		return true, contentDerivativeHandler(re, cfg, store, catalog, converter, id, action)
	}
}

func isBundleImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tif", ".tiff", ".bmp", ".svg":
		return true
	}
	return false
}

// bundleImageMember resolves a producer-relative image reference without
// flattening directories or guessing an image from a different revision.
func bundleImageMember(m paperbundle.Manifest, markdownPath, name string) (paperbundle.Member, bool) {
	if paperbundle.ValidateMemberPath(name) != nil {
		return paperbundle.Member{}, false
	}
	for _, candidate := range []string{path.Join(path.Dir(markdownPath), name), name, "images/" + name} {
		if member, ok := m.Member(candidate); ok && isBundleImage(member.Path) {
			return member, true
		}
	}
	return paperbundle.Member{}, false
}

func contentDerivativeHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter contentReadConverter, requestedID, action string) error {
	rp, src, b, m, handled, err := readyContent(re, cfg, store, catalog, converter, requestedID)
	if handled || err != nil {
		return err
	}
	setContentSourceHeaders(re, rp, src)
	re.Response.Header().Set("X-QAtlas-Parse-Revision", b.RevisionID)
	re.Response.Header().Set("Cache-Control", "private, no-cache")
	if action == "markdown" {
		member, ok := m.Member(m.MarkdownPath)
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "bundle has no original markdown"})
		}
		if assetFormat(re) == "link" {
			// Keep authentication and the master switch on every follow-up request;
			// never expose a private NAS presign as an agent-public URL.
			return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "revision": b.RevisionID, "format": "link", "markdown_url": bundleMemberURL(rp.canonical, b.RevisionID, member.Path), "sha256": member.SHA256})
		}
		data, err := blockReadVerified(re.Request.Context(), store, paperbundle.FileKey(rp.canonical, src.SourceID, b.RevisionID, member.Path), member.SHA256)
		if err != nil {
			return contentAccessErrorResponse(re, err)
		}
		re.Response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		re.Response.Header().Set("X-QAtlas-Sha256", member.SHA256)
		re.Response.Header().Set("X-QAtlas-Artifact-SHA256", member.SHA256)
		re.Response.Header().Set("ETag", `"`+member.SHA256+`"`)
		http.ServeContent(re.Response, re.Request, path.Base(member.Path), time.Time{}, bytes.NewReader(data))
		return nil
	}
	if strings.HasPrefix(action, "images/") && action != "images/zip" {
		member, ok := bundleImageMember(m, m.MarkdownPath, strings.TrimPrefix(action, "images/"))
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "image not in selected bundle"})
		}
		return paperBundleFileHandler(re, cfg, store, catalog, rp.canonical, b.RevisionID, member.Path)
	}
	images := make(map[string]paperbundle.Member)
	files := make([]map[string]any, 0)
	for _, member := range m.Files {
		if !isBundleImage(member.Path) {
			continue
		}
		images[member.Path] = member
		files = append(files, map[string]any{"key": member.Path, "name": member.Path, "size": member.SizeBytes, "sha256": member.SHA256, "url": bundleMemberURL(rp.canonical, b.RevisionID, member.Path)})
	}
	if action == "images" {
		return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "revision": b.RevisionID, "files": files, "truncated": false, "assets": []map[string]any{{"source_id": src.SourceID, "revision": b.RevisionID, "kind": "bundle", "files": files, "truncated": false}}})
	}
	if action == "images/zip" {
		members := make(map[string][]byte, len(images))
		for name, member := range images {
			data, err := blockReadVerified(re.Request.Context(), store, paperbundle.FileKey(rp.canonical, src.SourceID, b.RevisionID, name), member.SHA256)
			if err != nil {
				return contentAccessErrorResponse(re, err)
			}
			members[name] = data
		}
		archive, err := buildOriginalMembersZIP(members)
		if err != nil {
			return contentAccessErrorResponse(re, err)
		}
		digest := paperbundle.SHA256(archive)
		re.Response.Header().Set("X-QAtlas-Sha256", digest)
		re.Response.Header().Set("X-QAtlas-Artifact-SHA256", digest)
		re.Response.Header().Set("ETag", `"`+digest+`"`)
		re.Response.Header().Set("Content-Type", "application/zip")
		re.Response.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", rp.canonical+"-"+b.RevisionID+"-images.zip"))
		http.ServeContent(re.Response, re.Request, "images.zip", time.Time{}, bytes.NewReader(archive))
		return nil
	}
	if action == "figures" {
		md, ok := m.Member(m.MarkdownPath)
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "bundle has no original markdown"})
		}
		data, err := blockReadVerified(re.Request.Context(), store, paperbundle.FileKey(rp.canonical, src.SourceID, b.RevisionID, md.Path), md.SHA256)
		if err != nil {
			return contentAccessErrorResponse(re, err)
		}
		figures := make([]figureJSON, 0)
		referenced := make(map[string]bool)
		for _, f := range paperassets.ExtractFigures(string(data)) {
			item := figureJSON{FigNo: f.FigNo, Caption: f.Caption, Context: f.Context, Images: []figureImageJSON{}}
			for _, name := range f.Images {
				if member, ok := bundleImageMember(m, m.MarkdownPath, name); ok {
					referenced[member.Path] = true
					item.Images = append(item.Images, figureImageJSON{Name: member.Path, Size: member.SizeBytes, URL: bundleMemberURL(rp.canonical, b.RevisionID, member.Path)})
				}
			}
			figures = append(figures, item)
		}
		unmatched := make([]figureImageJSON, 0)
		for _, member := range m.Files {
			if isBundleImage(member.Path) && !referenced[member.Path] {
				unmatched = append(unmatched, figureImageJSON{Name: member.Path, Size: member.SizeBytes, URL: bundleMemberURL(rp.canonical, b.RevisionID, member.Path)})
			}
		}
		return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "resolved_id": rp.canonical, "source_id": src.SourceID, "revision": b.RevisionID, "markdown_ready": true, "figures": figures, "unmatched_images": unmatched, "image_count": len(images)})
	}
	return re.JSON(http.StatusNotFound, map[string]string{"detail": "unknown content action"})
}
