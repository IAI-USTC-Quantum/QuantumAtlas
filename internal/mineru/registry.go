package mineru

// registry.go: glue between the converter and the PostgreSQL paper
// registry — the shared markdown write-through plus the bucket-relative
// key convention paper_assets stores.

import (
	"context"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// upsertMDWriteThrough records a converted MinerU markdown bundle for
// canonical (a versioned arXiv id) in the registry: resolve-or-mint the
// paper (the PDF write-through usually minted it already), then upsert
// the asset's md/json pointers + image count. sha/size are part of the
// caller contract and, as in the legacy catalog, are not persisted on
// the md row.
func (c *Converter) upsertMDWriteThrough(ctx context.Context, canonical, mdSha string, mdSize int64, imageCount int) error {
	paperID, _, err := c.catalog.ResolveOrMint(ctx, registry.PaperRef{ArxivID: canonical})
	if err != nil {
		return err
	}
	mdPath := bucketRelKey(paperassets.AssetKey("markdown", canonical))
	jsonPath := bucketRelKey(paperassets.AssetKey("json", canonical))
	return c.catalog.UpsertMD(ctx, paperID,
		registry.ArxivVersionOf(canonical), mdSha, mdSize, mdPath, jsonPath, imageCount)
}

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
