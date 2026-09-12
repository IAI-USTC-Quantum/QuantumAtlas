package routes

// papers_figures.go: GET /api/papers/{id}/figures — the figure/caption
// index of a paper's MinerU markdown.
//
// The index answers "which figures does this paper have, what do their
// captions say, and which image files make up each figure" without the
// client downloading the markdown and the images zip itself. Grouping is
// done by paperassets.ExtractFigures (consecutive image references share
// one caption; captions may sit above or below the group); the per-image
// sizes come from the asset's images object — the zip central directory
// for the canonical zip layout, a prefix listing for the legacy per-paper
// directory layout.
//
// The handler accepts every id form the asset endpoints take (qa_
// surrogate, arXiv id, DOI) and resolves through the catalog, mirroring
// papers_images.go: catalog miss → 404, missing markdown → 200 with
// markdown_ready:false (the paper exists, its conversion just hasn't
// produced a markdown yet).

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// Sentinels for the id-form resolver shared by the figures / single-image
// endpoints. errPaperNotHosted maps to 404; errInvalidPaperRef to 400.
var (
	errPaperNotHosted  = errors.New("paper not hosted")
	errInvalidPaperRef = errors.New("unrecognized paper reference")
)

// figureImageJSON is one image object referenced by (or leftover from) a
// figure group. URL points at the single-image download endpoint.
type figureImageJSON struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

// figureJSON is one extracted figure group.
type figureJSON struct {
	FigNo   int               `json:"fig_no"`
	Caption string            `json:"caption"`
	Context string            `json:"context"`
	Images  []figureImageJSON `json:"images"`
}

// paperFiguresHandler answers GET /api/papers/{id}/figures for any id
// form (qa_ surrogate, arXiv id, DOI).
func paperFiguresHandler(re *core.RequestEvent, catalog paperCatalog, store objstore.Store, requestedID string) error {
	ctx := re.Request.Context()
	detail, err := resolvePaperDetail(ctx, catalog, requestedID)
	if err != nil {
		return figuresResolveError(re, requestedID, err)
	}

	resp := struct {
		PaperID         string            `json:"paper_id"`
		ResolvedID      string            `json:"resolved_id"`
		MarkdownReady   bool              `json:"markdown_ready"`
		Figures         []figureJSON      `json:"figures"`
		UnmatchedImages []figureImageJSON `json:"unmatched_images"`
		ImageCount      int               `json:"image_count"`
	}{
		PaperID:         detail.Paper.PaperID,
		Figures:         []figureJSON{},
		UnmatchedImages: []figureImageJSON{},
	}

	asset, hasAsset := defaultPaperAsset(detail)
	if !hasAsset {
		return re.JSON(http.StatusOK, resp)
	}
	resp.ResolvedID = assetServingIdentity(detail.Paper, asset)
	if resp.ResolvedID == "" {
		return re.JSON(http.StatusOK, resp)
	}
	imageURL := func(name string) string {
		return "/api/papers/" + resp.ResolvedID + "/images/" + name
	}

	md, mdOK, err := fetchAssetMarkdown(ctx, store, detail.Paper, asset)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch markdown: " + err.Error(),
		})
	}
	resp.MarkdownReady = mdOK

	sizes, err := assetImageSizes(ctx, store, detail.Paper, asset)
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "list images: " + err.Error(),
		})
	}
	resp.ImageCount = len(sizes)

	referenced := map[string]bool{}
	if mdOK {
		for _, f := range paperassets.ExtractFigures(string(md)) {
			fj := figureJSON{FigNo: f.FigNo, Caption: f.Caption, Context: f.Context, Images: []figureImageJSON{}}
			for _, name := range f.Images {
				referenced[name] = true
				fj.Images = append(fj.Images, figureImageJSON{Name: name, Size: sizes[name], URL: imageURL(name)})
			}
			resp.Figures = append(resp.Figures, fj)
		}
	}
	names := make([]string, 0, len(sizes))
	for name := range sizes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !referenced[name] {
			resp.UnmatchedImages = append(resp.UnmatchedImages, figureImageJSON{Name: name, Size: sizes[name], URL: imageURL(name)})
		}
	}
	return re.JSON(http.StatusOK, resp)
}

// figuresResolveError maps the id-form resolver's errors onto the usual
// HTTP convention (shared with the single-image endpoint).
func figuresResolveError(re *core.RequestEvent, requestedID string, err error) error {
	switch {
	case errors.Is(err, registry.ErrCatalogUnavailable):
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
		})
	case errors.Is(err, errPaperNotHosted):
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": "no such paper: " + requestedID,
		})
	case errors.Is(err, errInvalidPaperRef):
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("unrecognized paper id: %q (expected a qa_ surrogate, an arXiv id, or a DOI)", requestedID),
		})
	default:
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
}

// isQASurrogate reports whether s is a bare qa_ paper_id (no sub-path).
func isQASurrogate(s string) bool {
	pid, ok := strings.CutPrefix(s, "qa_")
	return ok && pid != "" && !strings.Contains(pid, "/")
}

// resolvePaperDetail resolves any accepted id form — qa_ surrogate, arXiv
// id (bare or versioned), or DOI (bare or URL form) — onto the paper's
// registry detail. qa_ ids go straight to GetWithAssets; external ids
// resolve through GetPaperIDByIdentity with the same normalization
// dispatchDetailByIdentifier applies. errPaperNotHosted marks a
// well-formed but unknown id.
func resolvePaperDetail(ctx context.Context, catalog paperCatalog, requestedID string) (*registry.PaperDetail, error) {
	ident := normalizeIDForDispatch(strings.Trim(requestedID, "/"))
	if isQASurrogate(ident) {
		d, found, err := catalog.GetWithAssets(ctx, ident)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errPaperNotHosted
		}
		return d, nil
	}
	scheme, key := "", ""
	switch {
	case isDOICandidate(ident):
		scheme, key = "doi", ident
	default:
		if parsed, perr := paperassets.Parse(ident); perr == nil && parsed.IsValid() {
			scheme, key = "arxiv", registry.NormalizeArxivID(ident)
		} else {
			return nil, errInvalidPaperRef
		}
	}
	paperID, found, err := catalog.GetPaperIDByIdentity(ctx, scheme, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errPaperNotHosted
	}
	d, found, err := catalog.GetWithAssets(ctx, paperID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errPaperNotHosted
	}
	return d, nil
}

// defaultPaperAsset returns the paper's default asset — the registry's
// asset ordering (published first, then highest arXiv version) makes
// Assets[0] the serving default.
func defaultPaperAsset(d *registry.PaperDetail) (registry.Asset, bool) {
	if len(d.Assets) == 0 {
		return registry.Asset{}, false
	}
	return d.Assets[0], true
}

// assetServingIdentity returns the identity the paper's bytes are served
// under for the given asset: the DOI namespace for published assets, the
// (versioned when known) arXiv id otherwise. Empty when the paper carries
// no usable identity.
func assetServingIdentity(p *registry.Paper, a registry.Asset) string {
	if a.Source == "published" && p.DOI != "" {
		return registry.NormalizeDOI(p.DOI)
	}
	if p.ArxivID != "" {
		if a.ArxivVersion > 0 {
			return fmt.Sprintf("%sv%d", p.ArxivID, a.ArxivVersion)
		}
		return p.ArxivID
	}
	if p.DOI != "" {
		return registry.NormalizeDOI(p.DOI)
	}
	return ""
}

// fetchAssetMarkdown returns the asset's markdown bytes, or (nil, false)
// when no markdown object exists for it. Published assets read the DOI
// namespace; arXiv assets locate the object via the dual-read
// paperassets.LocateAsset (canonical + legacy layouts), the same probing
// serveReadyAsset performs.
func fetchAssetMarkdown(ctx context.Context, store objstore.Store, p *registry.Paper, a registry.Asset) ([]byte, bool, error) {
	if store == nil {
		return nil, false, nil
	}
	var key string
	if a.Source == "published" {
		key = paperassets.DOIAssetKey("markdown", p.DOI)
		if key == "" {
			return nil, false, nil
		}
		if _, exists, err := store.Stat(ctx, key); err != nil {
			return nil, false, err
		} else if !exists {
			return nil, false, nil
		}
	} else {
		parsed, perr := paperassets.Parse(assetServingIdentity(p, a))
		if perr != nil {
			return nil, false, nil
		}
		var exists bool
		var err error
		key, _, exists, err = paperassets.LocateAsset(ctx, store, "markdown", parsed)
		if err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, nil
		}
	}
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, paperassets.MaxMarkdownBytes+1))
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// assetImageSizes lists the asset's image files as name → size. Zip
// candidates (the canonical write layout) are read through their central
// directory only — no member is decompressed; dir candidates (the legacy
// per-paper layout) come from a prefix listing. The first candidate that
// yields objects wins, mirroring listAssetImages' probe order.
func assetImageSizes(ctx context.Context, store objstore.Store, p *registry.Paper, a registry.Asset) (map[string]int64, error) {
	sizes := map[string]int64{}
	if store == nil {
		return sizes, nil
	}
	for _, cand := range imageListingCandidates(p, a) {
		switch {
		case strings.HasSuffix(cand, ".zip"):
			zr, err := getZipReader(ctx, store, cand)
			if err != nil {
				return nil, err
			}
			if zr == nil {
				continue
			}
			for _, f := range zr.File {
				if f.FileInfo().IsDir() {
					continue
				}
				name := strings.TrimPrefix(f.Name, "images/")
				sizes[name] = int64(f.UncompressedSize64)
			}
			if len(sizes) > 0 {
				return sizes, nil
			}
		case strings.HasSuffix(cand, "/"):
			infos, err := store.ListPrefix(ctx, cand, imagesListMaxFiles+1)
			if err != nil {
				return nil, fmt.Errorf("list %s: %w", cand, err)
			}
			if len(infos) == 0 {
				continue
			}
			for _, info := range infos {
				sizes[strings.TrimPrefix(info.Key, cand)] = info.Size
			}
			return sizes, nil
		}
	}
	return sizes, nil
}

// getZipReader fetches a zip object and opens it for central-directory
// access. Returns (nil, nil) when the object is absent; a parse failure
// is a real error (the images key existed but holds non-zip bytes).
func getZipReader(ctx context.Context, store objstore.Store, key string) (*zip.Reader, error) {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, paperassets.MaxMineruZipBytes+1))
	rc.Close()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	zr, zerr := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if zerr != nil {
		return nil, fmt.Errorf("open images zip %s: %w", key, zerr)
	}
	return zr, nil
}
