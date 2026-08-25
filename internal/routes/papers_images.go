package routes

// papers_images.go: GET /api/papers/{paper_id}/images — the on-demand
// per-paper image listing. The sync pipeline no longer lists the images
// bucket (image_count on the asset comes from MinerU upload metadata);
// when someone accesses a paper, the actual image file list is fetched
// straight from the object store under the asset's images prefix.
//
// Two arXiv layouts exist in production (plus the DOI zip namespace):
//
//   - zip, new-style:     images/<yymm>/<stem>.zip
//   - zip, with category: images/<yymm>/<category>/<stem>.zip
//   - per-paper dir:      images/<yymm>/<stem>/<file...>
//   - DOI zip:            images/doi/<reg>/<suffix>.zip
//
// Each asset reports kind "zip" or "dir" depending on which layout the
// listing found; an asset with no images reports kind "zip" (the
// canonical write layout since the converter stores one zip per paper)
// with an empty files list.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// imagesListMaxFiles caps the per-asset file list. The listing probe
// asks for one extra object so truncated is exact (true only when a
// 201st object exists).
const imagesListMaxFiles = 200

// imageFileJSON is the wire shape of one listed image object.
type imageFileJSON struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

// paperImagesHandler answers GET /api/papers/{paper_id}/images,
// dispatched from the /api/papers/{path...} catch-all for
// "qa_"-prefixed two-segment paths.
func paperImagesHandler(re *core.RequestEvent, catalog *registry.Store, store objstore.Store, paperID string) error {
	ctx := re.Request.Context()
	detail, found, err := catalog.GetWithAssets(ctx, paperID)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": "no such paper: " + paperID,
		})
	}

	assets := make([]map[string]any, 0, len(detail.Assets))
	for _, a := range detail.Assets {
		kind, files, truncated, err := listAssetImages(ctx, store, imageListingCandidates(detail.Paper, a))
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "list images: " + err.Error(),
			})
		}
		assets = append(assets, map[string]any{
			"asset_id":      a.AssetID,
			"source":        a.Source,
			"arxiv_version": a.ArxivVersion,
			"kind":          kind,
			"files":         files,
			"truncated":     truncated,
		})
	}
	return re.JSON(http.StatusOK, map[string]any{
		"paper_id": paperID,
		"assets":   assets,
	})
}

// imageListingCandidates returns the object-key prefixes to probe for
// one asset's images, most-preferred first. Zip candidates end in
// ".zip"; dir candidates end in "/". Empty when the asset's identity
// cannot be rendered into an images key (e.g. an arXiv asset on a paper
// whose arxiv_id is unknown).
func imageListingCandidates(p *registry.Paper, a registry.Asset) []string {
	switch a.Source {
	case "published":
		zip := paperassets.DOIAssetKey("images", p.DOI)
		if zip == "" {
			return nil
		}
		return []string{zip, strings.TrimSuffix(zip, ".zip") + "/"}
	case "arxiv":
		if p.ArxivID == "" || a.ArxivVersion <= 0 {
			return nil
		}
		versioned := fmt.Sprintf("%sv%d", p.ArxivID, a.ArxivVersion)
		zip := paperassets.AssetKey("images", versioned)
		if zip == "" {
			return nil
		}
		out := []string{zip, strings.TrimSuffix(zip, ".zip") + "/"}
		// Legacy bare-stem layout (pre-A1 old-style ids dropped the
		// category): images/<yymm>/<bare-stem>.zip and
		// images/<yymm>/<bare-stem>/.
		if i := strings.IndexByte(p.ArxivID, '/'); i >= 0 {
			bare := fmt.Sprintf("%sv%d", p.ArxivID[i+1:], a.ArxivVersion)
			if shard := paperassets.Shard(bare); shard != "" {
				out = append(out,
					"images/"+shard+"/"+bare+".zip",
					"images/"+shard+"/"+bare+"/",
				)
			}
		}
		return out
	}
	return nil
}

// listAssetImages probes the candidate prefixes in order and returns the
// first non-empty listing. kind is "zip" when the winning prefix names a
// zip object, "dir" for a per-paper directory; when no candidate yields
// objects, kind is "zip" (the canonical write layout) and files is
// empty. A listing error aborts immediately — an incomplete listing must
// never be reported as "no images".
func listAssetImages(ctx context.Context, store objstore.Store, candidates []string) (kind string, files []imageFileJSON, truncated bool, err error) {
	for _, cand := range candidates {
		candKind := "dir"
		if strings.HasSuffix(cand, ".zip") {
			candKind = "zip"
		}
		infos, err := store.ListPrefix(ctx, cand, imagesListMaxFiles+1)
		if err != nil {
			return "", nil, false, fmt.Errorf("list %s: %w", cand, err)
		}
		if len(infos) == 0 {
			continue
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
		truncated = len(infos) > imagesListMaxFiles
		if truncated {
			infos = infos[:imagesListMaxFiles]
		}
		files = make([]imageFileJSON, 0, len(infos))
		for _, info := range infos {
			files = append(files, imageFileJSON{Key: info.Key, Size: info.Size})
		}
		return candKind, files, truncated, nil
	}
	return "zip", []imageFileJSON{}, false, nil
}
