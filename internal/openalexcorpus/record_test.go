package openalexcorpus

import (
	"encoding/json"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
)

func TestShortID(t *testing.T) {
	cases := map[string]string{
		"https://openalex.org/W2741809807": "W2741809807",
		"W2741809807":                      "W2741809807",
		"https://openalex.org/A5088290527": "A5088290527",
		"":                                 "",
		"  https://openalex.org/W1  ":      "W1",
	}
	for in, want := range cases {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartitionDate(t *testing.T) {
	cases := map[string]string{
		"works/updated_date=2026-05-30/part_000.gz":        "2026-05-30",
		"works/updated_date=2016-06-24/part_0000.gz":       "2016-06-24",
		"works/part_0000.gz":                               "",
		"nonsense":                                         "",
		"works/updated_date=2024-01-01/part_0042.jsonl.gz": "2024-01-01",
	}
	for in, want := range cases {
		if got := PartitionDate(in); got != want {
			t.Errorf("PartitionDate(%q) = %q, want %q", in, got, want)
		}
	}
}

// rawWorkFrom builds a RawWork from a JSON literal the way StreamRawWorks
// does (raw bytes + decoded projection), so the derivation helpers are
// tested on a realistic record shape.
func rawWorkFrom(t *testing.T, js string) RawWork {
	t.Helper()
	raw := json.RawMessage(js)
	var meta openalex.Work
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return RawWork{Record: raw, Meta: meta}
}

func TestRawWorkDerivations(t *testing.T) {
	rw := rawWorkFrom(t, `{
		"id": "https://openalex.org/W42",
		"doi": "https://doi.org/10.1/x",
		"locations": [{"landing_page_url": "https://arxiv.org/abs/2401.12345v2"}],
		"referenced_works": ["https://openalex.org/W1", "https://openalex.org/W2"]
	}`)
	if got, want := rw.OpenAlexID(), "W42"; got != want {
		t.Errorf("OpenAlexID = %q, want %q", got, want)
	}
	if got, want := rw.ArxivID(), "2401.12345"; got != want {
		t.Errorf("ArxivID = %q, want %q", got, want)
	}
	refs := rw.ReferencedIDs()
	if len(refs) != 2 || refs[0] != "W1" || refs[1] != "W2" {
		t.Errorf("ReferencedIDs = %v, want [W1 W2]", refs)
	}
}

func TestRawWorkNoArxivNoRefs(t *testing.T) {
	rw := rawWorkFrom(t, `{"id":"https://openalex.org/W7","locations":[],"referenced_works":[]}`)
	if got := rw.ArxivID(); got != "" {
		t.Errorf("ArxivID = %q, want empty (no arxiv location)", got)
	}
	if refs := rw.ReferencedIDs(); refs != nil {
		t.Errorf("ReferencedIDs = %v, want nil", refs)
	}
}

func TestPgText(t *testing.T) {
	if pgText("") != nil {
		t.Error(`pgText("") must be nil (SQL NULL)`)
	}
	if got := pgText("x"); got == nil || *got != "x" {
		t.Errorf("pgText(\"x\") = %v, want pointer to \"x\"", got)
	}
}
