package openalexcorpus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// QueryOptions is the local, OpenAlex-like query surface over the PostgreSQL
// corpus. It intentionally models the useful operator/API shapes (id lookup,
// DOI lookup, filters, search, sort, page/per-page) without exposing an
// outbound OpenAlex-compatible HTTP API.
type QueryOptions struct {
	ID      string
	DOI     string
	Search  string
	Filters []Filter
	Sort    string
	Page    int
	PerPage int
	Count   bool
}

// Filter is one parsed "key:value" OpenAlex-like filter.
type Filter struct {
	Key   string
	Value string
}

// QueryMeta mirrors the official API envelope where that shape is useful.
// Count is optional because count(*) over the full corpus is expensive and
// should be an explicit operator choice.
type QueryMeta struct {
	Count   *int64 `json:"count,omitempty"`
	Page    int    `json:"page"`
	PerPage int    `json:"per_page"`
}

// QueryResult is the JSON envelope emitted by query-pg for list/search calls.
type QueryResult struct {
	Meta    QueryMeta         `json:"meta"`
	Results []json.RawMessage `json:"results"`
}

// GetWork returns one raw OpenAlex work record by bare or URL-form OpenAlex id.
func (s *Store) GetWork(ctx context.Context, id string) (json.RawMessage, bool, error) {
	if !s.ensure(ctx) {
		return nil, false, ErrCorpusUnavailable
	}
	id = shortID(id)
	if id == "" {
		return nil, false, fmt.Errorf("openalexcorpus: empty id")
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT record FROM openalex_works WHERE openalex_id = $1`, id).Scan(&raw)
	if err != nil {
		if isNoRows(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("openalexcorpus: get work %s: %w", id, err)
	}
	return json.RawMessage(raw), true, nil
}

// GetWorkByDOI returns one raw OpenAlex work record by DOI. Bare DOIs are
// normalized to the OpenAlex snapshot form ("https://doi.org/...").
func (s *Store) GetWorkByDOI(ctx context.Context, doi string) (json.RawMessage, bool, error) {
	if !s.ensure(ctx) {
		return nil, false, ErrCorpusUnavailable
	}
	doi = normalizeOpenAlexDOI(doi)
	if doi == "" {
		return nil, false, fmt.Errorf("openalexcorpus: empty doi")
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT record FROM openalex_works WHERE doi = $1`, doi).Scan(&raw)
	if err != nil {
		if isNoRows(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("openalexcorpus: get doi %s: %w", doi, err)
	}
	return json.RawMessage(raw), true, nil
}

// QueryWorks runs a local OpenAlex-like list/search query against PostgreSQL.
// It returns raw records exactly as stored in record jsonb.
func (s *Store) QueryWorks(ctx context.Context, opts QueryOptions) (QueryResult, error) {
	var out QueryResult
	if !s.ensure(ctx) {
		return out, ErrCorpusUnavailable
	}
	if opts.PerPage <= 0 {
		opts.PerPage = 25
	}
	if opts.PerPage > 200 {
		opts.PerPage = 200
	}
	if opts.Page <= 0 {
		opts.Page = 1
	}
	out.Meta.Page = opts.Page
	out.Meta.PerPage = opts.PerPage

	where, args, err := buildWhere(opts)
	if err != nil {
		return out, err
	}
	orderBy, err := orderBySQL(opts.Sort, opts.Search != "")
	if err != nil {
		return out, err
	}

	if opts.Count {
		countSQL := `SELECT count(*)::bigint FROM openalex_works` + where
		var n int64
		if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&n); err != nil {
			return out, fmt.Errorf("openalexcorpus: count query: %w", err)
		}
		out.Meta.Count = &n
	}

	limitArg := len(args) + 1
	offsetArg := len(args) + 2
	args = append(args, opts.PerPage, (opts.Page-1)*opts.PerPage)
	sql := fmt.Sprintf(
		`SELECT record FROM openalex_works%s%s LIMIT $%d OFFSET $%d`,
		where, orderBy, limitArg, offsetArg,
	)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return out, fmt.Errorf("openalexcorpus: query works: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return out, fmt.Errorf("openalexcorpus: scan work: %w", err)
		}
		out.Results = append(out.Results, json.RawMessage(raw))
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("openalexcorpus: iterate works: %w", err)
	}
	return out, nil
}

func buildWhere(opts QueryOptions) (string, []any, error) {
	var (
		clauses []string
		args    []any
	)
	add := func(clause string, v any) {
		args = append(args, v)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if opts.ID != "" {
		add("openalex_id = $%d", shortID(opts.ID))
	}
	if opts.DOI != "" {
		add("doi = $%d", normalizeOpenAlexDOI(opts.DOI))
	}
	if opts.Search != "" {
		add("search_text @@ plainto_tsquery('simple'::regconfig, $%d)", opts.Search)
	}
	for _, f := range opts.Filters {
		if err := addFilter(&clauses, &args, f); err != nil {
			return "", nil, err
		}
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func addFilter(clauses *[]string, args *[]any, f Filter) error {
	key := strings.TrimSpace(strings.ToLower(f.Key))
	val := strings.TrimSpace(f.Value)
	add := func(clause string, v any) {
		*args = append(*args, v)
		*clauses = append(*clauses, fmt.Sprintf(clause, len(*args)))
	}
	switch key {
	case "openalex_id", "id":
		add("openalex_id = $%d", shortID(val))
	case "doi":
		add("doi = $%d", normalizeOpenAlexDOI(val))
	case "type":
		add("work_type = $%d", val)
	case "language":
		add("language = $%d", val)
	case "primary_topic.id", "primary_topic_id":
		add("primary_topic_id = $%d", val)
	case "arxiv_id":
		add("arxiv_id = $%d", val)
	case "has_arxiv":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("openalexcorpus: filter has_arxiv: %w", err)
		}
		if b {
			*clauses = append(*clauses, "arxiv_id IS NOT NULL")
		} else {
			*clauses = append(*clauses, "arxiv_id IS NULL")
		}
	case "is_retracted":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("openalexcorpus: filter is_retracted: %w", err)
		}
		add("is_retracted = $%d", b)
	case "publication_year":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("openalexcorpus: filter publication_year: %w", err)
		}
		add("publication_year = $%d", n)
	case "from_publication_year":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("openalexcorpus: filter from_publication_year: %w", err)
		}
		add("publication_year >= $%d", n)
	case "to_publication_year":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("openalexcorpus: filter to_publication_year: %w", err)
		}
		add("publication_year <= $%d", n)
	case "from_updated_date":
		add("updated_date >= $%d::date", val)
	case "to_updated_date":
		add("updated_date <= $%d::date", val)
	case "cites":
		// Works that cite $val: $val is an element of this row's out-edge
		// array (GIN `?` element-exists reverse-lookup, ADR 0010).
		add("openalex_referenced_work_ids ? $%d", shortID(val))
	case "cited_by":
		// Works cited by $val: this row's id is an element of $val's
		// out-edge array (single-row PK probe on the citing side).
		add("EXISTS (SELECT 1 FROM openalex_works citing WHERE citing.openalex_id = $%d AND citing.openalex_referenced_work_ids ? openalex_works.openalex_id)", shortID(val))
	default:
		return fmt.Errorf("openalexcorpus: unsupported filter %q", f.Key)
	}
	return nil
}

func orderBySQL(sort string, hasSearch bool) (string, error) {
	if strings.TrimSpace(sort) == "" {
		if hasSearch {
			return " ORDER BY cited_by_count DESC NULLS LAST, openalex_id", nil
		}
		return " ORDER BY openalex_id", nil
	}
	parts := strings.Split(sort, ":")
	key := strings.TrimSpace(strings.ToLower(parts[0]))
	dir := "ASC"
	if len(parts) > 1 {
		switch strings.ToLower(strings.TrimSpace(parts[1])) {
		case "desc":
			dir = "DESC"
		case "asc":
			dir = "ASC"
		default:
			return "", fmt.Errorf("openalexcorpus: unsupported sort direction %q", parts[1])
		}
	}
	var col string
	switch key {
	case "cited_by_count":
		col = "cited_by_count"
	case "publication_year":
		col = "publication_year"
	case "updated_date":
		col = "updated_date"
	case "openalex_id", "id":
		col = "openalex_id"
	default:
		return "", fmt.Errorf("openalexcorpus: unsupported sort %q", sort)
	}
	nulls := " NULLS LAST"
	if col == "openalex_id" {
		nulls = ""
	}
	return " ORDER BY " + col + " " + dir + nulls + ", openalex_id", nil
}

// ParseFilters parses the official-API-like "key:value,key:value" syntax.
// Values can be URL-escaped when they contain commas.
func ParseFilters(s string) ([]Filter, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]Filter, 0, len(parts))
	for _, p := range parts {
		k, v, ok := strings.Cut(p, ":")
		if !ok {
			return nil, fmt.Errorf("openalexcorpus: invalid filter %q (want key:value)", p)
		}
		decoded, err := url.QueryUnescape(v)
		if err != nil {
			return nil, fmt.Errorf("openalexcorpus: decode filter %q: %w", p, err)
		}
		out = append(out, Filter{Key: strings.TrimSpace(k), Value: strings.TrimSpace(decoded)})
	}
	return out, nil
}

func normalizeOpenAlexDOI(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "doi:")
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "https://dx.doi.org/", "http://dx.doi.org/"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	if s == "" {
		return ""
	}
	return "https://doi.org/" + s
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "t", "1", "yes":
		return true, nil
	case "false", "f", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool %q", s)
	}
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
