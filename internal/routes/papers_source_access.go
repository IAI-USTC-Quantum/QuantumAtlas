package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// All content routes resolve and freeze a PDF identity before reading content.
// Legacy markdown/JSON/images never participate in selection or readiness.
type contentCatalog interface {
	blockCatalog
	GetWithAssets(context.Context, string) (*registry.PaperDetail, bool, error)
	GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	RegisterFrozenPaperSource(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
	BindPaperSourceImport(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
	FreezePaperSource(context.Context, objstore.Store, registry.PaperSource) (registry.PaperSource, error)
	GetParseBundle(context.Context, string, string) (registry.ParseBundle, bool, error)
	GetReadyParseBundle(context.Context, objstore.Store, string, string) (registry.ParseBundle, bool, error)
}

var _ contentCatalog = (*registry.Store)(nil)

// AllowFetch is true only for a genuinely absent unselected PDF; frozen/pinned
// source failures can never authorize a mutable legacy read or a new download.
type contentAccessError struct {
	Status     int
	Detail     string
	Cause      error
	AllowFetch bool
}

func (e *contentAccessError) Error() string        { return e.Detail }
func (e *contentAccessError) Unwrap() error        { return e.Cause }
func contentAccessEnabled(cfg *config.Config) bool { return cfg != nil && cfg.PaperAccessEnabled }
func contentAccessErrorResponse(re *core.RequestEvent, err error) error {
	var access *contentAccessError
	if errors.As(err, &access) {
		if access.Status == http.StatusServiceUnavailable {
			re.Response.Header().Set("Retry-After", "5")
		}
		return re.JSON(access.Status, map[string]any{"detail": access.Detail})
	}
	if errors.Is(err, paperbundle.ErrIntegrity) || errors.Is(err, paperbundle.ErrInvalid) {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "immutable content integrity check failed"})
	}
	if errors.Is(err, objstore.ErrNotFound) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "selected immutable content object is missing"})
	}
	return assetStorageUnavailable(re, err)
}
func contentMissing(detail string, canFetch bool) error {
	return &contentAccessError{Status: http.StatusNotFound, Detail: detail, Cause: objstore.ErrNotFound, AllowFetch: canFetch}
}

func sourceOriginVersion(origin string) int {
	if strings.HasPrefix(origin, "arxiv:v") {
		n, _ := strconv.Atoi(strings.TrimPrefix(origin, "arxiv:v"))
		return n
	}
	if strings.HasPrefix(origin, "arxiv:") {
		return registry.ArxivVersionOf(strings.TrimPrefix(origin, "arxiv:"))
	}
	return 0
}
func normalizedSourceOrigin(origin string) string {
	if n := sourceOriginVersion(origin); n > 0 {
		return fmt.Sprintf("arxiv:v%d", n)
	}
	return origin
}
func sourceVersionMatches(origin, version string) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(version, "v"))
	return err == nil && n > 0 && sourceOriginVersion(origin) == n
}
func setContentSourceHeaders(re *core.RequestEvent, rp resolvedPaper, src registry.PaperSource) {
	applyResolutionHeaders(re.Response, rp.resolution)
	re.Response.Header().Set("X-QAtlas-Paper-Id", rp.canonical)
	re.Response.Header().Set("X-QAtlas-Source-Id", src.SourceID)
	re.Response.Header().Set("X-QAtlas-Sha256", src.Sha256)
	re.Response.Header().Set("X-QAtlas-PDF-SHA256", src.Sha256)
	re.Response.Header().Set("X-QAtlas-Source-Origin", normalizedSourceOrigin(src.Origin))
}

// sourceForAccess never substitutes a different source/version when a pin was
// supplied. The authoritative import mapping is checked before legacy objects.
func sourceForAccess(ctx context.Context, store objstore.Store, catalog contentCatalog, requestedID, sourceID, version string) (resolvedPaper, registry.PaperSource, error) {
	var zero registry.PaperSource
	if catalog == nil || store == nil {
		return resolvedPaper{}, zero, &contentAccessError{Status: 503, Detail: "content storage/catalog not configured"}
	}
	requestedID = normalizeIDForDispatch(strings.Trim(requestedID, "/"))
	wantVersion := 0
	if version != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(version, "v"))
		if err != nil || n < 1 {
			return resolvedPaper{}, zero, &contentAccessError{Status: 400, Detail: "version must be a positive arXiv vN"}
		}
		wantVersion = n
	}
	parsed, parseErr := paperassets.Parse(requestedID)
	isArxiv := parseErr == nil && parsed.IsValid()
	if isArxiv && parsed.Version != "" {
		n := registry.ArxivVersionOf(parsed.Canonical)
		if wantVersion > 0 && wantVersion != n {
			return resolvedPaper{}, zero, contentMissing("path version conflicts with requested version; refusing substitution", false)
		}
		wantVersion = n
	}
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		return rp, zero, &contentAccessError{Status: status, Detail: detail, AllowFetch: status == 404 && sourceID == "" && (isArxiv || isDOICandidate(requestedID)), Cause: objstore.ErrNotFound}
	}
	versionProven := false
	freeze := func(src registry.PaperSource) (resolvedPaper, registry.PaperSource, error) {
		frozen, err := catalog.FreezePaperSource(ctx, store, src)
		if err != nil {
			return rp, src, err
		}
		if frozen.PaperID != rp.canonical || frozen.SourceID != src.SourceID || frozen.Sha256 != src.Sha256 || frozen.ObjstoreKey != paperbundle.PDFKey(rp.canonical, src.SourceID) {
			return rp, src, paperbundle.ErrIntegrity
		}
		if wantVersion > 0 && (versionProven || sourceVersionMatches(src.Origin, strconv.Itoa(wantVersion))) {
			// Alias-proven edition is response metadata only. Never mutate the
			// stored source origin/history or its stable source/comment ID.
			frozen.Origin = fmt.Sprintf("arxiv:v%d", wantVersion)
		}
		return rp, frozen, nil
	}
	if sourceID != "" {
		src, found, err := catalog.GetPaperSource(ctx, rp.canonical, sourceID)
		if err != nil {
			return rp, zero, err
		}
		if !found {
			return rp, zero, contentMissing("no such source under the selected paper", false)
		}
		if wantVersion > 0 && !sourceVersionMatches(src.Origin, strconv.Itoa(wantVersion)) {
			arxiv := ""
			if isArxiv {
				arxiv = registry.NormalizeArxivID(parsed.Canonical)
			} else {
				d, found, err := catalog.GetWithAssets(ctx, rp.canonical)
				if err != nil {
					return rp, src, err
				}
				if found && d != nil && d.Paper != nil {
					arxiv = d.Paper.ArxivID
				}
			}
			if arxiv != "" {
				key := paperassets.AssetKey("pdf", fmt.Sprintf("%sv%d", registry.NormalizeArxivID(arxiv), wantVersion))
				bound, found, err := catalog.GetImportedPaperSource(ctx, rp.canonical, key)
				if err != nil {
					return rp, src, err
				}
				versionProven = found && bound.SourceID == src.SourceID && bound.Sha256 == src.Sha256
			}
			if !versionProven {
				return rp, src, contentMissing("source/version pin mismatch; refusing substitution", false)
			}
		}
		return freeze(src)
	}
	d, found, err := catalog.GetWithAssets(ctx, rp.canonical)
	if err != nil {
		return rp, zero, err
	}
	if !found || d == nil || d.Paper == nil {
		return rp, zero, contentMissing("no such paper", false)
	}
	arxivID := d.Paper.ArxivID
	if isArxiv {
		arxivID = registry.NormalizeArxivID(parsed.Canonical)
	}
	canonicalKey := ""
	if wantVersion > 0 {
		if arxivID == "" {
			return rp, zero, contentMissing("paper has no matching arXiv version; refusing published substitution", false)
		}
		canonicalKey = paperassets.AssetKey("pdf", fmt.Sprintf("%sv%d", registry.NormalizeArxivID(arxivID), wantVersion))
	} else if isDOICandidate(requestedID) {
		canonicalKey = paperassets.DOIAssetKey("pdf", requestedID)
	}
	if canonicalKey != "" {
		src, imported, err := catalog.GetImportedPaperSource(ctx, rp.canonical, canonicalKey)
		if err != nil {
			return rp, zero, err
		}
		if imported {
			versionProven = wantVersion > 0
			return freeze(src)
		}
	}
	sources, err := catalog.ListPaperSources(ctx, rp.canonical)
	if err != nil {
		return rp, zero, err
	}
	isPublished := func(origin string) bool {
		return strings.HasPrefix(origin, "doi:") || strings.HasPrefix(origin, "published")
	}
	rankSource := func(src registry.PaperSource) int {
		v := sourceOriginVersion(src.Origin)
		if wantVersion > 0 {
			if v == wantVersion {
				return 1000 + v
			}
			return -1
		}
		if isArxiv && isPublished(src.Origin) {
			return -1
		}
		if isPublished(src.Origin) {
			return 1000000
		}
		if v > 0 {
			return 1000 + v
		}
		return 1
	}
	sort.SliceStable(sources, func(i, j int) bool {
		ri, rj := rankSource(sources[i]), rankSource(sources[j])
		if ri != rj {
			return ri > rj
		}
		if !sources[i].CreatedAt.Equal(sources[j].CreatedAt) {
			return sources[i].CreatedAt.After(sources[j].CreatedAt)
		}
		return sources[i].SourceID > sources[j].SourceID
	})
	var selected registry.PaperSource
	srcRank := -1
	if len(sources) > 0 && rankSource(sources[0]) >= 0 {
		selected = sources[0]
		srcRank = rankSource(selected)
	}
	var asset registry.Asset
	assetRank := -1
	for _, a := range d.Assets {
		if a.PDFPath == "" {
			continue
		}
		r := -1
		if a.Source == "arxiv" && a.ArxivVersion > 0 && (wantVersion == 0 || a.ArxivVersion == wantVersion) {
			r = 1000 + a.ArxivVersion
		}
		if a.Source == "published" && !isArxiv && wantVersion == 0 {
			r = 1000000
		}
		if r > assetRank || (r == assetRank && a.FetchedAt.After(asset.FetchedAt)) {
			asset, assetRank = a, r
		}
	}
	if srcRank >= 0 && srcRank >= assetRank {
		return freeze(selected)
	}
	legacyKey := ""
	origin := ""
	if assetRank >= 0 {
		legacyKey = asset.PDFPath
		if !strings.HasPrefix(legacyKey, "pdf/") && !strings.HasPrefix(legacyKey, "content/") {
			legacyKey = "pdf/" + legacyKey
		}
		if asset.Source == "arxiv" {
			origin = fmt.Sprintf("arxiv:v%d", asset.ArxivVersion)
			canonicalKey = paperassets.AssetKey("pdf", fmt.Sprintf("%sv%d", registry.NormalizeArxivID(arxivID), asset.ArxivVersion))
		} else {
			origin = "published"
			if d.Paper.DOI != "" {
				canonicalKey = paperassets.DOIAssetKey("pdf", d.Paper.DOI)
			}
		}
	} else if canonicalKey != "" {
		legacyKey = canonicalKey
		origin = fmt.Sprintf("arxiv:v%d", wantVersion)
		if wantVersion == 0 {
			origin = "doi:" + requestedID
		}
	}
	if legacyKey == "" {
		return rp, zero, contentMissing("no PDF source available for the requested selection", true)
	}
	// An import mapping is authoritative even when the legacy path disappeared.
	for _, key := range []string{canonicalKey, legacyKey} {
		if key == "" {
			continue
		}
		src, imported, err := catalog.GetImportedPaperSource(ctx, rp.canonical, key)
		if err != nil {
			return rp, zero, err
		}
		if imported {
			return freeze(src)
		}
	}
	_, exists, err := store.Stat(ctx, legacyKey)
	if err != nil {
		return rp, zero, err
	}
	if !exists && isArxiv {
		id := parsed.Canonical
		if wantVersion > 0 {
			id = fmt.Sprintf("%sv%d", registry.NormalizeArxivID(arxivID), wantVersion)
		}
		legacyKey, _, exists, err = paperassets.LocateAssetByID(ctx, store, "pdf", id)
		if err != nil {
			return rp, zero, err
		}
		if exists {
			src, imported, err := catalog.GetImportedPaperSource(ctx, rp.canonical, legacyKey)
			if err != nil {
				return rp, zero, err
			}
			if imported {
				return freeze(src)
			}
		}
	}
	if !exists {
		return rp, zero, contentMissing("selected PDF not present", true)
	}
	src, err := catalog.RegisterFrozenPaperSource(ctx, store, rp.canonical, origin, legacyKey)
	if err != nil {
		return rp, src, err
	}
	if asset.PDFSha256 != "" && src.Sha256 != asset.PDFSha256 {
		return rp, src, paperbundle.ErrIntegrity
	}
	if canonicalKey != "" && canonicalKey != legacyKey {
		src, err = catalog.BindPaperSourceImport(ctx, store, rp.canonical, src.SourceID, canonicalKey)
		if err != nil {
			return rp, src, err
		}
	}
	if wantVersion > 0 && canonicalKey != "" {
		versionProven = true
	}
	return freeze(src)
}
