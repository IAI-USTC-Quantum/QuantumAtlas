package paperassets

// figures.go: the figure/caption index extracted from MinerU markdown.
//
// MinerU emits images as bare references (`![](images/<sha256>.<ext>)`)
// followed or preceded by plain-text caption lines ("FIG. 1. Surface code
// performance. ..."). Multi-panel figures arrive as SEVERAL consecutive
// image lines sharing ONE caption; the caption may sit below (the common
// case) or above the image block, with blank lines in between. ExtractFigures
// groups those lines into Figure records so the figures endpoint can serve
// a per-figure index (caption text + the exact image files) without the
// client downloading and parsing the markdown itself.
//
// The grouping is intentionally heuristic: FigNo stays 0 and Caption empty
// when no caption-shaped line is found near a group, and every image the
// markdown references still appears in some Figure (an isolated image with
// no caption is a valid one-image figure group). Callers that need exact
// ground truth should treat the index as a navigation aid, not a citation.

import (
	"regexp"
	"strconv"
	"strings"
)

// Figure is one figure group: consecutive image references that share a
// caption (a multi-panel figure), or a single referenced image.
type Figure struct {
	// Images are the referenced image file names ("<sha256>.<ext>"), in
	// markdown order.
	Images []string
	// Caption is the matched caption line, trimmed. Empty = not found.
	Caption string
	// FigNo is the figure number parsed out of the caption; 0 = unknown.
	FigNo int
	// Context is the nearest non-empty text line above the group (trimmed,
	// truncated to figureContextMaxRunes runes) — the prose the figure
	// illustrates. Empty when the group starts the document.
	Context string
}

const (
	// figureCaptionBelowWindow is how many lines below the last image of a
	// group we scan for the caption before giving up.
	figureCaptionBelowWindow = 12
	// figureCaptionAboveWindow is how many lines above the first image of a
	// group we scan when the below-scan found nothing.
	figureCaptionAboveWindow = 4
	// figureCaptionStopLineLen: a body line longer than this between the
	// images and a would-be caption means real prose started — stop the
	// below-scan (avoids gluing the NEXT section's "FIG. 9." onto this group).
	figureCaptionStopLineLen = 200
	// figureContextMaxRunes caps the stored context line.
	figureContextMaxRunes = 200
)

var (
	// figureImageLineRE matches one MinerU image reference line.
	figureImageLineRE = regexp.MustCompile(`^\s*!\[\]\(images/([0-9a-f]{64})\.(jpg|jpeg|png|gif|webp)\)`)
	// figureCaptionBelowRE matches a caption line searched downward from the
	// image group: optional leading whitespace / bold markers, then the
	// figure label. Willow-style "FIG. 1." and Chinese "图 1" both match.
	figureCaptionBelowRE = regexp.MustCompile(`^\s*\**\s*(FIG|Fig|Figure|图)\.?\s*(\d+)`)
	// figureCaptionAboveRE matches a caption line searched upward: caption
	// lines above an image group start at column 0.
	figureCaptionAboveRE = regexp.MustCompile(`^(FIG|Fig|Figure|图)\.?\s*(\d+)`)
)

// ExtractFigures walks the markdown line by line, groups consecutive image
// references (blank lines allowed inside a group) into Figures, and attaches
// the nearest caption: first by scanning up to figureCaptionBelowWindow lines
// below the group's last image (stopping at a >figureCaptionStopLineLen body
// line), then up to figureCaptionAboveWindow lines above its first image.
// Context is the nearest preceding non-empty, non-image, non-caption line.
// It returns nil for markdown with no image references.
func ExtractFigures(markdown string) []Figure {
	lines := strings.Split(markdown, "\n")
	var out []Figure
	for i := 0; i < len(lines); i++ {
		m := figureImageLineRE.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		start, last := i, i
		fig := Figure{Images: []string{m[1] + "." + m[2]}}
		j := i + 1
		for j < len(lines) {
			m2 := figureImageLineRE.FindStringSubmatch(lines[j])
			if m2 != nil {
				fig.Images = append(fig.Images, m2[1]+"."+m2[2])
				last = j
				j++
				continue
			}
			if strings.TrimSpace(lines[j]) == "" {
				j++
				continue
			}
			break
		}
		fig.Caption, fig.FigNo = findFigureCaption(lines, start, last)
		fig.Context = figureContext(lines, start)
		out = append(out, fig)
		i = j - 1
	}
	return out
}

// findFigureCaption locates the caption line for the image group spanning
// lines [start, last]: below the group first, then above it.
func findFigureCaption(lines []string, start, last int) (string, int) {
	for k := last + 1; k < len(lines) && k <= last+figureCaptionBelowWindow; k++ {
		if m := figureCaptionBelowRE.FindStringSubmatch(lines[k]); m != nil {
			return strings.TrimSpace(lines[k]), captionFigNo(m[2])
		}
		if len(lines[k]) > figureCaptionStopLineLen {
			break
		}
	}
	for k := start - 1; k >= 0 && k >= start-figureCaptionAboveWindow; k-- {
		if m := figureCaptionAboveRE.FindStringSubmatch(lines[k]); m != nil {
			return strings.TrimSpace(lines[k]), captionFigNo(m[2])
		}
	}
	return "", 0
}

func captionFigNo(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}

// figureContext returns the nearest line above the group that is neither
// blank, an image reference, nor a caption line, truncated to
// figureContextMaxRunes runes.
func figureContext(lines []string, start int) string {
	for k := start - 1; k >= 0; k-- {
		line := lines[k]
		if strings.TrimSpace(line) == "" || figureImageLineRE.MatchString(line) {
			continue
		}
		if figureCaptionAboveRE.MatchString(line) || figureCaptionBelowRE.MatchString(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return truncateRunes(trimmed, figureContextMaxRunes)
	}
	return ""
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
