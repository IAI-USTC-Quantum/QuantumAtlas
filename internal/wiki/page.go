// Package wiki parses and walks the QuantumAtlas wiki repository — a
// directory tree of markdown files with YAML frontmatter.
//
// The on-disk format matches the Python implementation in atlas/wiki/page.py
// so a single wiki repo can be consumed by either server during the
// FastAPI -> Go transition. See README in the wiki repo for the canonical
// schema.
package wiki

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SystemFiles are markdown files that are NOT wiki pages even though they
// live under wiki_dir (index landing, log, README). Mirrors
// atlas/wiki/engine.py:SYSTEM_MARKDOWN_FILES.
var SystemFiles = map[string]struct{}{
	"index.md":  {},
	"log.md":    {},
	"README.md": {},
}

// PageType enumerates the allowed `type` frontmatter values.
type PageType = string

const (
	TypeConcept    PageType = "concept"
	TypeEntity     PageType = "entity"
	TypeSource     PageType = "source"
	TypeComparison PageType = "comparison"
)

// Status enumerates the allowed `status` frontmatter values.
type Status = string

const (
	StatusDraft     Status = "draft"
	StatusReview    Status = "review"
	StatusPublished Status = "published"
)

// ExternalLink mirrors atlas/wiki/page.py:ExternalLink.
type ExternalLink struct {
	Label string `yaml:"label" json:"label"`
	URL   string `yaml:"url" json:"url"`
	Kind  string `yaml:"kind" json:"kind"`
	Note  string `yaml:"note,omitempty" json:"note,omitempty"`
}

// Frontmatter is the parsed YAML header of a wiki page. Field tags follow
// the Python pydantic schema's snake_case names so the JSON we emit
// matches what the existing frontend expects.
type Frontmatter struct {
	ID             string         `yaml:"id" json:"id"`
	Title          string         `yaml:"title" json:"title"`
	Type           string         `yaml:"type" json:"type"`
	Category       string         `yaml:"category,omitempty" json:"category,omitempty"`
	Tags           []string       `yaml:"tags,omitempty" json:"tags"`
	CreatedAt      *FlexTime      `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt      *FlexTime      `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	Version        int            `yaml:"version,omitempty" json:"version,omitempty"`
	Status         string         `yaml:"status,omitempty" json:"status"`
	Related        []string       `yaml:"related,omitempty" json:"related"`
	ExternalLinks  []ExternalLink `yaml:"external_links,omitempty" json:"external_links"`
	Neo4jSynced    bool           `yaml:"neo4j_synced,omitempty" json:"neo4j_synced"`
	Neo4jID        string         `yaml:"neo4j_id,omitempty" json:"neo4j_id,omitempty"`
}

// Page is one parsed wiki page (frontmatter + markdown body + source path).
type Page struct {
	Frontmatter Frontmatter `json:"frontmatter"`
	Content     string      `json:"content"`
	Path        string      `json:"-"` // source file path, internal use only
}

// FlexTime accepts either "2006-01-02" (the Python wiki convention) or
// full RFC3339 for the `created_at` / `updated_at` frontmatter values, so
// pages written by either implementation round-trip cleanly.
type FlexTime struct {
	time.Time
}

// UnmarshalYAML decodes the value as a string and tries the two known
// formats. Returns an error on unrecognized input so we don't silently
// drop dates.
func (t *FlexTime) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("unrecognized timestamp %q", raw)
}

// MarshalJSON emits ISO 8601 to match the Python isoformat() output that
// the frontend consumes. Empty time -> JSON null.
func (t FlexTime) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + t.Time.Format(time.RFC3339) + `"`), nil
}

// frontmatterPattern matches the leading "---\n<yaml>\n---\n<body>" block.
// Equivalent to atlas/wiki/page.py:WikiPage.FRONTMATTER_PATTERN.
var frontmatterPattern = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n(.*)$`)

// ErrNoFrontmatter signals a markdown file missing the YAML frontmatter
// block. Callers (e.g. linters) may want to handle this differently from
// a YAML parse error.
var ErrNoFrontmatter = errors.New("wiki page missing YAML frontmatter")

// ParseMarkdown turns a full markdown string into a Page (sans Path).
func ParseMarkdown(raw string) (*Page, error) {
	match := frontmatterPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return nil, ErrNoFrontmatter
	}

	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(match[1]), &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	return &Page{
		Frontmatter: fm,
		Content:     strings.TrimSpace(match[2]),
	}, nil
}

// ReadPage parses a single .md file from disk.
func ReadPage(path string) (*Page, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := ParseMarkdown(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Path = path
	return p, nil
}

// IsSystemFile reports whether the given file name belongs to the wiki
// infrastructure (index, log, README) rather than to a real page.
func IsSystemFile(name string) bool {
	_, ok := SystemFiles[filepath.Base(name)]
	return ok
}

// WalkOptions tunes the IteratePages walker behavior.
type WalkOptions struct {
	// IgnoreParseErrors continues past pages that fail to parse instead
	// of bubbling the error to the caller. Errors are stored on the
	// returned iteration result for logging.
	IgnoreParseErrors bool
}

// PageIterFunc is invoked for each successfully-parsed page during a walk.
// Return a non-nil error to stop iteration early.
type PageIterFunc func(*Page) error

// IteratePages walks wikiDir recursively, parsing each non-system .md file
// and calling fn for each page. SystemFiles and any path that's not a
// regular .md file are skipped. Returns the count of parse errors and
// the first such error (only when IgnoreParseErrors=true).
func IteratePages(wikiDir string, opts WalkOptions, fn PageIterFunc) (int, error) {
	var parseErrCount int
	var firstParseErr error

	err := filepath.WalkDir(wikiDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		if IsSystemFile(path) {
			return nil
		}

		page, perr := ReadPage(path)
		if perr != nil {
			parseErrCount++
			if firstParseErr == nil {
				firstParseErr = perr
			}
			if opts.IgnoreParseErrors {
				return nil
			}
			return perr
		}
		return fn(page)
	})
	if err != nil {
		return parseErrCount, err
	}
	if firstParseErr != nil && !opts.IgnoreParseErrors {
		return parseErrCount, firstParseErr
	}
	return parseErrCount, nil
}

// ListFilter narrows the result set of ListPages.
type ListFilter struct {
	Type     string
	Category string
	Tags     []string // OR-semantics: any tag match keeps the page
	Status   string
}

// matches reports whether page satisfies all set fields on f.
func (f ListFilter) matches(p *Page) bool {
	if f.Type != "" && p.Frontmatter.Type != f.Type {
		return false
	}
	if f.Category != "" && p.Frontmatter.Category != f.Category {
		return false
	}
	if f.Status != "" && p.Frontmatter.Status != f.Status {
		return false
	}
	if len(f.Tags) > 0 {
		hit := false
		for _, want := range f.Tags {
			for _, have := range p.Frontmatter.Tags {
				if have == want {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// IsEmpty reports whether f is the zero filter (every page matches).
// Used by Cache.Pages as a fast-path to skip per-page matches() when
// no filtering is requested — callers can then return the underlying
// slice directly.
func (f ListFilter) IsEmpty() bool {
	return f.Type == "" && f.Category == "" && f.Status == "" && len(f.Tags) == 0
}

// ListPages returns every page under wikiDir matching f, with parse errors
// counted but not fatal (matches FastAPI's "skip bad pages" behavior).
func ListPages(wikiDir string, f ListFilter) ([]*Page, int, error) {
	var pages []*Page
	parseErrs, err := IteratePages(wikiDir, WalkOptions{IgnoreParseErrors: true}, func(p *Page) error {
		if f.matches(p) {
			pages = append(pages, p)
		}
		return nil
	})
	return pages, parseErrs, err
}

// FindPage returns the page whose frontmatter.id equals pageID, or
// (nil, nil) if no match. Parse errors are skipped silently.
func FindPage(wikiDir, pageID string) (*Page, error) {
	var found *Page
	stop := errors.New("__stop_iter__")
	_, err := IteratePages(wikiDir, WalkOptions{IgnoreParseErrors: true}, func(p *Page) error {
		if p.Frontmatter.ID == pageID {
			found = p
			return stop
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, err
	}
	return found, nil
}

// Stats summarizes a wiki tree. Mirrors atlas/wiki/engine.py:get_stats output.
type Stats struct {
	TotalPages    int            `json:"total_pages"`
	ByType        map[string]int `json:"by_type"`
	ByStatus      map[string]int `json:"by_status"`
	ByCategory    map[string]int `json:"by_category"`
	SyncedToNeo4j int            `json:"synced_to_neo4j"`
	NeedsSync     int            `json:"needs_sync"`
}

// ComputeStats counts pages bucketed by type/status/category and Neo4j sync state.
func ComputeStats(wikiDir string) (*Stats, error) {
	stats := &Stats{
		ByType:     map[string]int{},
		ByStatus:   map[string]int{},
		ByCategory: map[string]int{},
	}
	_, err := IteratePages(wikiDir, WalkOptions{IgnoreParseErrors: true}, func(p *Page) error {
		stats.TotalPages++
		if t := p.Frontmatter.Type; t != "" {
			stats.ByType[t]++
		}
		if s := p.Frontmatter.Status; s != "" {
			stats.ByStatus[s]++
		}
		if c := p.Frontmatter.Category; c != "" {
			stats.ByCategory[c]++
		}
		if p.Frontmatter.Neo4jSynced {
			stats.SyncedToNeo4j++
		} else {
			stats.NeedsSync++
		}
		return nil
	})
	return stats, err
}

// SearchResult is one match returned by Search.
type SearchResult struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Type    string  `json:"type"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// Search runs a case-insensitive substring search across page title,
// id, tags, and body content. Scoring is intentionally simple: title hits
// count 3x, tag hits 2x, body hits 1x. Sorted by descending score, ties
// broken by ID.
//
// This is a deliberate downgrade from the Python engine's BM25 — none of
// our wiki pages exceed a few KB, and the frontend already does its own
// fuzzy filter on the returned list.
func Search(wikiDir, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	var results []SearchResult
	_, err := IteratePages(wikiDir, WalkOptions{IgnoreParseErrors: true}, func(p *Page) error {
		score := 0.0
		title := strings.ToLower(p.Frontmatter.Title)
		id := strings.ToLower(p.Frontmatter.ID)
		body := strings.ToLower(p.Content)

		if strings.Contains(title, q) {
			score += 3
		}
		if strings.Contains(id, q) {
			score += 3
		}
		for _, tag := range p.Frontmatter.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				score += 2
			}
		}
		if hits := strings.Count(body, q); hits > 0 {
			score += float64(hits)
		}
		if score == 0 {
			return nil
		}

		snippet := makeSnippet(p.Content, q, 160)
		results = append(results, SearchResult{
			ID:      p.Frontmatter.ID,
			Title:   p.Frontmatter.Title,
			Type:    p.Frontmatter.Type,
			Snippet: snippet,
			Score:   score,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortResults(results)
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results, nil
}

// makeSnippet returns up to maxLen characters of body centered on the first
// case-insensitive match of q. If q isn't in body, returns the body prefix.
func makeSnippet(body, q string, maxLen int) string {
	lower := strings.ToLower(body)
	idx := strings.Index(lower, q)
	if idx < 0 {
		if len(body) <= maxLen {
			return body
		}
		return body[:maxLen] + "..."
	}
	start := idx - maxLen/2
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(body) {
		end = len(body)
	}
	snippet := body[start:end]
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(body) {
		snippet += "..."
	}
	// Strip newlines so JSON output stays one-line per snippet.
	return strings.Join(strings.Fields(snippet), " ")
}

// sortResults is a tiny insertion sort, since N is at most maxResults.
func sortResults(results []SearchResult) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0; j-- {
			if results[j].Score > results[j-1].Score ||
				(results[j].Score == results[j-1].Score && results[j].ID < results[j-1].ID) {
				results[j], results[j-1] = results[j-1], results[j]
				continue
			}
			break
		}
	}
}
