package mineru

// Native MinerU V1 standard/hybrid output uses pdf_info[] with final
// para_blocks, native lines/spans and page-unit boxes, not DocVortex JSON.
// This adapter builds a read-only in-memory view. It never rewrites artifacts,
// substitutes ContentList, runs inference, clamps boxes or invents anchors.
import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

const NativeMiddleSchema = "mineru.native.middle"
const NativeMiddleSchemaVersion = "pdf_info-v1"

func IsSupportedMiddleProfile(schema, version string) bool {
	return registry.SupportedParseProfile(schema, version)
}
func SupportedMiddleProfile(schema, version string) bool {
	return IsSupportedMiddleProfile(schema, version)
}

type nativePage struct {
	PageIdx   *int              `json:"page_idx"`
	PageSize  []float64         `json:"page_size"`
	Para      []json.RawMessage `json:"para_blocks"`
	Preproc   []json.RawMessage `json:"preproc_blocks"`
	Discarded []json.RawMessage `json:"discarded_blocks"`
}
type nativeNode struct {
	Type      string            `json:"type"`
	Index     json.RawMessage   `json:"index"`
	BBox      []float64         `json:"bbox"`
	Lines     []nativeLine      `json:"lines"`
	Blocks    []json.RawMessage `json:"blocks"`
	Content   json.RawMessage   `json:"content"`
	HTML      string            `json:"html"`
	ImagePath string            `json:"image_path"`
	ImgPath   string            `json:"img_path"`
	Level     int               `json:"level"`
	Anchor    string            `json:"anchor"`
	SubType   string            `json:"sub_type"`
	GuessLang string            `json:"guess_lang"`
}
type nativeLine struct {
	Spans []json.RawMessage `json:"spans"`
}

func parseNativeMiddle(data []byte) (*MiddleDoc, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode native Middle: %w", err)
	}
	for _, mixed := range []string{"schema", "schema_version", "pages", "blocks", "metadata", "producer", "page_index_map"} {
		if _, ok := envelope[mixed]; ok {
			return nil, fmt.Errorf("%w: mixed native envelope field %s", ErrNotMiddleJSON, mixed)
		}
	}
	var pages []nativePage
	if err := json.Unmarshal(envelope["pdf_info"], &pages); err != nil || pages == nil {
		return nil, fmt.Errorf("%w: native pdf_info must be page objects", ErrBadBlocks)
	}
	doc := &MiddleDoc{Schema: NativeMiddleSchema, SchemaVersion: NativeMiddleSchemaVersion, Pages: len(pages), PageSize: make([][2]float64, len(pages))}
	if v := envelope["_version_name"]; v != nil {
		if err := json.Unmarshal(v, &doc.ProducerVersion); err != nil {
			return nil, fmt.Errorf("%w: invalid native producer version", ErrNotMiddleJSON)
		}
	}
	seenPages := map[int]bool{}
	for ordinal, page := range pages {
		if page.PageIdx == nil || *page.PageIdx < 0 || *page.PageIdx >= len(pages) || seenPages[*page.PageIdx] {
			return nil, fmt.Errorf("%w: missing/duplicate/out-of-range native page_idx", ErrBadBlocks)
		}
		pageIdx := *page.PageIdx
		seenPages[pageIdx] = true
		sizeOK := len(page.PageSize) == 2 && finitePositive(page.PageSize[0]) && finitePositive(page.PageSize[1])
		if sizeOK {
			doc.PageSize[pageIdx] = [2]float64{page.PageSize[0], page.PageSize[1]}
		}
		field, blocks := "para_blocks", page.Para
		if blocks == nil {
			field, blocks = "preproc_blocks", page.Preproc
		}
		if blocks == nil {
			return nil, fmt.Errorf("%w: native page has no final para/preproc block array", ErrBadBlocks)
		}
		seen := map[int]bool{}
		collections := []struct {
			name   string
			blocks []json.RawMessage
		}{{field, blocks}, {"discarded_blocks", page.Discarded}}
		for _, collection := range collections {
			for position, original := range collection.blocks {
				var node nativeNode
				if err := json.Unmarshal(original, &node); err != nil || node.Type == "" {
					return nil, fmt.Errorf("%w: malformed native block", ErrBadBlocks)
				}
				idx, ok := decodeBlockIndex(node.Index)
				// Actual standard/hybrid original indices are zero-based, including
				// discarded page auxiliaries. Never replace gaps by array ordinals.
				if !ok || idx < 0 || idx == int(^uint(0)>>1) || seen[idx] {
					return nil, fmt.Errorf("%w: missing/invalid/duplicate native index", ErrBadBlocks)
				}
				seen[idx] = true
				pointer := fmt.Sprintf("/pdf_info/%d/%s/%d", ordinal, collection.name, position)
				bbox := nativeBBox(node.BBox, page.PageSize)
				if len(node.BBox) > 0 && bbox == nil {
					doc.NormalizationWarnings = append(doc.NormalizationWarnings, pointer+": source bbox/page_size invalid; crop unavailable")
				}
				view, err := nativeRenderNode(original, 0)
				if err != nil {
					return nil, err
				}
				render, err := json.Marshal(view)
				if err != nil {
					return nil, err
				}
				content, _ := json.Marshal(view["content"])
				doc.Blocks = append(doc.Blocks, MiddleBlock{PageIdx: pageIdx, Index: idx + 1, Type: view["type"].(string), Content: content, BBox: bbox, Raw: append(json.RawMessage(nil), original...), RenderRaw: render, NativePath: pointer, NativeIndex: append(json.RawMessage(nil), node.Index...)})
			}
		}
	}
	return doc, nil
}
func finitePositive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func nativeBBox(box, size []float64) []float64 {
	if len(box) != 4 || len(size) != 2 || !finitePositive(size[0]) || !finitePositive(size[1]) {
		return nil
	}
	out := []float64{box[0] / size[0], box[1] / size[1], box[2] / size[0], box[3] / size[1]}
	for _, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return nil
		}
	}
	if out[2] <= out[0] || out[3] <= out[1] {
		return nil
	}
	return out
}
func nativeType(t string, level int) string {
	switch t {
	case "title":
		if level == 1 {
			return "doc_title"
		}
		return "paragraph_title"
	case "abstract", "phonetic":
		return "text"
	case "interline_equation":
		return "equation"
	case "header":
		return "page_header"
	case "footer":
		return "page_footer"
	}
	return t
}
func nativeImagePath(value string) string {
	if value != "" && !strings.Contains(value, "/") && !strings.ContainsAny(value, "\\:") {
		return "images/" + value
	}
	return value
}
func nativeRenderNode(original json.RawMessage, depth int) (map[string]any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("%w: native block nesting exceeds 64", ErrBadBlocks)
	}
	var node nativeNode
	if err := json.Unmarshal(original, &node); err != nil || node.Type == "" {
		return nil, fmt.Errorf("%w: malformed native node", ErrBadBlocks)
	}
	t := nativeType(node.Type, node.Level)
	view := map[string]any{"type": t}
	if node.Level > 0 {
		view["level"] = node.Level
	}
	if node.Anchor != "" {
		view["anchor"] = node.Anchor
	}
	if node.SubType != "" {
		view["sub_type"] = node.SubType
	}
	if node.GuessLang != "" {
		view["guess_lang"] = node.GuessLang
	}
	image := node.ImagePath
	if image == "" {
		image = node.ImgPath
	}
	if len(node.Blocks) > 0 {
		children := make([]map[string]any, 0, len(node.Blocks))
		for _, raw := range node.Blocks {
			child, err := nativeRenderNode(raw, depth+1)
			if err != nil {
				return nil, err
			}
			if t == "code" && node.SubType == "algorithm" && child["type"] == "code_body" {
				child["type"] = "algorithm_body"
			}
			children = append(children, child)
		}
		view["content"] = children
	} else {
		textSpans := make([]map[string]any, 0)
		var plain strings.Builder
		for lineNo, line := range node.Lines {
			if lineNo > 0 && plain.Len() > 0 && (t == "code_body" || t == "code") {
				plain.WriteString("\n")
			}
			for _, raw := range line.Spans {
				var span struct {
					Type      string          `json:"type"`
					Content   json.RawMessage `json:"content"`
					HTML      string          `json:"html"`
					ImagePath string          `json:"image_path"`
					ImgPath   string          `json:"img_path"`
					Styles    []string        `json:"styles"`
					URL       string          `json:"url"`
				}
				if err := json.Unmarshal(raw, &span); err != nil {
					return nil, fmt.Errorf("%w: malformed native span", ErrBadBlocks)
				}
				if image == "" {
					image = span.ImagePath
					if image == "" {
						image = span.ImgPath
					}
				}
				var text string
				if span.HTML != "" && (t == "table_body" || t == "table") {
					text = span.HTML
				} else if len(span.Content) > 0 && string(span.Content) != "null" {
					if err := json.Unmarshal(span.Content, &text); err != nil {
						return nil, fmt.Errorf("%w: native span content must be string", ErrBadBlocks)
					}
				}
				if text == "" {
					continue
				}
				kind := span.Type
				switch kind {
				case "inline_equation", "equation_inline":
					kind = "equation_inline"
				case "code":
					kind = "code_inline"
				case "interline_equation", "equation":
					kind = "text"
				case "":
					kind = "text"
				}
				if len(textSpans) > 0 {
					previous, _ := textSpans[len(textSpans)-1]["content"].(string)
					if nativeJoinSpace(previous, text) {
						textSpans = append(textSpans, map[string]any{"type": "text", "content": " "})
					}
				}
				converted := map[string]any{"type": kind, "content": text}
				if len(span.Styles) > 0 {
					converted["styles"] = span.Styles
				}
				if span.URL != "" {
					converted["url"] = span.URL
				}
				textSpans = append(textSpans, converted)
				if plain.Len() > 0 && nativeJoinSpace(plain.String(), text) {
					plain.WriteString(" ")
				}
				plain.WriteString(text)
			}
		}
		if node.HTML != "" && (t == "table_body" || t == "table") {
			view["content"] = node.HTML
		} else if len(node.Lines) == 0 && len(node.Content) > 0 {
			view["content"] = node.Content
		} else {
			switch t {
			case "equation", "table_body", "image_body", "chart_body", "code_body", "code", "table":
				view["content"] = plain.String()
			default:
				view["content"] = textSpans
			}
		}
	}
	if image != "" {
		view["image_path"] = nativeImagePath(image)
	}
	return view, nil
}
func nativeJoinSpace(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	a, _ := utf8.DecodeLastRuneInString(left)
	b, _ := utf8.DecodeRuneInString(right)
	return !unicode.IsSpace(a) && !unicode.IsSpace(b) && a < 128 && b < 128 && (unicode.IsLetter(a) || unicode.IsDigit(a) || strings.ContainsRune(")}]", a)) && (unicode.IsLetter(b) || unicode.IsDigit(b) || strings.ContainsRune("([{", b))
}
