package routes

// registry_helpers.go: small glue between the HTTP layer and
// internal/registry — id-form conversions and the shared MinerU-markdown
// write-through used by both the upload handler and (indirectly, via the
// same registry calls) the server-side converter.

import (
	"context"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// bucketRelKey strips the leading "<kind>/" segment from an AssetKey,
// yielding the object key relative to a per-kind bucket, e.g.
// "pdf/9508/9508027.pdf" -> "9508/9508027.pdf" — the form
// paper_assets stores.
func bucketRelKey(assetKey string) string {
	if i := strings.IndexByte(assetKey, '/'); i >= 0 {
		return assetKey[i+1:]
	}
	return assetKey
}

// arxivVersionedURL returns the arxiv.org PDF URL WITH its version
// suffix preserved, e.g. "https://arxiv.org/pdf/2401.12345v1". Used by
// the mineru-claim contract: contributors must fetch the exact version
// our catalog references so the sha256 verification on upload-mineru
// succeeds. arXiv treats version URLs as immutable — once "v1" is
// published its bytes never change, even if v2 supersedes it — which
// is what makes "ship arxiv URL + sha256 to the contributor" safe.
func arxivVersionedURL(arxivID string) string {
	id := paperassets.NormalizeIdentifier(arxivID)
	return "https://arxiv.org/pdf/" + id
}

// upsertMDWriteThrough records a MinerU markdown bundle for canonical
// (a versioned arXiv id) in the registry: resolve-or-mint the paper
// (markdown can arrive before any PDF write-through), then upsert the
// asset's md/json pointers + image count. sha/size are part of the
// verification contract (already checked server-side) and, as in the
// legacy catalog, are not persisted on the md row.
func upsertMDWriteThrough(ctx context.Context, catalog *registry.Store, canonical, mdSha string, mdSize int64, imageCount int) error {
	paperID, _, err := catalog.ResolveOrMint(ctx, registry.PaperRef{ArxivID: canonical})
	if err != nil {
		return err
	}
	mdPath := bucketRelKey(paperassets.AssetKey("markdown", canonical))
	jsonPath := bucketRelKey(paperassets.AssetKey("json", canonical))
	return catalog.UpsertMD(ctx, paperID,
		registry.ArxivVersionOf(canonical), mdSha, mdSize, mdPath, jsonPath, imageCount)
}
