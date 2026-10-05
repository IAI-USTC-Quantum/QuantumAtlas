// Package paperread builds deterministic, resumable Markdown reading windows
// from immutable MinerU Middle JSON. It performs no inference, network access,
// or artifact writes. Its renderer is a versioned QAtlas implementation of
// MinerU/DocVortex single-block reading semantics, not a Python renderer bridge.
package paperread

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
)

const (
	DefaultLimit = 30000
	MaxLimit     = 100000
	// RendererVersion must change whenever rendered text or cursor positioning changes.
	RendererVersion = "qatlas-middle-markdown-v1"
)

var (
	ErrInvalidRequest   = errors.New("paperread: invalid request")
	ErrInvalidCursor    = errors.New("paperread: invalid cursor")
	ErrCursorMismatch   = errors.New("paperread: cursor pin does not match requested artifact or renderer")
	ErrNotFound         = errors.New("paperread: page or block not found")
	ErrUnsupportedBlock = errors.New("paperread: unsupported block")
)

// Options selects an immutable artifact and an optional exact page/block scope.
// Public Page and Block are 1-based (0 means unspecified); block numbers are
// producer indexes, not array positions. For cursor-only requests, first call
// PeekCursor to choose the pinned revision BEFORE fetching Middle JSON. Read
// then validates every pin. A zero Limit inherits the cursor limit or defaults
// to DefaultLimit. Explicit incompatible page/block/limit selectors are errors.
// ImageURL receives the original safe relative member path, including any
// images/ prefix or nested directories. It should return a stable per-revision
// download URL; changing it invalidates existing render-bound cursors.
// The callback must be deterministic and must not perform network I/O.
type Options struct {
	PaperID, SourceID, Revision string
	SourceSHA256, BundleSHA256  string
	Tier                        string
	Page, Block, Limit          int
	Cursor                      string
	ImageURL                    func(member string) string
}

type RequestScope struct {
	Page   int    `json:"page,omitempty"`
	Block  int    `json:"block,omitempty"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
}

// ContentRange addresses actual producer blocks. Start/End use native-style
// locators and optional rune offsets in rendered Markdown (not source text).
// Offsets are half-open: StartOffset inclusive, EndOffset exclusive. A window
// may split syntax inside a very large block; concatenating successive Content
// strings reconstructs the selected canonical Markdown exactly, with no added
// separators/markers on continuation. These explicit fields disambiguate that
// from a fully rendered, independently parseable Markdown document per window.
type ContentRange struct {
	Page        int    `json:"page"`
	Block       int    `json:"block"`
	Start       string `json:"start"`
	End         string `json:"end"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
}

type NextRequest struct {
	Cursor string `json:"cursor"`
}

type Response struct {
	PaperID        string         `json:"paper_id"`
	SourceID       string         `json:"source_id"`
	Revision       string         `json:"revision"`
	SourceSHA256   string         `json:"source_sha256"`
	ArtifactSHA256 string         `json:"artifact_sha256"`
	BundleSHA256   string         `json:"bundle_sha256,omitempty"`
	Renderer       string         `json:"renderer"`
	Format         string         `json:"format"`
	Content        string         `json:"content"`
	RequestScope   RequestScope   `json:"request_scope"`
	ContentRanges  []ContentRange `json:"content_ranges"`
	Truncated      bool           `json:"truncated"`
	NextRequest    *NextRequest   `json:"next_request"`
	Warnings       []string       `json:"warnings,omitempty"`
}

// CursorIdentity is routing metadata, NOT proof of authorization. Routes must
// authorize the paper/source/revision from these pins independently. PeekCursor
// validates syntax/version/shape; Read verifies the loaded artifact and renderer.
type CursorIdentity struct {
	PaperID  string `json:"paper_id"`
	SourceID string `json:"source_id"`
	Revision string `json:"revision"`
	Renderer string `json:"renderer"`
}

type readCursor struct {
	Version int `json:"v"`
	CursorIdentity
	SourceSHA256   string `json:"source_sha256"`
	BundleSHA256   string `json:"bundle_sha256,omitempty"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	RenderSHA256   string `json:"render_sha256"`
	Tier           string `json:"tier"`
	ScopePage      int    `json:"scope_page"`
	ScopeBlock     int    `json:"scope_block"`
	Limit          int    `json:"limit"`
	PositionPage   int    `json:"position_page"`
	PositionBlock  int    `json:"position_block"`
	Offset         int    `json:"offset"`
}

type renderedBlock struct {
	page, block int
	prefixLen   int
	text        []rune
}

func PeekCursor(token string) (CursorIdentity, error) {
	c, err := decodeCursor(token)
	if err != nil {
		return CursorIdentity{}, err
	}
	return c.CursorIdentity, nil
}

func DecodeCursorIdentity(token string) (paperID, sourceID, revision string, err error) {
	pin, err := PeekCursor(token)
	return pin.PaperID, pin.SourceID, pin.Revision, err
}

func decodeCursor(token string) (readCursor, error) {
	var c readCursor
	if len(token) == 0 || len(token) > 4096 {
		return c, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return c, ErrInvalidCursor
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, ErrInvalidCursor
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, ErrInvalidCursor
	}
	if c.Version != 1 || !validIdentity(c.PaperID) || !validIdentity(c.SourceID) || !validIdentity(c.Revision) ||
		c.Renderer == "" || c.ScopePage < 0 || c.ScopeBlock < 0 || (c.ScopeBlock > 0 && c.ScopePage == 0) ||
		c.PositionPage < 1 || c.PositionBlock < 1 || c.Offset < 0 || c.Limit < 1 || c.Limit > MaxLimit ||
		!validHash(c.SourceSHA256) || !validHash(c.ArtifactSHA256) || !validHash(c.RenderSHA256) ||
		(c.BundleSHA256 != "" && !validHash(c.BundleSHA256)) || c.Tier == "" {
		return c, ErrInvalidCursor
	}
	return c, nil
}

func validIdentity(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 256 }
func validHash(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func validTier(tier string) bool {
	return tier == "flash" || tier == "basic" || tier == "standard" || tier == "advanced"
}

// Read renders and slices one pinned document. Page > 0 selects exactly that
// page; Block > 0 additionally selects its exact public block index. A cursor
// resumes within the original selection, never silently switches revisions.
// Empty pages return empty content and no invented block range.
func Read(middleJSON []byte, opts Options) (*Response, error) {
	if !validIdentity(opts.PaperID) || !validIdentity(opts.SourceID) || !validIdentity(opts.Revision) ||
		!validHash(opts.SourceSHA256) || (opts.BundleSHA256 != "" && !validHash(opts.BundleSHA256)) ||
		opts.Page < 0 || opts.Block < 0 || opts.Limit < 0 || opts.Limit > MaxLimit {
		return nil, ErrInvalidRequest
	}
	artifactHash := digest(middleJSON)
	var cursor *readCursor
	if opts.Cursor != "" {
		c, err := decodeCursor(opts.Cursor)
		if err != nil {
			return nil, err
		}
		if c.PaperID != opts.PaperID || c.SourceID != opts.SourceID || c.Revision != opts.Revision ||
			c.Renderer != RendererVersion || c.SourceSHA256 != opts.SourceSHA256 ||
			c.BundleSHA256 != opts.BundleSHA256 || c.ArtifactSHA256 != artifactHash ||
			(opts.Tier != "" && opts.Tier != c.Tier) ||
			(opts.Page != 0 && opts.Page != c.ScopePage) || (opts.Block != 0 && opts.Block != c.ScopeBlock) ||
			(opts.Limit != 0 && opts.Limit != c.Limit) {
			return nil, ErrCursorMismatch
		}
		opts.Page, opts.Block, opts.Limit, opts.Tier = c.ScopePage, c.ScopeBlock, c.Limit, c.Tier
		cursor = &c
	}
	if opts.Block > 0 && opts.Page == 0 {
		return nil, ErrInvalidRequest
	}
	if opts.Limit == 0 {
		opts.Limit = DefaultLimit
	}
	if opts.Tier == "" {
		opts.Tier = middleTier(middleJSON)
	}
	if !validTier(opts.Tier) || !utf8.Valid(middleJSON) {
		return nil, ErrInvalidRequest
	}
	doc, err := mineru.ParseMiddleJSON(middleJSON)
	if err != nil {
		return nil, err
	}
	blocks := doc.OrderedBlocks()
	seen := make(map[[2]int]bool, len(blocks))
	pageNumbers, err := artifactPages(middleJSON, blocks)
	if err != nil {
		return nil, err
	}
	pageExists := false
	blockExists := false
	for _, p := range pageNumbers {
		if p == opts.Page {
			pageExists = true
		}
	}
	for _, b := range blocks {
		key := [2]int{b.PageIdx, b.Index}
		if b.PageIdx < 0 || b.Index < 1 || seen[key] {
			return nil, fmt.Errorf("%w: duplicate or invalid block identity", mineru.ErrBadBlocks)
		}
		seen[key] = true
		if b.PageIdx+1 == opts.Page && b.Index == opts.Block {
			blockExists = true
		}
	}
	if opts.Page > 0 && !pageExists || opts.Block > 0 && !blockExists {
		return nil, ErrNotFound
	}
	r := &markdownRenderer{imageURL: opts.ImageURL}
	segments := make([]renderedBlock, 0, len(blocks))
	previousPage := -1
	for _, b := range blocks {
		page := b.PageIdx + 1
		if opts.Page > 0 && page != opts.Page || opts.Block > 0 && b.Index != opts.Block {
			continue
		}
		text := r.block(b.Raw, 0)
		if strings.TrimSpace(text) == "" {
			continue
		}
		prefix := ""
		if len(segments) > 0 {
			prefix = "\n\n"
		}
		if page != previousPage {
			prefix += fmt.Sprintf("<!-- page %d -->\n\n", page)
		}
		segments = append(segments, renderedBlock{page: page, block: b.Index, prefixLen: utf8.RuneCountInString(prefix), text: []rune(prefix + text)})
		previousPage = page
	}
	response := &Response{
		PaperID: opts.PaperID, SourceID: opts.SourceID, Revision: opts.Revision,
		SourceSHA256: opts.SourceSHA256, ArtifactSHA256: artifactHash, BundleSHA256: opts.BundleSHA256,
		Renderer: RendererVersion, Format: "markdown", ContentRanges: []ContentRange{},
		RequestScope: RequestScope{Page: opts.Page, Block: opts.Block, Limit: opts.Limit, Cursor: opts.Cursor},
		Warnings:     r.warnings,
	}
	if len(segments) == 0 {
		if cursor != nil {
			return nil, ErrInvalidCursor
		}
		response.Warnings = append(response.Warnings, "No renderable blocks in requested scope.")
		return response, nil
	}
	var full strings.Builder
	for _, seg := range segments {
		full.WriteString(string(seg.text))
	}
	renderHash := digest([]byte(full.String()))
	start, offset := 0, 0
	if cursor != nil {
		if cursor.RenderSHA256 != renderHash {
			return nil, ErrCursorMismatch
		}
		start = -1
		for i, seg := range segments {
			if seg.page == cursor.PositionPage && seg.block == cursor.PositionBlock {
				start = i
				break
			}
		}
		if start < 0 || cursor.Offset >= len(segments[start].text) {
			return nil, ErrInvalidCursor
		}
		offset = cursor.Offset
	}
	remaining := opts.Limit
	var out strings.Builder
	for i := start; i < len(segments); i++ {
		seg := segments[i]
		from := 0
		if i == start {
			from = offset
		}
		available := len(seg.text) - from
		// Preserve whole block boundaries whenever another complete block has
		// already fit. A single oversized segment is split at Unicode boundaries.
		if available > remaining && out.Len() > 0 {
			response.NextRequest = makeNext(opts, artifactHash, renderHash, seg, from)
			break
		}
		count := available
		if count > remaining {
			count = softCut(seg.text[from:], remaining)
		}
		to := from + count
		out.WriteString(string(seg.text[from:to]))
		locator := mineru.Locator(mineru.ShortID(opts.SourceSHA256), opts.Tier, seg.page-1, seg.block)
		if to > seg.prefixLen {
			startOffset, endOffset := max(0, from-seg.prefixLen), to-seg.prefixLen
			response.ContentRanges = append(response.ContentRanges, ContentRange{
				Page: seg.page, Block: seg.block, Start: locatorOffset(locator, startOffset), End: locatorOffset(locator, endOffset),
				StartOffset: startOffset, EndOffset: endOffset,
			})
		}
		remaining -= count
		if to < len(seg.text) {
			response.NextRequest = makeNext(opts, artifactHash, renderHash, seg, to)
			break
		}
		if remaining == 0 && i+1 < len(segments) {
			response.NextRequest = makeNext(opts, artifactHash, renderHash, segments[i+1], 0)
			break
		}
	}
	response.Content = out.String()
	response.Truncated = response.NextRequest != nil
	return response, nil
}

func makeNext(opts Options, artifactHash, renderHash string, seg renderedBlock, offset int) *NextRequest {
	c := readCursor{
		Version: 1, CursorIdentity: CursorIdentity{opts.PaperID, opts.SourceID, opts.Revision, RendererVersion},
		SourceSHA256: opts.SourceSHA256, BundleSHA256: opts.BundleSHA256,
		ArtifactSHA256: artifactHash, RenderSHA256: renderHash, Tier: opts.Tier,
		ScopePage: opts.Page, ScopeBlock: opts.Block, Limit: opts.Limit,
		PositionPage: seg.page, PositionBlock: seg.block, Offset: offset,
	}
	data, _ := json.Marshal(c)
	return &NextRequest{Cursor: base64.RawURLEncoding.EncodeToString(data)}
}
func locatorOffset(locator string, offset int) string {
	if offset == 0 {
		return locator
	}
	return locator + "/char:" + strconv.Itoa(offset)
}
func softCut(text []rune, limit int) int {
	if len(text) <= limit {
		return len(text)
	}
	// Prefer a paragraph/line/word boundary in the latter half of the budget.
	for _, delimiter := range []string{"\n\n", "\n", "。", ". ", " "} {
		window := string(text[:limit])
		if pos := strings.LastIndex(window, delimiter); pos >= 0 {
			cut := utf8.RuneCountInString(window[:pos]) + utf8.RuneCountInString(delimiter)
			if cut > limit/2 {
				return cut
			}
		}
	}
	return limit
}
func middleTier(data []byte) string {
	var raw struct {
		Extensions struct {
			MinerU struct {
				Tier string `json:"tier"`
			} `json:"mineru"`
		} `json:"extensions"`
	}
	_ = json.Unmarshal(data, &raw)
	if tier := strings.TrimSpace(raw.Extensions.MinerU.Tier); tier != "" {
		return tier
	}
	return "standard"
}
func artifactPages(data []byte, blocks []mineru.MiddleBlock) ([]int, error) {
	var raw struct {
		Pages []struct {
			PageIdx *int `json:"page_idx"`
		} `json:"pages"`
	}
	_ = json.Unmarshal(data, &raw)
	set := make(map[int]bool)
	for _, p := range raw.Pages {
		if p.PageIdx == nil || *p.PageIdx < 0 || set[*p.PageIdx+1] {
			return nil, fmt.Errorf("%w: duplicate or invalid page identity", mineru.ErrBadBlocks)
		}
		set[*p.PageIdx+1] = true
	}
	for _, b := range blocks {
		set[b.PageIdx+1] = true
	}
	pages := make([]int, 0, len(set))
	for p := range set {
		pages = append(pages, p)
	}
	sort.Ints(pages)
	return pages, nil
}
