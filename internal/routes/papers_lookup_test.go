package routes

import (
	"context"
	"reflect"
	"testing"
)

func TestParseLookupIDs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"arxiv:2208.06941", []string{"arxiv:2208.06941"}},
		{"arxiv:2208.06941, openalex:W123 , doi:10.1/x", []string{"arxiv:2208.06941", "openalex:W123", "doi:10.1/x"}},
		{"a:1,a:1,b:2", []string{"a:1", "b:2"}}, // de-dupe, preserve order
		{",,a:1,", []string{"a:1"}},
	}
	for _, tc := range cases {
		if got := parseLookupIDs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parseLookupIDs(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSplitRef(t *testing.T) {
	cases := []struct {
		ref      string
		kind, id string
		ok       bool
	}{
		{"arxiv:2208.06941", "arxiv", "2208.06941", true},
		{"openalex:W4406693713", "openalex", "W4406693713", true},
		{"doi:10.22331/q-2023-03-20-955", "doi", "10.22331/q-2023-03-20-955", true},
		{"arxiv:quant-ph/0811.3171", "arxiv", "quant-ph/0811.3171", true},
		{"ARXIV:2208.06941", "arxiv", "2208.06941", true}, // case-insensitive kind
		{"unknown:x", "", "", false},
		{"noscheme", "", "", false},
		{"arxiv:", "", "", false},
		{":bare", "", "", false},
	}
	for _, tc := range cases {
		kind, id, ok := splitRef(tc.ref)
		if ok != tc.ok || kind != tc.kind || id != tc.id {
			t.Fatalf("splitRef(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.ref, kind, id, ok, tc.kind, tc.id, tc.ok)
		}
	}
}

func TestStripArxivVersion(t *testing.T) {
	cases := map[string]string{
		"2208.06941":           "2208.06941",
		"2208.06941v1":         "2208.06941",
		"2208.06941v12":        "2208.06941",
		"quant-ph/0811.3171v2": "quant-ph/0811.3171",
	}
	for in, want := range cases {
		if got := stripArxivVersion(in); got != want {
			t.Fatalf("stripArxivVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestYearFromPubDate(t *testing.T) {
	cases := map[string]int{
		"2022-06-15": 2022,
		"2022":       2022,
		"":           0,
		"abc":        0,
		"20":         0,
	}
	for in, want := range cases {
		if got := yearFromPubDate(in); got != want {
			t.Fatalf("yearFromPubDate(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCatalogHostedNilCatalogIsFalse(t *testing.T) {
	if catalogHosted(context.Background(), nil, "arxiv", "2208.06941") {
		t.Fatal("nil catalog must report not-hosted")
	}
}
