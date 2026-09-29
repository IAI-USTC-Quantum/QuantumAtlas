package routes

// papers_blocks.go: Q1 block-comments block surface (plan §12.2, §4.2, §5):
//
//	GET /api/papers/{paper_id}/parses/{revision}/blocks
//	      ?page_idx=&cursor=&per_page=      — keyset-paginated top-level blocks
//	GET /api/papers/{paper_id}/parses/{revision}/blocks/{page_idx}/{block_index}
//	                                          — combined read: source+anchor+content+discussions
//	GET /api/papers/{paper_id}/parses/{revision}/blocks/{page_idx}/{block_index}/image
//	                                          — original-PDF render crop (bbox [0,1])
//
// Block identity inside a revision is the ORIGINAL page_idx + block
// index pair (§5.1): indexes may be non-contiguous (1,2,5 → no 3/4),
// lookups are exact (a gap 404s, never a nearest-neighbour fallback),
// and the same visual text under two different parses of one PDF is TWO
// distinct anchors (different revision + index) that never auto-migrate
// (golden-anchors).
//
// TODO(Q2): discussions is a placeholder (empty + discussions_ready:
// false) until the comment domain lands; the combined-read contract
// stays forward-compatible.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pdfraster"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// Pagination bounds per the shared contract (plan §12.2).
const (
	blocksPerPageDefault = 20
	blocksPerPageMax     = 100
)

// loadParseDoc fetches and parses one revision's Middle JSON artifact.
// ok=false means the failure response has ALREADY been written on re
// (catalog down, unknown paper/revision, missing/corrupt bytes, bad
// schema) — the caller just returns. ok=true guarantees doc != nil.
func loadParseDoc(re *core.RequestEvent, store objstore.Store, catalog blockCatalog, requestedID, revisionID string) (rp resolvedPaper, rev registry.ParseRevision, doc *mineru.MiddleDoc, ok bool) {
	ctx := re.Request.Context()
	rp, status, detail := resolveCanonicalPaper(ctx, catalog, requestedID)
	if status != 0 {
		_ = blockResolveError(re, status, detail)
		return rp, rev, nil, false
	}
	applyResolutionHeaders(re.Response, rp.resolution)

	var found bool
	rev, found, err := catalog.GetParseRevision(ctx, rp.canonical, revisionID)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			_ = re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
			return rp, rev, nil, false
		}
		_ = re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return rp, rev, nil, false
	}
	if !found {
		_ = re.JSON(http.StatusNotFound, map[string]string{
			"detail": "no such parse revision " + revisionID + " under paper " + rp.canonical,
		})
		return rp, rev, nil, false
	}
	if store == nil {
		_ = re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "object store not configured on this server",
		})
		return rp, rev, nil, false
	}
	data, err := blockReadVerified(ctx, store, rev.ObjstoreKey, rev.ArtifactSha256)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			_ = re.JSON(http.StatusNotFound, map[string]string{
				"detail": "parse artifact object missing from the store (row exists, bytes absent): " + rev.ObjstoreKey,
			})
			return rp, rev, nil, false
		}
		_ = re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch parse artifact: " + err.Error(),
		})
		return rp, rev, nil, false
	}
	doc, err = mineru.ParseMiddleJSON(data)
	if err != nil {
		switch {
		case errors.Is(err, mineru.ErrNotMiddleJSON),
			errors.Is(err, mineru.ErrUnsupportedVer),
			errors.Is(err, mineru.ErrBadBlocks),
			errors.Is(err, mineru.ErrBadBBox):
			// The stored artifact does not satisfy the profile its row
			// claims — surface it as data corruption, 422-class.
			_ = re.JSON(http.StatusUnprocessableEntity, map[string]string{
				"detail":  "parse artifact rejected: " + err.Error(),
				"schema":  rev.Schema,
				"version": rev.SchemaVersion,
			})
		default:
			_ = re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "decode parse artifact: " + err.Error(),
			})
		}
		return rp, rev, nil, false
	}
	return rp, rev, doc, true
}

// blockBody renders one block for list/combined responses. The block's
// original JSON object rides along verbatim under "raw" so nothing the
// parser doesn't model is lost.
func blockBody(b mineru.MiddleBlock) map[string]any {
	out := map[string]any{
		"page_idx":    b.PageIdx,
		"block_index": b.Index,
		"page_no":     b.PageIdx + 1, // public 1-based page (§5.1)
		"type":        b.Type,
		"content":     json.RawMessage(b.Content),
		"bbox":        nil,
		"has_bbox":    b.BBox != nil,
	}
	if b.BBox != nil {
		out["bbox"] = b.BBox
	}
	if len(b.Raw) > 0 {
		out["raw"] = b.Raw
	}
	return out
}

// paperBlocksListHandler answers the keyset-paginated block listing.
// Order is (page_idx, index) ascending — stable across re-reads of the
// immutable artifact. cursor is the last emitted block's position
// ("p<page>:b<index>"); per_page defaults to 20, max 100.
func paperBlocksListHandler(re *core.RequestEvent, store objstore.Store, catalog blockCatalog, requestedID, revisionID string) error {
	rp, rev, doc, ok := loadParseDoc(re, store, catalog, requestedID, revisionID)
	if !ok {
		return nil
	}
	q := re.Request.URL.Query()

	blocks := doc.OrderedBlocks()

	// page_idx filter (exact, 0-based).
	if v := q.Get("page_idx"); v != "" {
		pageIdx, err := strconv.Atoi(v)
		if err != nil || pageIdx < 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid page_idx (want a non-negative 0-based integer): " + v,
			})
		}
		filtered := blocks[:0:0]
		for _, b := range blocks {
			if b.PageIdx == pageIdx {
				filtered = append(filtered, b)
			}
		}
		blocks = filtered
	}

	// Keyset cursor: skip while (page_idx, index) <= cursor position.
	if c := q.Get("cursor"); c != "" {
		cPage, cIdx, err := mineru.DecodeCursor(c)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid cursor (want the opaque token from next_cursor): " + c,
			})
		}
		filtered := blocks[:0:0]
		for _, b := range blocks {
			if b.PageIdx > cPage || (b.PageIdx == cPage && b.Index > cIdx) {
				filtered = append(filtered, b)
			}
		}
		blocks = filtered
	}

	perPage := blocksPerPageDefault
	if v := q.Get("per_page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid per_page (want a positive integer): " + v,
			})
		}
		if n > blocksPerPageMax {
			n = blocksPerPageMax
		}
		perPage = n
	}

	var nextCursor any
	page := blocks
	if len(blocks) > perPage {
		page = blocks[:perPage]
		last := page[len(page)-1]
		nextCursor = mineru.EncodeCursor(last.PageIdx, last.Index)
	}
	items := make([]map[string]any, 0, len(page))
	for _, b := range page {
		items = append(items, blockBody(b))
	}
	body := map[string]any{
		"paper_id":       rp.canonical,
		"revision_id":    rev.RevisionID,
		"schema":         doc.Schema,
		"schema_version": doc.SchemaVersion,
		"blocks":         items,
		"next_cursor":    nextCursor,
	}
	embedResolutionInBody(body, rp.resolution)
	return re.JSON(http.StatusOK, body)
}

// parsePageBlockSegments parses the {page_idx}/{block_index} path pair:
// page_idx is the ORIGINAL 0-based page index, block_index the 1-based
// public block number from the parse (§5.1).
func parsePageBlockSegments(seg string) (pageIdx, blockIndex int, ok bool) {
	p, b, found := cut2(seg)
	if !found {
		return 0, 0, false
	}
	pageIdx, perr := strconv.Atoi(p)
	blockIndex, berr := strconv.Atoi(b)
	if perr != nil || berr != nil || pageIdx < 0 || blockIndex < 1 {
		return 0, 0, false
	}
	return pageIdx, blockIndex, true
}

func cut2(seg string) (a, b string, ok bool) {
	for i := 0; i < len(seg); i++ {
		if seg[i] == '/' {
			return seg[:i], seg[i+1:], true
		}
	}
	return "", "", false
}

// paperBlockGetHandler answers the combined single-block read:
// source + anchor + content + discussions (+ next_cursor for reply
// pagination, null until Q2 wires discussions).
func paperBlockGetHandler(re *core.RequestEvent, store objstore.Store, catalog blockCatalog, requestedID, revisionID, pageBlockSeg string) error {
	pageIdx, blockIndex, ok := parsePageBlockSegments(pageBlockSeg)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid block path (want /blocks/{page_idx}/{block_index}, page_idx 0-based, block_index the 1-based parse index): %q", pageBlockSeg),
		})
	}
	rp, rev, doc, ok := loadParseDoc(re, store, catalog, requestedID, revisionID)
	if !ok {
		return nil
	}
	block, found := doc.FindBlock(pageIdx, blockIndex)
	if !found {
		// Golden-anchor contract: a non-contiguous index gap or a page
		// the parse lacks 404s — NO nearest-block fallback.
		return re.JSON(http.StatusNotFound, map[string]any{
			"detail": fmt.Sprintf(
				"block not found in revision %s: no top-level block with page_idx=%d index=%d (indexes are exact, gaps 404)",
				rev.RevisionID, pageIdx, blockIndex),
			"revision_id": rev.RevisionID,
			"page_idx":    pageIdx,
			"block_index": blockIndex,
		})
	}
	ctx := re.Request.Context()
	src, found, err := catalog.GetPaperSource(ctx, rp.canonical, rev.SourceID)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if !found {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "parse revision " + rev.RevisionID + " references missing source " + rev.SourceID,
		})
	}

	discussions := []any{}
	nextCursor := any(nil)
	ready := false
	hint := "discussions not configured on this server (Q2 comment API absent); anchor semantics are final"
	if blockDiscussions != nil {
		items, next, derr := blockDiscussions(ctx, rp.canonical, rev.RevisionID, pageIdx, blockIndex, combinedBlockDiscussionsLimit)
		if derr != nil {
			// Combined read degrades honestly: the block itself is
			// readable, the comment store is not — say so instead of
			// failing the whole read (discussions have their own
			// dedicated endpoints for retries).
			hint = "discussions temporarily unavailable: " + derr.Error()
		} else {
			for _, d := range items {
				discussions = append(discussions, discussionJSON(d))
			}
			if next != "" {
				nextCursor = next
			}
			ready = true
			hint = ""
		}
	}

	// Readable locator (plan §5.1): auxiliary reading aid, never
	// identity — §4.2 keeps the anchor on (paper, source, revision,
	// page_idx, block_index). short_id = first 7 hex of the SOURCE PDF
	// sha256; tier comes from the revision row (00009), defaulting to
	// "standard" for rows written before the column existed.
	anchor := map[string]any{
		"paper_id":       rp.canonical,
		"source_id":      src.SourceID,
		"parse_revision": rev.RevisionID,
		"schema":         rev.Schema,
		"schema_version": rev.SchemaVersion,
		"page_idx":       block.PageIdx,
		"block_index":    block.Index,
	}
	tier := rev.Tier
	if tier == "" {
		tier = "standard"
	}
	if sid := mineru.ShortID(src.Sha256); sid != "" {
		anchor["short_id"] = sid
		anchor["tier"] = tier
		anchor["locator"] = mineru.Locator(src.Sha256, tier, block.PageIdx, block.Index)
	}
	body := map[string]any{
		"source": map[string]any{
			"paper_id":         rp.canonical,
			"source_id":        src.SourceID,
			"origin":           src.Origin,
			"pdf_sha256":       src.Sha256,
			"size_bytes":       src.SizeBytes,
			"parse_revision":   rev.RevisionID,
			"artifact_sha256":  rev.ArtifactSha256,
			"schema":           rev.Schema,
			"schema_version":   rev.SchemaVersion,
			"is_current_parse": rev.IsCurrent,
		},
		"anchor":            anchor,
		"content":           blockBody(block),
		"discussions":       discussions,
		"discussions_ready": ready,
		"next_cursor":       nextCursor,
	}
	if hint != "" {
		body["discussions_hint"] = hint
	}
	embedResolutionInBody(body, rp.resolution)
	return re.JSON(http.StatusOK, body)
}

// paperBlockImageHandler answers the block-image crop. The crop is a
// render of the ORIGINAL source PDF page clipped to the block's
// normalized bbox — never a redraw of parsed text (plan §5.2). Honest
// failures:
//
//	no bbox on the block            → 404 reason=no_bbox (never fabricated)
//	source PDF bytes missing        → 404
//	source sha mismatch             → 500 (store corruption)
//	rasterizer not installed        → 503 (config hint)
//	page beyond the source PDF      → 404
func paperBlockImageHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog blockCatalog, requestedID, revisionID, pageBlockSeg string) error {
	pageIdx, blockIndex, ok := parsePageBlockSegments(pageBlockSeg)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid block path (want /blocks/{page_idx}/{block_index}/image): %q", pageBlockSeg),
		})
	}
	rp, rev, doc, ok := loadParseDoc(re, store, catalog, requestedID, revisionID)
	if !ok {
		return nil
	}
	block, found := doc.FindBlock(pageIdx, blockIndex)
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": fmt.Sprintf(
				"block not found in revision %s: no top-level block with page_idx=%d index=%d",
				rev.RevisionID, pageIdx, blockIndex),
		})
	}
	if block.BBox == nil {
		return re.JSON(http.StatusNotFound, map[string]any{
			"detail":      "block image unavailable: the parse output carries no bbox for this block (a crop would be fabricated)",
			"reason":      "no_bbox",
			"revision_id": rev.RevisionID,
			"page_idx":    block.PageIdx,
			"block_index": block.Index,
		})
	}
	ctx := re.Request.Context()
	src, found, err := catalog.GetPaperSource(ctx, rp.canonical, rev.SourceID)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if !found {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "parse revision " + rev.RevisionID + " references missing source " + rev.SourceID,
		})
	}
	pdfBytes, err := blockReadVerified(ctx, store, src.ObjstoreKey, src.Sha256)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "source PDF object missing from the store — cannot render an original-page crop: " + src.ObjstoreKey,
				"reason": "source_missing",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch source pdf: " + err.Error(),
		})
	}

	command := pdfraster.DefaultCommand
	dpi := pdfraster.DefaultDPI
	if cfg != nil {
		if cfg.BlockImageCommand != "" {
			command = cfg.BlockImageCommand
		}
		if cfg.BlockImageDPI > 0 {
			dpi = cfg.BlockImageDPI
		}
	}
	crop, err := pdfraster.RenderCropPNG(ctx, pdfBytes, block.PageIdx,
		[4]float64{block.BBox[0], block.BBox[1], block.BBox[2], block.BBox[3]}, dpi, command)
	if err != nil {
		switch {
		case errors.Is(err, pdfraster.ErrRendererUnavailable):
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "block image unavailable: no PDF rasterizer on this server (install poppler-utils or set paper_access.block_image_command)",
				"reason": "renderer_unavailable",
			})
		case errors.Is(err, pdfraster.ErrPageOutOfRange):
			return re.JSON(http.StatusNotFound, map[string]any{
				"detail":   "block image unavailable: the source PDF has no such page (parse and source disagree)",
				"reason":   "page_out_of_range",
				"page_idx": block.PageIdx,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "render block crop: " + err.Error(),
		})
	}

	// Deterministic ETag over the crop inputs: source sha + page +
	// bbox + dpi. Crops of an immutable source at fixed settings are
	// immutable too.
	etagInput := fmt.Sprintf("%s|%d|%v|%d", src.Sha256, block.PageIdx, block.BBox, dpi)
	etag := blockImageETag(etagInput)
	re.Response.Header().Set("ETag", `"`+etag+`"`)
	re.Response.Header().Set("X-QAtlas-Source-Sha256", src.Sha256)
	re.Response.Header().Set("Cache-Control", "private, max-age=86400")
	return re.Blob(http.StatusOK, "image/png", crop)
}

// blockImageETag hashes the crop inputs (source sha + page + bbox +
// dpi) into a deterministic ETag. Distinct from the test helper
// sha256Hex in stagedupload_test.go.
func blockImageETag(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
