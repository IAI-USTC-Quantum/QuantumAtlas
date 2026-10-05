package paperread

import (
	"html"
	"strconv"
	"strings"

	nethtml "golang.org/x/net/html"
)

func parseHTML(markup string) *nethtml.Node {
	doc, err := nethtml.Parse(strings.NewReader(markup))
	if err != nil {
		return nil
	}
	var body *nethtml.Node
	var visit func(*nethtml.Node)
	visit = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return body
}
func (r *markdownRenderer) embeddedHTML(markup string) string {
	body := parseHTML(markup)
	if body == nil {
		return html.EscapeString(markup)
	}
	var out strings.Builder
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		out.WriteString(r.safeHTML(c))
	}
	return strings.TrimSpace(out.String())
}

// Chart semantic content may be HTML without a table. Sanitize that HTML
// instead of treating it as table projection text in a verbatim code fence.
func (r *markdownRenderer) chart(markup string) string {
	body := parseHTML(markup)
	if body == nil {
		return html.EscapeString(markup)
	}
	var hasTable func(*nethtml.Node) bool
	hasTable = func(n *nethtml.Node) bool {
		if n.Type == nethtml.ElementNode && n.Data == "table" {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if hasTable(c) {
				return true
			}
		}
		return false
	}
	if hasTable(body) {
		return r.table(markup)
	}
	return r.embeddedHTML(markup)
}

func (r *markdownRenderer) table(markup string) string {
	body := parseHTML(markup)
	if body == nil {
		return fenced(markup, "")
	}
	var tables []*nethtml.Node
	var collect func(*nethtml.Node)
	collect = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && n.Data == "table" {
			tables = append(tables, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(body)
	if len(tables) == 0 {
		return fenced(markup, "")
	}
	if len(tables) != 1 || !simpleTable(tables[0]) {
		return r.embeddedHTML(markup)
	}
	// Do not discard annotations outside the table when converting to GFM.
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if c != tables[0] && (c.Type == nethtml.ElementNode || strings.TrimSpace(c.Data) != "") {
			return r.embeddedHTML(markup)
		}
	}
	var rows []*nethtml.Node
	var findRows func(*nethtml.Node)
	findRows = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && n.Data == "tr" {
			rows = append(rows, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findRows(c)
		}
	}
	findRows(tables[0])
	if len(rows) == 0 {
		return r.embeddedHTML(markup)
	}
	cells := make([][]string, 0, len(rows))
	header, width := -1, 0
	for ri, row := range rows {
		var values []string
		allTH := true
		for c := row.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != nethtml.ElementNode || c.Data != "th" && c.Data != "td" {
				continue
			}
			values = append(values, normalizeCell(r.cell(c)))
			if c.Data != "th" {
				allTH = false
			}
		}
		if len(values) == 0 {
			return r.embeddedHTML(markup)
		}
		if header < 0 && (allTH || row.Parent.Data == "thead") {
			header = ri
		}
		if len(values) > width {
			width = len(values)
		}
		cells = append(cells, values)
	}
	if header < 0 {
		header = 0
	}
	format := func(values []string) string {
		for len(values) < width {
			values = append(values, "")
		}
		return "| " + strings.Join(values, " | ") + " |"
	}
	lines := []string{format(cells[header]), format(repeatString("---", width))}
	for i, row := range cells {
		if i != header {
			lines = append(lines, format(row))
		}
	}
	return strings.Join(lines, "\n")
}
func repeatString(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func simpleTable(table *nethtml.Node) bool {
	allowed := map[string]bool{"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true, "a": true, "b": true, "strong": true, "i": true, "em": true, "br": true, "code": true, "eq": true, "p": true, "s": true, "span": true, "sub": true, "sup": true, "u": true}
	ok := true
	var visit func(*nethtml.Node)
	visit = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode {
			if !allowed[n.Data] || n != table && n.Data == "table" {
				ok = false
			}
			for _, a := range n.Attr {
				if a.Key == "rowspan" || a.Key == "colspan" || n.Data == "span" {
					ok = false
				}
			}
			if n.Data == "td" || n.Data == "th" || n.Data == "thead" {
				count := 0
				tag := "p"
				if n.Data == "thead" {
					tag = "tr"
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == nethtml.ElementNode && c.Data == tag {
						count++
					}
				}
				if count > 1 {
					ok = false
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(table)
	return ok
}

func (r *markdownRenderer) cell(n *nethtml.Node) string {
	if n.Type == nethtml.TextNode {
		return escapeCellText(n.Data)
	}
	if n.Type != nethtml.ElementNode {
		return ""
	}
	var out strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out.WriteString(r.cell(c))
	}
	text := out.String()
	switch n.Data {
	case "br":
		return "<br>"
	case "code":
		return inlineCode(strings.ReplaceAll(plainNode(n), "|", "\\|"))
	case "eq":
		return "$" + strings.ReplaceAll(strings.TrimSpace(plainNode(n)), "|", "\\|") + "$"
	case "b", "strong":
		return applyStyles(text, []string{"bold"})
	case "i", "em":
		return applyStyles(text, []string{"italic"})
	case "s":
		return applyStyles(text, []string{"strikethrough"})
	case "u":
		return "<u>" + text + "</u>"
	case "sub":
		return "<sub>" + text + "</sub>"
	case "sup":
		return "<sup>" + text + "</sup>"
	case "a":
		link := attr(n, "href")
		if link != "" && safeLink(link) {
			return "[" + escapeLabel(text) + "](" + strings.ReplaceAll(escapeDestination(link), "|", "%7C") + ")"
		}
	}
	return text
}
func normalizeCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(" <br>", "<br>", "<br> ", "<br>").Replace(s)
}
func escapeCellText(s string) string {
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", "\\", "\\\\", "|", "\\|").Replace(s)
	return s
}
func plainNode(n *nethtml.Node) string {
	if n.Type == nethtml.TextNode {
		return n.Data
	}
	var s strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.WriteString(plainNode(c))
	}
	return s.String()
}
func attr(n *nethtml.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// safeHTML keeps table geometry/semantic annotations but never exposes scripts,
// event handlers, CSS URLs or unsafe URLs from parsed document text. It rewrites
// relative embedded-image members through the same revision-pinned callback.
func (r *markdownRenderer) safeHTML(n *nethtml.Node) string {
	if n.Type == nethtml.TextNode {
		return html.EscapeString(n.Data)
	}
	if n.Type != nethtml.ElementNode {
		return ""
	}
	name := n.Data
	if name == "script" || name == "style" || name == "iframe" || name == "object" || name == "embed" || name == "svg" || name == "math" {
		r.warn("Unsafe or unsupported embedded HTML removed.")
		return ""
	}
	if name == "eq" {
		return " $" + html.EscapeString(strings.TrimSpace(plainNode(n))) + "$ "
	}
	var content strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		content.WriteString(r.safeHTML(c))
	}
	allowed := map[string]bool{"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true, "caption": true, "colgroup": true, "col": true, "p": true, "br": true, "b": true, "strong": true, "i": true, "em": true, "s": true, "u": true, "sub": true, "sup": true, "a": true, "img": true, "span": true, "div": true, "details": true, "summary": true, "pre": true, "code": true, "ul": true, "ol": true, "li": true}
	if !allowed[name] {
		r.warn("Unsupported embedded HTML tag unwrapped.")
		return content.String()
	}
	var attrs strings.Builder
	for _, a := range n.Attr {
		switch {
		case (name == "td" || name == "th") && (a.Key == "rowspan" || a.Key == "colspan"):
			value, err := strconv.Atoi(a.Val)
			if err == nil && value >= 1 && value <= 1000 {
				fmtAttr(&attrs, a.Key, a.Val)
			}
		case name == "a" && a.Key == "href":
			if safeLink(a.Val) {
				fmtAttr(&attrs, a.Key, a.Val)
			} else {
				r.warn("Unsafe hyperlink omitted.")
			}
		case name == "img" && a.Key == "src":
			source := a.Val
			if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") && !strings.HasPrefix(source, "/") {
				if !safeMember(source) {
					r.warn("Unsafe image member omitted.")
					continue
				}
				if r.imageURL != nil {
					source = r.imageURL(source)
				}
			}
			if safeLink(source) {
				fmtAttr(&attrs, a.Key, source)
			} else {
				r.warn("Unsafe image URL omitted.")
			}
		case name == "img" && a.Key == "alt":
			fmtAttr(&attrs, a.Key, a.Val)
		default:
			if strings.HasPrefix(a.Key, "on") || a.Key == "style" {
				r.warn("Unsafe embedded HTML attributes removed.")
			}
		}
	}
	if name == "img" || name == "br" || name == "col" {
		return "<" + name + attrs.String() + ">"
	}
	return "<" + name + attrs.String() + ">" + content.String() + "</" + name + ">"
}
func fmtAttr(out *strings.Builder, name, value string) {
	out.WriteString(" " + name + `="` + html.EscapeString(value) + `"`)
}
