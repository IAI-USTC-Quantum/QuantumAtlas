package mineru

// middlejson.go: reader for the MinerU "Middle JSON" parse artifact —
// the raw structured model with `schema=docvortex.middle` /
// `schema_version=2.0` (plan §5). This is the artifact Q1 anchors block
// comments to; block identity inside one artifact is the ORIGINAL
// `page_idx` + top-level `block.index` pair (plan §4.2 / §5.1):
//
//   - page_idx is 0-based; the public locator page number is page_idx+1.
//   - index is the block's own 1-based public number from the parse.
//     Indexes may be non-contiguous (1, 2, 5, ...) and MUST be looked up
//     by exact value — never by array position, never "nearest".
//   - bbox, when present, is [x0, y0, x1, y1] NORMALIZED to [0,1]
//     fractions of the page width/height (new Middle JSON profile).
//     Legacy ContentList [0,1000] coordinates are NOT supported here
//     (plan §5.1) — a bbox outside [0,1] is rejected, never rescaled.
//
// Unknown keys (including the fixtures' "_synthetic" markers) are
// ignored so forward-compatible minor revisions still parse.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MiddleSchema is the only schema value ParseMiddleJSON accepts.
const MiddleSchema = "docvortex.middle"

// MiddleSchemaVersion is the only schema_version value ParseMiddleJSON
// accepts. Bumping this is a deliberate contract change (new profile).
const MiddleSchemaVersion = "2.0"

// Errors surfaced by ParseMiddleJSON. Callers map them to 4xx/5xx:
// ErrNotMiddleJSON / ErrUnsupportedSchema / ErrBadBlocks are data
// problems (4xx-class); everything else is a decode failure.
var (
	ErrNotMiddleJSON  = errors.New("mineru: artifact is not docvortex.middle JSON")
	ErrUnsupportedVer = errors.New("mineru: unsupported docvortex.middle schema_version")
	ErrBadBlocks      = errors.New("mineru: middle JSON has invalid blocks array")
	ErrBadBBox        = errors.New("mineru: block bbox invalid (want [0,1]-normalized [x0,y0,x1,y1])")
	ErrBlockNotFound  = errors.New("mineru: block not found")
	ErrInvalidCursor  = errors.New("mineru: invalid block cursor")
)

// MiddleBlock is one top-level block of a Middle JSON artifact. Content
// is kept verbatim (json.RawMessage) so re-serialization never rewrites
// the parsed value — string contents round-trip byte-for-byte and
// non-string contents stay structured. BBox is nil when the parse
// carried no bbox for this block (image crops then answer "unavailable",
// never a fabricated crop — plan §5.2).
type MiddleBlock struct {
	PageIdx int
	Index   int
	Type    string
	Content json.RawMessage
	BBox    []float64 // nil or [4]float64 in [0,1]
	// Raw is the complete original JSON object of the block.
	Raw json.RawMessage
	// RenderRaw is a derived native-profile view held ONLY in memory. Raw and
	// the persisted artifact always remain producer-original bytes.
	RenderRaw   json.RawMessage
	NativePath  string
	NativeIndex json.RawMessage
}

func (b MiddleBlock) RenderJSON() json.RawMessage {
	if len(b.RenderRaw) != 0 {
		return b.RenderRaw
	}
	return b.Raw
}

// ContentText returns the content as a plain string when it is a JSON
// string (equation blocks), or the concatenated span texts when it is
// the real-MinerU rich-content array of {type, content} objects (text
// blocks); "" otherwise.
func (b MiddleBlock) ContentText() string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		return s
	}
	var spans []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b.Content, &spans); err == nil {
		var sb strings.Builder
		for _, sp := range spans {
			sb.WriteString(sp.Content)
		}
		return sb.String()
	}
	return ""
}

// MiddleDoc is a parsed Middle JSON artifact.
type MiddleDoc struct {
	Schema                string
	SchemaVersion         string
	Pages                 int // pdf_info.pages when present
	PageSize              [][2]float64
	Blocks                []MiddleBlock
	ProducerVersion       string
	NormalizationWarnings []string
}

type middleJSON struct {
	Schema        string `json:"schema"`
	SchemaVersion string `json:"schema_version"`
	PDFInfo       *struct {
		Pages    int          `json:"pages"`
		PageSize [][2]float64 `json:"page_size"`
	} `json:"pdf_info"`
	// Legacy/synthetic layout: flat top-level blocks array where every
	// block carries its own page_idx and a 1-based public index.
	Blocks []json.RawMessage `json:"blocks"`
	// Real MinerU 4.x layout (verified against a genuine result zip,
	// 2026-09-29): pages[] each with page_idx + blocks[]; the blocks
	// carry NO page_idx of their own and their index is 0-based, as a
	// JSON string OR number — the producer mixes both shapes.
	Pages []middlePageJSON `json:"pages"`
}

type middlePageJSON struct {
	PageIdx int               `json:"page_idx"`
	Blocks  []json.RawMessage `json:"blocks"`
}

// middleBlockJSON accepts both index spellings (int for the synthetic
// fixtures, int-or-string for real MinerU 4 output).
type middleBlockJSON struct {
	PageIdx *int            `json:"page_idx"`
	Index   json.RawMessage `json:"index"`
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	BBox    []float64       `json:"bbox"`
}

// decodeBlockIndex parses a block index that may be a JSON number or a
// JSON string holding a decimal (real MinerU 4 writes "0", "1", ...).
// ok=false on any other shape.
func decodeBlockIndex(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parsed, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// ParseMiddleJSON decodes and validates one Middle JSON artifact.
// The schema / schema_version gate is strict: a mismatched artifact
// must never be silently interpreted as blocks (plan §8 Q1: "坏schema
// 如实报错").
//
// Two block layouts are accepted and NORMALIZED to one internal model
// where Index is the 1-based public block number (§5.1):
//
//   - top-level "blocks" (synthetic fixtures / Q0 golden anchors):
//     blocks carry page_idx and an already-1-based index — used as-is;
//   - top-level "pages[].blocks" (real MinerU 4.x output): blocks
//     inherit their page's page_idx and carry a 0-based index — +1 on
//     ingest. TODO(real-profile): pin this against more real samples;
//     if a future MinerU ships 1-based page-block indexes the +1 must
//     be keyed off the producer version in metadata.
func ParseMiddleJSON(data []byte) (*MiddleDoc, error) {
	// Identify the envelope BEFORE decoding DocVortex's object pdf_info.
	// An explicit wrong schema never falls through to another profile.
	var envelope struct {
		Schema  string          `json:"schema"`
		PDFInfo json.RawMessage `json:"pdf_info"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("mineru: decode middle json: %w", err)
	}
	if envelope.Schema == "" && strings.HasPrefix(strings.TrimSpace(string(envelope.PDFInfo)), "[") {
		return parseNativeMiddle(data)
	}
	var raw middleJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mineru: decode middle json: %w", err)
	}
	if raw.Schema != MiddleSchema {
		return nil, fmt.Errorf("%w: schema=%q", ErrNotMiddleJSON, raw.Schema)
	}
	if raw.SchemaVersion != MiddleSchemaVersion {
		return nil, fmt.Errorf("%w: schema_version=%q (supported: %s)",
			ErrUnsupportedVer, raw.SchemaVersion, MiddleSchemaVersion)
	}
	doc := &MiddleDoc{
		Schema:        raw.Schema,
		SchemaVersion: raw.SchemaVersion,
	}
	if raw.PDFInfo != nil {
		doc.Pages = raw.PDFInfo.Pages
		doc.PageSize = raw.PDFInfo.PageSize
	}

	switch {
	case raw.Blocks != nil:
		for i, braw := range raw.Blocks {
			var b middleBlockJSON
			if err := json.Unmarshal(braw, &b); err != nil {
				return nil, fmt.Errorf("%w: block[%d] decode: %v", ErrBadBlocks, i, err)
			}
			if b.PageIdx == nil {
				return nil, fmt.Errorf("%w: block[%d] missing page_idx", ErrBadBlocks, i)
			}
			idx, ok := decodeBlockIndex(b.Index)
			if !ok {
				return nil, fmt.Errorf("%w: block[%d] missing/invalid index", ErrBadBlocks, i)
			}
			if idx < 1 {
				return nil, fmt.Errorf("%w: block[%d] index %d < 1 (public index is 1-based)", ErrBadBlocks, i, idx)
			}
			if err := validateMiddleBlock(i, &b); err != nil {
				return nil, err
			}
			doc.Blocks = append(doc.Blocks, MiddleBlock{
				PageIdx: *b.PageIdx,
				Index:   idx,
				Type:    b.Type,
				Content: b.Content,
				BBox:    b.BBox,
				Raw:     append(json.RawMessage(nil), braw...),
			})
		}
	case raw.Pages != nil:
		if doc.Pages == 0 {
			doc.Pages = len(raw.Pages)
		}
		for pi, page := range raw.Pages {
			for i, braw := range page.Blocks {
				var b middleBlockJSON
				if err := json.Unmarshal(braw, &b); err != nil {
					return nil, fmt.Errorf("%w: page[%d].block[%d] decode: %v", ErrBadBlocks, pi, i, err)
				}
				idx, ok := decodeBlockIndex(b.Index)
				if !ok {
					return nil, fmt.Errorf("%w: page[%d].block[%d] missing/invalid index", ErrBadBlocks, pi, i)
				}
				// Real profile is 0-based; normalize to the 1-based
				// public number every downstream contract speaks.
				idx++
				if err := validateMiddleBlock(i, &b); err != nil {
					return nil, err
				}
				p := page.PageIdx
				doc.Blocks = append(doc.Blocks, MiddleBlock{
					PageIdx: p,
					Index:   idx,
					Type:    b.Type,
					Content: b.Content,
					BBox:    b.BBox,
					Raw:     append(json.RawMessage(nil), braw...),
				})
			}
		}
	default:
		return nil, fmt.Errorf("%w: missing blocks array", ErrBadBlocks)
	}
	return doc, nil
}

// validateMiddleBlock applies the per-block invariants shared by both
// layouts (bbox shape + [0,1] range).
func validateMiddleBlock(i int, b *middleBlockJSON) error {
	if b.BBox == nil {
		return nil
	}
	if len(b.BBox) != 4 {
		return fmt.Errorf("%w: block[%d] has %d bbox coords", ErrBadBBox, i, len(b.BBox))
	}
	for _, v := range b.BBox {
		// Small epsilon for float round-trips at the edges.
		if v < -1e-9 || v > 1+1e-9 {
			return fmt.Errorf("%w: block[%d] coord %v outside [0,1] (legacy [0,1000] profile is not supported)", ErrBadBBox, i, v)
		}
	}
	if b.BBox[2] < b.BBox[0] || b.BBox[3] < b.BBox[1] {
		return fmt.Errorf("%w: block[%d] x1<y0 or y1<y0", ErrBadBBox, i)
	}
	return nil
}

// FindBlock returns the block with EXACTLY the given (page_idx, block
// index) — a non-contiguous index gap (1,2,5 → no 3 or 4) answers
// found=false; callers MUST 404, never fall back to a neighbour block
// (golden-anchors case "parse-A page 1 block 3/4").
func (d *MiddleDoc) FindBlock(pageIdx, index int) (MiddleBlock, bool) {
	for _, b := range d.Blocks {
		if b.PageIdx == pageIdx && b.Index == index {
			return b, true
		}
	}
	return MiddleBlock{}, false
}

// OrderedBlocks returns all blocks sorted by (page_idx, index) — the
// stable order the keyset-paginated listing serves.
func (d *MiddleDoc) OrderedBlocks() []MiddleBlock {
	out := append([]MiddleBlock(nil), d.Blocks...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PageIdx != out[j].PageIdx {
			return out[i].PageIdx < out[j].PageIdx
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// EncodeCursor renders the keyset cursor for "the block after
// (pageIdx, index)".
func EncodeCursor(pageIdx, index int) string {
	return fmt.Sprintf("p%d:b%d", pageIdx, index)
}

// ShortIDLen is the fixed width of a locator's doc component: the
// first 7 hex chars of the SOURCE PDF sha256 (plan §5.1).
const ShortIDLen = 7

// ShortID returns the locator doc component for a source sha256: the
// first 7 lowercase hex characters. §5.1 says MinerU widens short_id on
// collision and persists the widened value; qatlas pins a fixed 7 chars
// (28 bits — collision odds are ~2^-28 per pair of distinct sources
// under one paper) and TODO(deferred): revisit widening when a paper
// carries enough distinct sources for a collision to be observable.
// Empty or malformed input yields "" — callers skip the locator rather
// than emit a fabricated id.
func ShortID(sourceSha256 string) string {
	s := strings.ToLower(strings.TrimSpace(sourceSha256))
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
		if !ok {
			return ""
		}
	}
	if len(s) < ShortIDLen {
		return ""
	}
	return s[:ShortIDLen]
}

// Locator renders the readable MinerU-style locator string (plan §5.1):
//
//	doc:<short_id>/tier:<tier>/page:<page_no>/block:<block_no>
//
// page_no is the PUBLIC 1-based page (pageIdx+1); block_no is the
// parse's own 1-based block index. The locator is an auxiliary reading
// aid only — §4.2 keeps identity on (paper, source, revision, page_idx,
// block_index); §5.1 notes the locator deliberately omits the parse id,
// so same-PDF-same-tier re-parses share a locator while pointing at
// different revisions. An invalid shortID yields "" (caller omits the
// field rather than lying).
func Locator(shortID, tier string, pageIdx, blockIndex int) string {
	if ShortID(shortID) == "" {
		return ""
	}
	tier = strings.TrimSpace(tier)
	if tier == "" {
		tier = "standard"
	}
	return fmt.Sprintf("doc:%s/tier:%s/page:%d/block:%d", ShortID(shortID), tier, pageIdx+1, blockIndex)
}

// DecodeCursor parses a keyset cursor produced by EncodeCursor.
func DecodeCursor(s string) (pageIdx, index int, err error) {
	p, rest, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	b, ok := strings.CutPrefix(rest, "b")
	if !ok {
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	p = strings.TrimPrefix(p, "p")
	pageIdx, perr := strconv.Atoi(p)
	index, ierr := strconv.Atoi(b)
	if perr != nil || ierr != nil {
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	return pageIdx, index, nil
}
