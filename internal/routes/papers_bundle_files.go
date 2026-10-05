package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// verifiedContentBundle checks the whole published inventory, not merely the
// requested member or a process-local Done snapshot. All object keys are derived
// from and checked against the immutable paper/source/revision identities.
func verifiedContentBundle(ctx context.Context, store objstore.Store, b registry.ParseBundle, src registry.PaperSource) (paperbundle.Manifest, error) {
	if store == nil {
		return paperbundle.Manifest{}, &contentAccessError{Status: http.StatusServiceUnavailable, Detail: "content object store unavailable"}
	}
	if b.PaperID != src.PaperID || b.SourceID != src.SourceID || b.SourcePDFSHA256 != src.Sha256 ||
		src.ObjstoreKey != paperbundle.PDFKey(src.PaperID, src.SourceID) ||
		b.ManifestKey != paperbundle.ManifestKey(b.PaperID, b.SourceID, b.RevisionID) ||
		b.ObjstoreKey != paperbundle.FileKey(b.PaperID, b.SourceID, b.RevisionID, b.MiddlePath) {
		return paperbundle.Manifest{}, paperbundle.ErrIntegrity
	}
	if !mineru.IsSupportedMiddleProfile(b.Schema, b.SchemaVersion) {
		return paperbundle.Manifest{}, &contentAccessError{Status: http.StatusUnprocessableEntity, Detail: "published bundle has an unsupported Middle JSON profile"}
	}
	if _, err := paperbundle.New(store).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes); err != nil {
		return paperbundle.Manifest{}, err
	}
	m, err := paperbundle.New(store).VerifyBundle(ctx, b.PaperID, b.SourceID, b.RevisionID)
	if err != nil {
		return m, err
	}
	middle, found := m.Member(m.MiddlePath)
	if !found || m.SourcePDFSHA256 != src.Sha256 || m.MiddlePath != b.MiddlePath || m.MarkdownPath != b.MarkdownPath || middle.SHA256 != b.ArtifactSha256 {
		return m, paperbundle.ErrIntegrity
	}
	manifestBytes, err := contentReadVerifiedObject(ctx, store, b.ManifestKey, b.ManifestSHA256)
	if err != nil {
		return m, err
	}
	if b.ManifestSHA256 == "" || paperbundle.SHA256(manifestBytes) != b.ManifestSHA256 {
		return m, paperbundle.ErrIntegrity
	}
	var pinned paperbundle.Manifest
	if json.Unmarshal(manifestBytes, &pinned) != nil || !sameContentManifest(m, pinned) {
		return m, paperbundle.ErrIntegrity
	}
	middleBytes, err := verifiedBundleMember(ctx, store, b, m, m.MiddlePath)
	if err != nil {
		return m, err
	}
	if _, err := mineru.ParseMiddleJSON(middleBytes); err != nil {
		return m, &contentAccessError{Status: http.StatusUnprocessableEntity, Detail: "published Middle JSON is invalid", Cause: err}
	}
	return m, nil
}

func sameContentManifest(a, b paperbundle.Manifest) bool {
	// The raw bytes hash is pinned separately; structural equality ensures the
	// inventory whose members were verified is precisely that pinned inventory.
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func verifiedBundleMember(ctx context.Context, store objstore.Store, b registry.ParseBundle, m paperbundle.Manifest, member string) ([]byte, error) {
	if err := paperbundle.ValidateMemberPath(member); err != nil {
		return nil, err
	}
	entry, found := m.Member(member)
	if !found {
		return nil, objstore.ErrNotFound
	}
	data, err := contentReadVerifiedObject(ctx, store, paperbundle.FileKey(b.PaperID, b.SourceID, b.RevisionID, member), entry.SHA256)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != entry.SizeBytes || paperbundle.SHA256(data) != entry.SHA256 {
		return nil, paperbundle.ErrIntegrity
	}
	return data, nil
}

// pinnedContentBundle never follows today's current pointer or submits repair.
// A historical comments /parses/{revision}/json object without a parse_bundles
// publication row is intentionally not a complete reading/file bundle.
func pinnedContentBundle(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, requestedID, revisionID string) (rp resolvedPaper, src registry.PaperSource, b registry.ParseBundle, m paperbundle.Manifest, handled bool, err error) {
	if !contentAccessEnabled(cfg) {
		return rp, src, b, m, true, re.JSON(http.StatusNotFound, map[string]string{"detail": "paper content access is disabled"})
	}
	if queryErr := validateContentQuery(re); queryErr != nil {
		return rp, src, b, m, true, contentBundleErrorResponse(re, queryErr)
	}
	if catalog == nil || store == nil {
		return rp, src, b, m, true, contentBundleErrorResponse(re, &contentAccessError{Status: http.StatusServiceUnavailable, Detail: "content catalog or object store unavailable"})
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	var status int
	var detail string
	rp, status, detail = resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		return rp, src, b, m, true, blockResolveError(re, status, detail)
	}
	applyResolutionHeaders(re.Response, rp.resolution)
	var found bool
	b, found, err = catalog.GetParseBundle(ctx, rp.canonical, revisionID)
	if err != nil {
		return rp, src, b, m, true, contentBundleErrorResponse(re, err)
	}
	if !found {
		return rp, src, b, m, true, re.JSON(http.StatusNotFound, map[string]string{"detail": "no published complete bundle for this revision"})
	}
	if b.PaperID != rp.canonical || b.RevisionID != revisionID {
		return rp, src, b, m, true, contentBundleErrorResponse(re, paperbundle.ErrIntegrity)
	}
	if want := re.Request.URL.Query().Get("source_id"); want != "" && want != b.SourceID {
		return rp, src, b, m, true, re.JSON(http.StatusConflict, map[string]string{"code": "source_revision_mismatch", "detail": "revision is not bound to the requested source"})
	}
	rp, src, err = sourceForAccess(ctx, store, catalog, requestedID, b.SourceID, re.Request.URL.Query().Get("version"))
	if err != nil {
		return rp, src, b, m, true, contentBundleErrorResponse(re, err)
	}
	setContentSourceHeaders(re, rp, src)
	m, err = verifiedContentBundle(ctx, store, b, src)
	if err != nil {
		return rp, src, b, m, true, contentBundleErrorResponse(re, err)
	}
	return rp, src, b, m, false, nil
}

func setBundleReadHeaders(re *core.RequestEvent, b registry.ParseBundle) {
	re.Response.Header().Set("X-QAtlas-Parse-Revision", b.RevisionID)
	re.Response.Header().Set("X-QAtlas-Manifest-SHA256", b.ManifestSHA256)
	re.Response.Header().Set("X-QAtlas-PDF-SHA256", b.SourcePDFSHA256)
	re.Response.Header().Set("X-QAtlas-Source-SHA256", b.SourcePDFSHA256)
	re.Response.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	re.Response.Header().Set("X-Content-Type-Options", "nosniff")
}

func paperBundleManifestHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, requestedID, revisionID string) error {
	_, _, b, _, handled, err := pinnedContentBundle(re, cfg, store, catalog, requestedID, revisionID)
	if handled || err != nil {
		return err
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	data, err := contentReadVerifiedObject(ctx, store, b.ManifestKey, b.ManifestSHA256)
	if err != nil {
		return contentBundleErrorResponse(re, err)
	}
	setBundleReadHeaders(re, b)
	re.Response.Header().Set("Content-Type", "application/json")
	re.Response.Header().Set("X-QAtlas-Sha256", b.ManifestSHA256)
	re.Response.Header().Set("X-QAtlas-Artifact-SHA256", b.ManifestSHA256)
	re.Response.Header().Set("ETag", `"`+b.ManifestSHA256+`"`)
	http.ServeContent(re.Response, re.Request, "manifest.json", b.CreatedAt, bytes.NewReader(data))
	return nil
}

func paperBundleFileHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, requestedID, revisionID, member string) error {
	if !contentAccessEnabled(cfg) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "paper content access is disabled"})
	}
	if err := paperbundle.ValidateMemberPath(member); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"code": "invalid_member", "detail": "invalid original bundle member path"})
	}
	_, _, b, m, handled, err := pinnedContentBundle(re, cfg, store, catalog, requestedID, revisionID)
	if handled || err != nil {
		return err
	}
	entry, found := m.Member(member)
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "member is not listed in the pinned manifest"})
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	data, err := verifiedBundleMember(ctx, store, b, m, member)
	if err != nil {
		return contentBundleErrorResponse(re, err)
	}
	setBundleReadHeaders(re, b)
	contentType, disposition := bundleMemberMediaType(member)
	re.Response.Header().Set("Content-Type", contentType)
	re.Response.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(member)}))
	re.Response.Header().Set("X-QAtlas-Sha256", entry.SHA256)
	re.Response.Header().Set("X-QAtlas-Artifact-SHA256", entry.SHA256)
	re.Response.Header().Set("ETag", `"`+entry.SHA256+`"`)
	http.ServeContent(re.Response, re.Request, path.Base(member), b.CreatedAt, bytes.NewReader(data))
	return nil
}

func bundleMemberMediaType(member string) (string, string) {
	switch strings.ToLower(path.Ext(member)) {
	case ".png":
		return "image/png", "inline"
	case ".jpg", ".jpeg":
		return "image/jpeg", "inline"
	case ".webp":
		return "image/webp", "inline"
	case ".gif":
		return "image/gif", "inline"
	case ".json":
		return "application/json", "attachment"
	case ".md", ".txt", ".csv":
		return "text/plain; charset=utf-8", "attachment"
	default:
		return "application/octet-stream", "attachment"
	}
}

// Distinguish a corrupt pin from an outage when a no-pin cache is checked.
func contentBundleCorrupt(err error) bool {
	return errors.Is(err, objstore.ErrNotFound) || errors.Is(err, paperbundle.ErrIntegrity) || errors.Is(err, paperbundle.ErrInvalid)
}
func contentReadVerifiedObject(ctx context.Context, store objstore.Store, key, sha string) ([]byte, error) {
	r, _, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if sha == "" || paperbundle.SHA256(data) != sha {
		return nil, paperbundle.ErrIntegrity
	}
	return data, nil
}
func contentBundleErrorResponse(re *core.RequestEvent, err error) error {
	if errors.Is(err, paperbundle.ErrIntegrity) || errors.Is(err, paperbundle.ErrInvalid) {
		return re.JSON(http.StatusUnprocessableEntity, map[string]string{"code": "content_integrity_failed", "detail": "immutable content bundle integrity check failed"})
	}
	return contentAccessErrorResponse(re, err)
}
