package paperread

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// This renderer follows single-block DocVortex semantics (no document-level
// continuation merging or auxiliary-page filtering). Unknown future types are
// surfaced as bounded inert content with warnings rather than silently omitted.
// It does not claim byte equality with every upstream Python renderer version.
type markdownRenderer struct {
	imageURL func(string) string
	warnings []string
	warned   map[string]bool
}

type node struct {
	Type        string          `json:"type"`
	Content     json.RawMessage `json:"content"`
	Styles      []string        `json:"styles"`
	Level       int             `json:"level"`
	Anchor      string          `json:"anchor"`
	URL         string          `json:"url"`
	ImagePath   string          `json:"image_path"`
	ImageURL    string          `json:"image_url"`
	ImageBase64 string          `json:"image_base64"`
	SubType     string          `json:"sub_type"`
	GuessLang   string          `json:"guess_lang"`
}

func (r *markdownRenderer) warn(s string) {
	if r.warned == nil {
		r.warned = make(map[string]bool)
	}
	if !r.warned[s] {
		r.warnings = append(r.warnings, s)
		r.warned[s] = true
	}
}
func rawString(data json.RawMessage) (string, bool) {
	var s string
	err := json.Unmarshal(data, &s)
	return s, err == nil
}
func children(data json.RawMessage) ([]json.RawMessage, bool) {
	var ns []json.RawMessage
	err := json.Unmarshal(data, &ns)
	return ns, err == nil && ns != nil
}

func (r *markdownRenderer) block(raw json.RawMessage, depth int) string {
	if depth > 64 {
		r.warn("Block nesting exceeds 64; deeper content shown as bounded raw data.")
		return fallback(raw)
	}
	var n node
	if json.Unmarshal(raw, &n) != nil {
		r.warn("Malformed block shown as bounded raw data.")
		return fallback(raw)
	}
	var text string
	switch n.Type {
	case "text", "ref_text", "aside_text", "page_header", "page_footer", "page_number", "page_footnote", "image_caption", "image_footnote", "table_caption", "table_footnote", "chart_caption", "chart_footnote", "code_caption", "code_footnote", "list_item", "index_item":
		text = r.inline(n.Content, depth+1)
		if n.Type == "text" || n.Type == "aside_text" || strings.HasPrefix(n.Type, "page_") {
			text = escapePrefix(text)
		}
		if n.Type == "page_footnote" && strings.TrimSpace(text) != "" {
			text = `<small><span class="docvortex-page-footnote" data-block-type="page_footnote" style="color:#6b7280">` + strings.ReplaceAll(text, "\n", "<br>") + `</span></small>`
		}
	case "doc_title", "paragraph_title":
		level := n.Level
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		text = strings.Repeat("#", level) + " " + r.inline(n.Content, depth+1)
	case "equation":
		if latex, ok := rawString(n.Content); ok && strings.TrimSpace(latex) != "" {
			text = "$$\n" + strings.TrimSpace(latex) + "\n$$"
		} else {
			text = r.image(n)
		}
	case "image", "table", "chart", "code":
		if ns, ok := children(n.Content); ok {
			parts := make([]string, 0, len(ns))
			for _, child := range ns {
				var c node
				_ = json.Unmarshal(child, &c)
				var part string
				if c.Type == "code_body" {
					language := n.GuessLang
					if !languageRE.MatchString(language) {
						language = "txt"
					}
					s, _ := rawString(c.Content)
					part = fenced(s, language)
				} else {
					part = r.block(child, depth+1)
				}
				if strings.TrimSpace(part) != "" {
					parts = append(parts, part)
				}
			}
			text = strings.Join(parts, "\n\n")
		} else if s, ok := rawString(n.Content); ok {
			if n.Type == "table" {
				text = r.table(s)
			} else if n.Type == "chart" {
				text = r.chart(s)
			} else if n.Type == "code" {
				text = fenced(s, "txt")
			} else {
				text = r.image(n)
				if s != "" {
					text += "\n\n" + r.embeddedHTML(s)
				}
			}
		} else {
			text = r.image(n)
		}
	case "image_body", "chart_body":
		text = r.image(n)
		if s, ok := rawString(n.Content); ok && strings.TrimSpace(s) != "" {
			body := r.embeddedHTML(s)
			if n.Type == "chart_body" {
				body = r.chart(s)
			}
			if text != "" {
				text += "\n\n"
			}
			text += body
		}
	case "table_body":
		if s, ok := rawString(n.Content); ok && strings.TrimSpace(s) != "" {
			text = r.table(s)
		} else {
			text = r.image(n)
		}
	case "code_body":
		s, _ := rawString(n.Content)
		text = fenced(s, "txt")
	case "algorithm_body":
		text = `<div class="docvortex-algorithm" style="white-space: pre-wrap; font-family:monospace;">` + r.inlineHTML(n.Content, depth+1) + `</div>`
	case "list", "index":
		text = r.list(n, depth+1, 0)
	default:
		r.warn(fmt.Sprintf("Unsupported block type %q; content shown as bounded raw data.", n.Type))
		text = fallback(n.Content)
	}
	text = strings.TrimSpace(text)
	if text != "" && n.Anchor != "" && (n.Type == "text" || n.Type == "doc_title" || n.Type == "paragraph_title") {
		text = `<a id="` + html.EscapeString(n.Anchor) + `"></a>` + "\n" + text
	}
	return text
}

func (r *markdownRenderer) inline(raw json.RawMessage, depth int) string {
	if depth > 64 {
		r.warn("Inline nesting exceeds 64; deeper content shown as bounded raw data.")
		return fallback(raw)
	}
	if s, ok := rawString(raw); ok {
		return escapeText(s)
	}
	ns, ok := children(raw)
	if !ok {
		if len(raw) == 0 || string(raw) == "null" {
			return ""
		}
		r.warn("Malformed inline content shown as bounded raw data.")
		return fallback(raw)
	}
	var out strings.Builder
	for _, v := range ns {
		var n node
		if json.Unmarshal(v, &n) != nil {
			r.warn("Malformed inline span shown as bounded raw data.")
			out.WriteString(fallback(v))
			continue
		}
		s, _ := rawString(n.Content)
		switch n.Type {
		case "text":
			out.WriteString(applyStyles(escapeText(s), n.Styles))
		case "equation_inline":
			out.WriteString("$" + s + "$")
		case "code_inline":
			out.WriteString(inlineCode(s))
		case "hyperlink":
			label := r.inline(n.Content, depth+1)
			if n.URL == "" || n.URL == "." {
				out.WriteString(label)
			} else if safeLink(n.URL) {
				out.WriteString("[" + escapeLabel(label) + "](" + escapeDestination(n.URL) + ")")
			} else {
				out.WriteString(label)
				r.warn("Unsafe hyperlink omitted.")
			}
		default:
			r.warn(fmt.Sprintf("Unsupported inline span %q; content shown literally.", n.Type))
			if s != "" {
				out.WriteString(escapeText(s))
			} else {
				out.WriteString(fallback(n.Content))
			}
		}
	}
	return out.String()
}

func (r *markdownRenderer) inlineHTML(raw json.RawMessage, depth int) string {
	if depth > 64 {
		return html.EscapeString(fallback(raw))
	}
	if s, ok := rawString(raw); ok {
		return html.EscapeString(s)
	}
	ns, _ := children(raw)
	var out strings.Builder
	for _, v := range ns {
		var n node
		_ = json.Unmarshal(v, &n)
		s, _ := rawString(n.Content)
		switch n.Type {
		case "text":
			out.WriteString(htmlStyles(html.EscapeString(s), n.Styles))
		case "equation_inline":
			out.WriteString(html.EscapeString("$" + s + "$"))
		case "code_inline":
			out.WriteString("<code>" + html.EscapeString(s) + "</code>")
		case "hyperlink":
			label := r.inlineHTML(n.Content, depth+1)
			if safeLink(n.URL) {
				out.WriteString(`<a href="` + html.EscapeString(n.URL) + `">` + label + `</a>`)
			} else {
				out.WriteString(label)
			}
		default:
			out.WriteString(html.EscapeString(s))
		}
	}
	return out.String()
}

func (r *markdownRenderer) list(n node, depth, indent int) string {
	if depth > 64 {
		r.warn("List nesting exceeds 64.")
		return fallback(n.Content)
	}
	ns, ok := children(n.Content)
	if !ok {
		return r.inline(n.Content, depth+1)
	}
	parts := make([]string, 0, len(ns))
	count, numbered := 0, 0
	for _, raw := range ns {
		var child node
		_ = json.Unmarshal(raw, &child)
		if child.Type == "list" || child.Type == "index" {
			continue
		}
		plain := strings.TrimSpace(plainInline(child.Content))
		if plain == "" {
			continue
		}
		count++
		for i, char := range []rune(plain) {
			if i > 4 {
				break
			}
			if unicode.IsDigit(char) {
				numbered++
				break
			}
		}
	}
	bullets := n.SubType == "ref_text" && count > 0 && numbered*2 <= count
	for _, raw := range ns {
		var child node
		_ = json.Unmarshal(raw, &child)
		if child.Type == "list" || child.Type == "index" {
			parts = append(parts, r.list(child, depth+1, indent+1))
			continue
		}
		item := r.inline(child.Content, depth+1)
		if strings.TrimSpace(item) == "" {
			continue
		}
		if n.Type == "index" {
			if child.Anchor != "" {
				item = "[" + escapeLabel(item) + "](#" + escapeDestination(child.Anchor) + ")"
			}
			item = "- " + item
		} else if bullets && !strings.HasPrefix(strings.TrimLeft(item, " \t"), "- ") {
			item = "- " + item
		}
		prefix := strings.Repeat("    ", indent)
		parts = append(parts, prefix+strings.ReplaceAll(item, "\n", "\n"+prefix))
	}
	return strings.Join(parts, "\n")
}
func plainInline(raw json.RawMessage) string {
	if s, ok := rawString(raw); ok {
		return s
	}
	ns, _ := children(raw)
	var out strings.Builder
	for _, v := range ns {
		var n node
		_ = json.Unmarshal(v, &n)
		if s, ok := rawString(n.Content); ok {
			out.WriteString(s)
		}
	}
	return out.String()
}

func (r *markdownRenderer) image(n node) string {
	source := ""
	if n.ImagePath != "" {
		if !safeMember(n.ImagePath) {
			r.warn("Unsafe image member omitted: " + bounded(n.ImagePath, 200))
			return ""
		}
		source = n.ImagePath
		if r.imageURL != nil {
			source = r.imageURL(n.ImagePath)
		}
	} else if n.ImageBase64 != "" {
		r.warn("Inline image bytes omitted; use the original parse artifact for embedded assets.")
	} else if n.ImageURL != "" && safeLink(n.ImageURL) {
		source = n.ImageURL
	}
	if source == "" {
		r.warn("Image asset unavailable in the parse bundle; inspect the original source or parse artifact.")
		return "[Image asset unavailable]"
	}
	if !safeLink(source) {
		r.warn("Unsafe image URL omitted.")
		return ""
	}
	return "![](" + escapeDestination(source) + ")"
}

func safeMember(s string) bool {
	if strings.TrimSpace(s) == "" || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\\x00\r\n") {
		return false
	}
	if path.Clean(s) != s || s == "." || s == ".." || strings.HasPrefix(s, "../") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "" && u.Host == "" && u.RawQuery == "" && u.Fragment == ""
}
func safeLink(s string) bool {
	if strings.ContainsAny(s, "\x00\r\n") {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || strings.HasPrefix(strings.TrimSpace(s), "//") {
		return false
	}
	return u.Scheme == "" || strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "mailto")
}
func escapeDestination(s string) string {
	return strings.NewReplacer("\\", "%5C", " ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "\t", "%09").Replace(s)
}
func escapeLabel(s string) string { return strings.NewReplacer("[", "\\[", "]", "\\]").Replace(s) }
func escapeText(s string) string {
	var out strings.Builder
	slashes := 0
	for _, char := range s {
		if char == '\\' {
			out.WriteRune(char)
			slashes++
			continue
		}
		if strings.ContainsRune("*_`~$", char) && slashes%2 == 0 {
			out.WriteByte('\\')
		}
		slashes = 0
		switch char {
		case '&':
			out.WriteString("&amp;")
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		default:
			out.WriteRune(char)
		}
	}
	return out.String()
}

var prefixRE = regexp.MustCompile(`^([ \t]{0,3})(#{1,6}|[+-])([ \t])`)

func escapePrefix(s string) string {
	loc := prefixRE.FindStringSubmatchIndex(s)
	if loc != nil {
		at := loc[4]
		s = s[:at] + "\\" + s[at:]
	}
	if strings.Trim(s, "-") == "" && s != "" || strings.Trim(s, "_") == "" && s != "" {
		return "\\" + s
	}
	return s
}
func applyStyles(s string, styles []string) string {
	if len(styles) == 0 {
		return s
	}
	set := make(map[string]bool)
	for _, style := range styles {
		set[style] = true
	}
	wrapper := ""
	if len(set) == 1 {
		if set["bold"] {
			wrapper = "**"
		}
		if set["italic"] {
			wrapper = "*"
		}
		if set["strikethrough"] {
			wrapper = "~~"
		}
	}
	if len(set) == 2 && set["bold"] && set["italic"] {
		wrapper = "***"
	}
	leading := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
	trailing := s[len(strings.TrimRight(s, " \t")):]
	core := strings.Trim(s, " \t")
	if core == "" {
		return s
	}
	if wrapper != "" {
		return leading + wrapper + core + wrapper + trailing
	}
	return leading + htmlStyles(core, styles) + trailing
}
func htmlStyles(s string, styles []string) string {
	set := make(map[string]bool)
	for _, style := range styles {
		set[style] = true
	}
	if set["superscript"] {
		s = "<sup>" + s + "</sup>"
	} else if set["subscript"] {
		s = "<sub>" + s + "</sub>"
	}
	for _, pair := range [][2]string{{"underline", "u"}, {"bold", "strong"}, {"italic", "em"}, {"strikethrough", "s"}} {
		if set[pair[0]] {
			s = "<" + pair[1] + ">" + s + "</" + pair[1] + ">"
		}
	}
	if set["emphasis"] {
		s = `<span style="text-emphasis: dot; text-emphasis-position: under;">` + s + `</span>`
	}
	return s
}
func inlineCode(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	fence := strings.Repeat("`", longestTicks(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, "`") || strings.HasSuffix(s, " ") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

var languageRE = regexp.MustCompile(`^[A-Za-z0-9_.+#-]+$`)

func fenced(s, language string) string {
	fence := strings.Repeat("`", max(3, longestTicks(s)+1))
	end := "\n"
	if strings.HasSuffix(s, "\n") {
		end = ""
	}
	return fence + language + "\n" + s + end + fence
}
func longestTicks(s string) int {
	longest, current := 0, 0
	for _, c := range s {
		if c == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return longest
}
func bounded(s string, limit int) string {
	rs := []rune(s)
	if len(rs) > limit {
		return string(rs[:limit]) + "… [truncated]"
	}
	return s
}
func fallback(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s, ok := rawString(raw); ok {
		return fenced(bounded(s, 4000), "txt")
	}
	return fenced(bounded(string(raw), 4000), "json")
}
