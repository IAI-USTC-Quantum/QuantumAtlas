package openalexcorpus

import (
	"strings"
	"testing"
)

func TestNormalizeOpenAlexDOI(t *testing.T) {
	cases := map[string]string{
		"10.7717/peerj.4375":              "https://doi.org/10.7717/peerj.4375",
		"doi:10.7717/PEERJ.4375":          "https://doi.org/10.7717/peerj.4375",
		"https://doi.org/10.7717/peerj.1": "https://doi.org/10.7717/peerj.1",
		"":                                "",
	}
	for in, want := range cases {
		if got := normalizeOpenAlexDOI(in); got != want {
			t.Errorf("normalizeOpenAlexDOI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFilters(t *testing.T) {
	got, err := ParseFilters("type:article,from_publication_year:2020,primary_topic.id:https%3A%2F%2Fopenalex.org%2FT1")
	if err != nil {
		t.Fatalf("ParseFilters: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(filters) = %d, want 3", len(got))
	}
	if got[2].Key != "primary_topic.id" || got[2].Value != "https://openalex.org/T1" {
		t.Errorf("decoded filter = %#v", got[2])
	}
}

func TestBuildWhereUsesWhitelistedSQL(t *testing.T) {
	where, args, err := buildWhere(QueryOptions{
		Search: "quantum error correction",
		Filters: []Filter{
			{Key: "type", Value: "article"},
			{Key: "has_arxiv", Value: "true"},
			{Key: "cites", Value: "https://openalex.org/W1"},
		},
	})
	if err != nil {
		t.Fatalf("buildWhere: %v", err)
	}
	for _, must := range []string{
		"search_text @@ plainto_tsquery",
		"work_type = $2",
		"arxiv_id IS NOT NULL",
		"openalex_referenced_work_ids ? $3",
	} {
		if !strings.Contains(where, must) {
			t.Errorf("WHERE missing %q in %s", must, where)
		}
	}
	if len(args) != 3 || args[0] != "quantum error correction" || args[1] != "article" || args[2] != "W1" {
		t.Errorf("args = %#v, want search/article/W1", args)
	}
}

func TestBuildWhereRejectsUnsupportedFilter(t *testing.T) {
	_, _, err := buildWhere(QueryOptions{Filters: []Filter{{Key: "unsafe_sql", Value: "x"}}})
	if err == nil {
		t.Fatal("buildWhere accepted unsupported filter")
	}
}

func TestOrderBySQL(t *testing.T) {
	got, err := orderBySQL("cited_by_count:desc", false)
	if err != nil {
		t.Fatalf("orderBySQL: %v", err)
	}
	if !strings.Contains(got, "ORDER BY cited_by_count DESC NULLS LAST, openalex_id") {
		t.Errorf("orderBySQL = %q", got)
	}
	if _, err := orderBySQL("record;drop:desc", false); err == nil {
		t.Fatal("orderBySQL accepted unsupported sort")
	}
}
