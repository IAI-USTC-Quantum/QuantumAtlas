package routes

// papers_mineru_ingest.go: new-format MinerU ingest wiring (plan §5,
// §4.2, Q1 "接收并保存真实原ZIP/JSON/MD/images及hash清单").
//
// POST /api/papers/{arxiv_id}/upload-mineru now branches on the zip's
// shape:
//
//   - legacy zip (full.md only)  → the historical path (markdown +
//     images-zip into the default_asset layout, unchanged);
//   - NEW format zip (contains middle_json.json) → this file: mint or
//     reuse the paper's SOURCE identity (paper_sources row pinned to
//     the source-PDF sha256), insert an immutable parse REVISION row
//     (artifact sha = sha256(middle.json), tier recorded, is_current
//     flipped transactionally), and store every member of the parse
//     bundle under the revision-scoped key layout
//
//       papers/<qa_>/parses/<revision_id>/middle.json
//       papers/<qa_>/parses/<revision_id>/markdown.md
//       papers/<qa_>/parses/<revision_id>/images/<relname>
//
// "若该源已存在则复用": FindPaperSourceBySHA((paper, sha)) reuses the
// existing source row so re-uploads never mint duplicate identities.
//
// Source-PDF sha provenance (in order): the contributor's pdf_sha256
// query param (already cross-checked against the stored PDF by the
// caller), else the stored PDF's recorded sha, else 400 — the source
// row pins bytes, and pinning an unknown hash would be a lie.
//
// Simplifications recorded per Q0's "执行者自行从简决定" license (all in
// the commit message too):
//   - tier: zip metadata.json {"tier"} → ?tier= query param →
//     'standard'. Real MinerU 4 zips carry no tier metadata today.
//   - The DOI upload variant keeps the legacy path; DOI-sourced parse
//     identity wiring is a TODO pending the §13 layout decision.
//   - The paper must already exist under the arxiv identity; this
//     endpoint never mints a paper (upload-pdf/claim flow owns that).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// mineruIngestCatalog is the registry slice the new-format ingest
// needs. *registry.Store satisfies it; tests fake it (the handler
// signature keeps *registry.Store for the legacy path).
type mineruIngestCatalog interface {
	GetPaperIDByIdentity(ctx context.Context, scheme, id string) (string, bool, error)
	FindPaperSourceBySHA(ctx context.Context, paperID, sha256 string) (registry.PaperSource, bool, error)
	InsertPaperSource(ctx context.Context, src registry.PaperSource) (bool, error)
	InsertParseRevision(ctx context.Context, rev registry.ParseRevision, setCurrent bool) error
}

var _ mineruIngestCatalog = (*registry.Store)(nil)

// ingestMinerUNewFormat runs the new-format branch. Errors are already
// written to re; returns the handler error.
func ingestMinerUNewFormat(
	re *core.RequestEvent,
	store objstore.Store,
	catalog mineruIngestCatalog,
	canonical string,
	zipBytes []byte,
	claimedPDFSha, tierParam, requester, source string,
) error {
	ctx := re.Request.Context()

	res, err := mineru.ExtractNewFormat(zipBytes)
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
	}

	// Validate the artifact BEFORE persisting anything: a zip whose
	// middle JSON fails the profile must not produce a parse revision
	// row (plan Q1: 坏 schema 如实报错).
	doc, err := mineru.ParseMiddleJSON(res.MiddleJSON)
	if err != nil {
		return re.JSON(http.StatusUnprocessableEntity, map[string]string{
			"detail": "middle_json.json rejected: " + err.Error(),
			"schema": mineru.MiddleSchema,
		})
	}

	// Paper must exist under the (bare) arxiv identity.
	paperID, found, err := catalog.GetPaperIDByIdentity(ctx, "arxiv", registry.NormalizeArxivID(canonical))
	if err != nil {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
		})
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": "no paper in the catalog for " + canonical + "; upload the PDF (or mint the paper) before contributing parse output",
		})
	}

	// Source-PDF sha provenance.
	srcSha := claimedPDFSha
	if srcSha == "" {
		srcSha = lookupStoredPDFSha256(ctx, store, canonical)
	}
	if srcSha == "" {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "cannot establish the source-PDF identity: pass ?pdf_sha256=<hex> (the sha of the exact PDF you parsed) or upload the PDF first",
		})
	}

	// Reuse-or-mint the source row.
	srcRow, exists, err := catalog.FindPaperSourceBySHA(ctx, paperID, srcSha)
	if err != nil {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
		})
	}
	sourceMinted := false
	if !exists {
		srcRow = registry.PaperSource{
			PaperID:  paperID,
			SourceID: registry.NewSourceID(), // mint here: InsertPaperSource doesn't return the minted id back
			Origin:   "upload",
			Sha256:   srcSha,
			SizeBytes: func() int64 {
				// Prefer the stored object's size when the PDF is in
				// the store; -1 (unknown) is honest otherwise.
				if _, info, ok, err := paperassets.LocateAssetByID(ctx, store, "pdf", canonical); err == nil && ok && info.Size >= 0 {
					return info.Size
				}
				return -1
			}(),
			ObjstoreKey: func() string {
				if key, _, ok, err := paperassets.LocateAssetByID(ctx, store, "pdf", canonical); err == nil && ok && key != "" {
					return key
				}
				return paperassets.AssetKey("pdf", canonical)
			}(),
		}
		if minted, err := catalog.InsertPaperSource(ctx, srcRow); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "insert paper source: " + err.Error(),
			})
		} else {
			sourceMinted = minted
		}
	}

	// Tier: zip metadata → query param → default.
	tier := res.Tier
	if tier == "" {
		tier = tierParam
	}
	tier = registry.NormalizeParseTier(tier)

	// Persist the parse bundle first, the revision row second: a bundle
	// without a row is an orphaned object (prunable), a row without
	// bytes is a "ready pointer at nothing" — the dishonest failure
	// mode plan §8 Q1 forbids.
	revID := registry.NewParseRevisionID()
	base := fmt.Sprintf("papers/%s/parses/%s", paperID, revID)
	midSum := sha256.Sum256(res.MiddleJSON)
	artifactSha := hex.EncodeToString(midSum[:])

	put := func(key string, b []byte, contentType string) error {
		if _, err := store.Put(ctx, key, strings.NewReader(string(b)), int64(len(b)), contentType); err != nil {
			return fmt.Errorf("store %s: %w", key, err)
		}
		return nil
	}
	middleKey := base + "/middle.json"
	if err := put(middleKey, res.MiddleJSON, "application/json"); err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	mdKey := ""
	if len(res.Markdown) > 0 {
		mdKey = base + "/markdown.md"
		if err := put(mdKey, res.Markdown, "text/markdown; charset=utf-8"); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
	}
	imgKeys := make([]string, 0, len(res.Images))
	for name, b := range res.Images {
		clean := path.Clean("/" + name) // strips traversal, keeps rel shape
		clean = strings.TrimPrefix(clean, "/")
		if clean == "" || strings.Contains(clean, "..") || strings.HasSuffix(clean, "/") {
			continue
		}
		key := base + "/" + clean
		ct := "application/octet-stream"
		switch {
		case strings.HasSuffix(clean, ".jpg"), strings.HasSuffix(clean, ".jpeg"):
			ct = "image/jpeg"
		case strings.HasSuffix(clean, ".png"):
			ct = "image/png"
		}
		if err := put(key, b, ct); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		imgKeys = append(imgKeys, key)
	}

	rev := registry.ParseRevision{
		RevisionID:     revID,
		PaperID:        paperID,
		SourceID:       srcRow.SourceID,
		Schema:         doc.Schema,
		SchemaVersion:  doc.SchemaVersion,
		ArtifactSha256: artifactSha,
		ObjstoreKey:    middleKey,
		Tier:           tier,
	}
	if err := catalog.InsertParseRevision(ctx, rev, true); err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "insert parse revision: " + err.Error(),
		})
	}

	slog.Info("ingested mineru new-format parse",
		"arxiv_id", canonical, "paper_id", paperID,
		"requester", requester, "source", source,
		"source_id", srcRow.SourceID, "source_minted", sourceMinted,
		"revision_id", revID, "tier", tier,
		"artifact_sha256", artifactSha, "middle_key", middleKey,
		"markdown_key", mdKey, "image_count", len(imgKeys),
	)

	body := map[string]any{
		"arxiv_id":        canonical,
		"paper_id":        paperID,
		"source_id":       srcRow.SourceID,
		"source_minted":   sourceMinted,
		"source_sha256":   srcSha,
		"revision_id":     revID,
		"is_current":      true,
		"tier":            tier,
		"schema":          doc.Schema,
		"schema_version":  doc.SchemaVersion,
		"artifact_sha256": artifactSha,
		"objstore_keys": map[string]any{
			"middle_json": middleKey,
			"markdown":    nil,
			"images":      imgKeys,
		},
		"blocks_endpoint": "/api/papers/" + paperID + "/parses/" + revID + "/blocks",
	}
	if mdKey != "" {
		body["objstore_keys"].(map[string]any)["markdown"] = mdKey
	}
	re.Response.WriteHeader(http.StatusCreated)
	return jsonBody(re, body)
}

// normaliseTierParam lowercases/trims the ?tier= query value; empty
// stays empty (default applied later).
func normaliseTierParam(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}
