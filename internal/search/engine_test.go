package search

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// fakeProvider is a test Provider: it returns canned hits, can be told to
// fail (contract-style, recording via BaseProvider) or to panic outright.
type fakeProvider struct {
	BaseProvider
	name  string
	hits  []Hit
	err   error
	panic bool
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Search(_ context.Context, _ SearchEntry) ([]Hit, error) {
	if f.panic {
		panic("fakeProvider: boom")
	}
	if f.err != nil {
		return f.RecordFailure(f.err)
	}
	return f.hits, nil
}

// fakeMinter records ResolveOrMint / UpdateStatus calls without a DB.
type fakeMinter struct {
	refs        []registry.PaperRef
	statuses    []string
	nextID      int
	resolveErr  error
	getErr      error
	getCalls    int
	paperStatus string
	createdFor  func(ref registry.PaperRef) bool
}

func (m *fakeMinter) ResolveOrMint(_ context.Context, ref registry.PaperRef) (string, bool, error) {
	m.refs = append(m.refs, ref)
	if m.resolveErr != nil {
		return "", false, m.resolveErr
	}
	m.nextID++
	created := m.createdFor == nil || m.createdFor(ref)
	return "qa_fake" + string(rune('a'+m.nextID-1)), created, nil
}

func (m *fakeMinter) UpdateStatus(_ context.Context, _ string, status string) (bool, error) {
	m.statuses = append(m.statuses, status)
	return true, nil
}

func (m *fakeMinter) Get(_ context.Context, paperID string) (*registry.Paper, bool, error) {
	m.getCalls++
	if m.getErr != nil {
		return nil, false, m.getErr
	}
	status := m.paperStatus
	if status == "" {
		status = "ready"
	}
	return &registry.Paper{PaperID: paperID, Status: status}, true, nil
}

func TestNormalize(t *testing.T) {
	e := SearchEntry{Text: "  hello  ", MaxResults: 0, RequiredPhrases: []string{" a ", "", "b"}}
	e.Normalize()
	if e.Text != "hello" || e.MaxResults != DefaultMaxResults {
		t.Fatalf("unexpected normalize: %+v", e)
	}
	if !slices.Equal(e.RequiredPhrases, []string{"a", "b"}) {
		t.Fatalf("required phrases not trimmed: %v", e.RequiredPhrases)
	}
	e.MaxResults = 500
	e.Normalize()
	if e.MaxResults != MaxResultsCap {
		t.Fatalf("max results not capped: %d", e.MaxResults)
	}
}

func TestFanOutMergeAndIsolation(t *testing.T) {
	good1 := &fakeProvider{name: "p1", hits: []Hit{
		{DOI: "10.1000/ABC", Title: "Paper A", Score: 0.8, Source: "p1"},
		{ArxivID: "2401.12345v2", Title: "Paper B", Score: 0.6, Source: "p1"},
	}}
	good2 := &fakeProvider{name: "p2", hits: []Hit{
		{DOI: "10.1000/abc", Title: "Paper A (dup)", Abstract: "Merged abstract", Authors: []string{"Alice"}, Year: 2024, Score: 0.9, Source: "p2"},
		{Title: "Title Only", Score: 0.3, Source: "p2"},
	}}
	failing := &fakeProvider{name: "p3", err: errors.New("backend down")}
	panicking := &fakeProvider{name: "p4", panic: true}

	eng := NewEngine(nil, good1, good2, failing, panicking)
	resp, err := eng.Search(context.Background(), SearchEntry{Text: "q"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if failing.LastError() == nil {
		t.Fatal("failing provider did not record its error")
	}

	// DOI dedup: two providers, one merged hit, max score, union sources.
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(resp.Results), resp.Results)
	}
	top := resp.Results[0]
	if top.Hit.DOI != "10.1000/abc" || top.Hit.Score != 0.9 || top.Hit.Source != "p1,p2" {
		t.Fatalf("bad merged doi hit: %+v", top.Hit)
	}
	if top.Hit.Title != "Paper A" {
		t.Fatalf("first non-empty title should win, got %q", top.Hit.Title)
	}
	if top.Hit.Abstract != "Merged abstract" || len(top.Hit.Authors) != 1 || top.Hit.Authors[0] != "Alice" || top.Hit.Year != 2024 {
		t.Fatalf("merged enrichment missing: %+v", top.Hit)
	}
	// arXiv version suffix normalized away.
	second := resp.Results[1]
	if second.Hit.ArxivID != "2401.12345" {
		t.Fatalf("arxiv id not normalized: %q", second.Hit.ArxivID)
	}
	// Nil registry: nothing anchored, nothing created.
	if top.PaperID != "" || top.Created {
		t.Fatalf("nil registry should not mint: %+v", top)
	}

	// Title-only hit lands in candidates, not results, and never mints.
	if len(resp.Candidates) != 1 || resp.Candidates[0].Title != "Title Only" {
		t.Fatalf("bad candidates: %+v", resp.Candidates)
	}
}

func TestMaxResultsCapAndCandidatesCap(t *testing.T) {
	var hits []Hit
	for i := 0; i < 8; i++ {
		hits = append(hits, Hit{DOI: "10.1000/" + string(rune('a'+i)), Score: float64(i), Source: "p1"})
	}
	for i := 0; i < 8; i++ {
		hits = append(hits, Hit{Title: "t" + string(rune('a'+i)), Source: "p1"})
	}
	eng := NewEngine(nil, &fakeProvider{name: "p1", hits: hits})
	resp, err := eng.Search(context.Background(), SearchEntry{Text: "q", MaxResults: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results not capped at 3: %d", len(resp.Results))
	}
	if len(resp.Candidates) != MaxCandidates {
		t.Fatalf("candidates not capped at %d: %d", MaxCandidates, len(resp.Candidates))
	}
	// Results ordered by score desc.
	if resp.Results[0].Hit.Score < resp.Results[1].Hit.Score {
		t.Fatal("results not ordered by score desc")
	}
}

func TestMintingPath(t *testing.T) {
	mint := &fakeMinter{}
	eng := &Engine{
		providers: []Provider{&fakeProvider{name: "p1", hits: []Hit{
			{DOI: "10.1000/x", Title: "New Paper", Score: 1.0, Source: "p1"},
			{Title: "No Identity", Score: 0.5, Source: "p1"},
		}}},
		reg: mint,
	}
	resp, err := eng.Search(context.Background(), SearchEntry{Text: "q"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Created || resp.Results[0].PaperID == "" {
		t.Fatalf("expected one created result: %+v", resp.Results)
	}
	if len(mint.refs) != 1 || mint.refs[0].DOI != "10.1000/x" {
		t.Fatalf("bad mint refs: %+v", mint.refs)
	}
	// Never create pending rows: both startup ingestion recovery and the
	// downloader's periodic adoption would otherwise acquire unselected hits.
	if len(mint.statuses) != 0 {
		t.Fatalf("search changed acquisition status: %v", mint.statuses)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("title-only hit should be a candidate, not minted: %+v", resp.Candidates)
	}
}

func TestExistingPaperNotDemoted(t *testing.T) {
	mint := &fakeMinter{createdFor: func(registry.PaperRef) bool { return false }}
	eng := &Engine{
		providers: []Provider{&fakeProvider{name: "p1", hits: []Hit{
			{ArxivID: "2401.00001", Score: 1.0, Source: "p1"},
		}}},
		reg: mint,
	}
	resp, err := eng.Search(context.Background(), SearchEntry{Text: "q"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Created {
		t.Fatalf("expected existing paper: %+v", resp.Results)
	}
	if len(mint.statuses) != 0 {
		t.Fatalf("existing paper must not be re-demoted: %v", mint.statuses)
	}
}

// MintHits is shared by multi, survey, agentic, and ranked search, while
// Search calls it after provider fan-out. Exercise both entry points so remote
// modes cannot bypass the metadata-only admission boundary.
func TestSearchAnchoringNeverChangesAcquisition(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, status := range []string{"new", "pending", "ready", "failed"} {
			name := status
			if direct {
				name += "/MintHits"
			} else {
				name += "/Search"
			}
			t.Run(name, func(t *testing.T) {
				mint := &fakeMinter{
					paperStatus: status,
					createdFor:  func(registry.PaperRef) bool { return status == "new" },
					getErr:      errors.New("acquisition inspection must not run"),
				}
				hits := []Hit{{DOI: "10.1000/selected-later", Title: "Metadata", Authors: []string{"Alice"}, Year: 2026}, {Title: "Candidate"}}
				eng := &Engine{reg: mint, providers: []Provider{&fakeProvider{name: "test", hits: hits}}}
				for range 2 { // repeated searches do not retry or reset existing work
					var results []Result
					var candidates []Hit
					var err error
					if direct {
						results, candidates, err = eng.MintHits(context.Background(), hits, 10)
					} else {
						var resp Response
						resp, err = eng.Search(context.Background(), SearchEntry{Text: "q"})
						results, candidates = resp.Results, resp.Candidates
					}
					if err != nil || len(results) != 1 || len(candidates) != 1 {
						t.Fatalf("results=%+v candidates=%+v err=%v", results, candidates, err)
					}
					if results[0].PaperID == "" || results[0].Created != (status == "new") {
						t.Fatalf("lost metadata anchor: %+v", results[0])
					}
				}
				if len(mint.statuses) != 0 || mint.getCalls != 0 || mint.paperStatus != status {
					t.Fatalf("search touched acquisition: statuses=%v reads=%d status=%s", mint.statuses, mint.getCalls, mint.paperStatus)
				}
				if len(mint.refs) != 2 || mint.refs[0].Title != "Metadata" || mint.refs[0].Year != 2026 || len(mint.refs[0].Authors) != 1 {
					t.Fatalf("metadata enrichment lost: %+v", mint.refs)
				}
			})
		}
	}
}

func TestExistingPendingPaperRemainsUntouched(t *testing.T) {
	mint := &fakeMinter{
		createdFor:  func(registry.PaperRef) bool { return false },
		paperStatus: "pending",
	}
	eng := &Engine{
		providers: []Provider{&fakeProvider{name: "p1", hits: []Hit{
			{DOI: "10.3788/cjl221209", Score: 1.0, Source: "p1"},
		}}},
		reg: mint,
	}
	resp, err := eng.Search(context.Background(), SearchEntry{Text: "q"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Created {
		t.Fatalf("expected existing pending paper: %+v", resp.Results)
	}
	if len(mint.statuses) != 0 {
		t.Fatalf("existing pending paper must not be re-demoted: %v", mint.statuses)
	}
	if mint.getCalls != 0 {
		t.Fatalf("search inspected pending paper for resubmission: %d", mint.getCalls)
	}
}
