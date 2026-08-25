package registry

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// seedSyncStore returns a LocalStore preloaded with a multi-shard
// fixture covering arXiv shards and the DOI pseudo-shard for pdf and
// markdown.
func seedSyncStore(t *testing.T) objstore.Store {
	t.Helper()
	s, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	keys := []string{
		"pdf/0001/0001001v1.pdf",
		"pdf/0002/0002001v2.pdf",
		"pdf/doi/10.9999/qatlas.test.1.pdf",
		"markdown/0001/0001001v1.md",
		"markdown/doi/10.9999/qatlas.test.1.md",
	}
	ctx := context.Background()
	for _, k := range keys {
		if _, err := s.Put(ctx, k, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	return s
}

// TestListKindPathsPerShard verifies the shard-first listing (ListDirs +
// per-shard ListPrefix) produces exactly the map the old single
// whole-prefix listing produced.
func TestListKindPathsPerShard(t *testing.T) {
	ctx := context.Background()
	store := seedSyncStore(t)

	got, err := listKindPaths(ctx, store, "pdf")
	if err != nil {
		t.Fatalf("listKindPaths: %v", err)
	}
	want := map[string]string{
		"0001001v1":                         "0001/0001001v1.pdf",
		"0002001v2":                         "0002/0002001v2.pdf",
		DOINodeKey("10.9999/qatlas.test.1"): "doi/10.9999/qatlas.test.1.pdf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listKindPaths pdf = %v, want %v", got, want)
	}

	// Equivalence with the old behavior: one flat ListPrefix parsed by
	// the same stemFromKey rules.
	infos, err := store.ListPrefix(ctx, "pdf/", 0)
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	flat := map[string]string{}
	for _, info := range infos {
		stem, isDOI, ok := stemFromKey(info.Key, "pdf")
		if !ok {
			continue
		}
		key := stem
		if isDOI {
			key = DOINodeKey(stem)
		}
		flat[key] = bucketRelKey(info.Key)
	}
	if !reflect.DeepEqual(got, flat) {
		t.Errorf("per-shard listing %v diverges from flat listing %v", got, flat)
	}
}

// TestListKindPathsFailingShardPropagates proves an error on ANY shard
// aborts the listing — partial results must never reach the merge step.
// The failing shards here contain no child directories, so drill-down
// cannot decompose them and the error must surface.
func TestListKindPathsFailingShardPropagates(t *testing.T) {
	shrinkBackoff(t)
	ctx := context.Background()
	boom := errors.New("rustfs: timeout awaiting response headers")
	fake := &fakeListStore{
		infos: []objstore.ObjectInfo{
			{Key: "pdf/0001/0001001v1.pdf"},
			{Key: "pdf/0002/0002001v2.pdf"},
		},
		failPrefix: "pdf/0002/",
		failErr:    boom,
		failTimes:  -1,
	}
	if _, err := listKindPaths(ctx, fake, "pdf"); !errors.Is(err, boom) {
		t.Errorf("listKindPaths err = %v, want wrapped boom", err)
	}
}

// TestListKindPathsRetryThenSucceed covers transient timeouts: the
// first ListPrefix attempt fails, the retry succeeds, no drill-down.
func TestListKindPathsRetryThenSucceed(t *testing.T) {
	shrinkBackoff(t)
	ctx := context.Background()
	boom := errors.New("rustfs: transient timeout")
	fake := &fakeListStore{
		infos: []objstore.ObjectInfo{
			{Key: "pdf/2501/2501.00010v1.pdf"},
		},
		failPrefix: "pdf/2501/",
		failErr:    boom,
		failTimes:  1, // fail once, then succeed
	}
	got, err := listKindPaths(ctx, fake, "pdf")
	if err != nil {
		t.Fatalf("listKindPaths: %v", err)
	}
	want := map[string]string{"2501.00010v1": "2501/2501.00010v1.pdf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listKindPaths = %v, want %v", got, want)
	}
}

// TestListPrefixResilientMaxDepthPropagates: at maxDepth 0 there is no
// drill-down — a failing leaf must surface its error.
func TestListPrefixResilientMaxDepthPropagates(t *testing.T) {
	shrinkBackoff(t)
	ctx := context.Background()
	boom := errors.New("rustfs: timeout")
	fake := &fakeListStore{
		infos:      []objstore.ObjectInfo{{Key: "images/0906/0906.0016v2/img-0.png"}},
		failPrefix: "images/0906/0906.0016v2/",
		failErr:    boom,
		failTimes:  -1,
	}
	if _, err := listPrefixResilient(ctx, fake, "images/0906/0906.0016v2/", 0); !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}

// TestSyncParsingRealLayouts locks in the layouts the production buckets
// actually hold: old-style category pdf and new-style pdf.
func TestSyncParsingRealLayouts(t *testing.T) {
	ctx := context.Background()
	s, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	keys := []string{
		"pdf/0001/quant-ph/0001001v1.pdf", // old-style canonical
		"pdf/2501/2501.00010v1.pdf",       // new-style
	}
	for _, k := range keys {
		if _, err := s.Put(ctx, k, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	pdfs, err := listKindPaths(ctx, s, "pdf")
	if err != nil {
		t.Fatalf("listKindPaths: %v", err)
	}
	wantPDFs := map[string]string{
		"quant-ph/0001001v1": "0001/quant-ph/0001001v1.pdf",
		"2501.00010v1":       "2501/2501.00010v1.pdf",
	}
	if !reflect.DeepEqual(pdfs, wantPDFs) {
		t.Errorf("listKindPaths pdf = %v, want %v", pdfs, wantPDFs)
	}
}

// shrinkBackoff replaces the production 2s/5s retry backoff with
// milliseconds for the duration of a test.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	orig := listRetryBackoff
	listRetryBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond}
	t.Cleanup(func() { listRetryBackoff = orig })
}

// TestFilterShards covers the --shard-range semantics: inclusive
// lexicographic bounds, open-ended sides, and the doi pseudo-shard
// always passing when its kind runs.
func TestFilterShards(t *testing.T) {
	shards := []string{"pdf/0001/", "pdf/0912/", "pdf/2501/", "pdf/doi/"}
	cases := []struct {
		name     string
		from, to string
		want     []string
	}{
		{"no range keeps all", "", "", shards},
		{"closed range", "0001", "0912", []string{"pdf/0001/", "pdf/0912/", "pdf/doi/"}},
		{"open from", "", "0912", []string{"pdf/0001/", "pdf/0912/", "pdf/doi/"}},
		{"open to", "0912", "", []string{"pdf/0912/", "pdf/2501/", "pdf/doi/"}},
		{"single shard", "2501", "2501", []string{"pdf/2501/", "pdf/doi/"}},
		{"range excludes all numeric", "0002", "0003", []string{"pdf/doi/"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterShards("pdf", shards, c.from, c.to)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("filterShards(%q:%q) = %v, want %v", c.from, c.to, got, c.want)
			}
		})
	}
}

// TestOrderedKinds covers the --kinds semantics: empty = both kinds in
// canonical merge order, subsets are reordered canonically, unknown
// kinds (including the dropped "images" stage) are rejected.
func TestOrderedKinds(t *testing.T) {
	all := []string{"pdf", "markdown"}
	got, err := orderedKinds(nil)
	if err != nil || !reflect.DeepEqual(got, all) {
		t.Errorf("orderedKinds(nil) = %v, %v; want %v, nil", got, err, all)
	}
	got, err = orderedKinds([]string{"markdown", "pdf"})
	if err != nil || !reflect.DeepEqual(got, []string{"pdf", "markdown"}) {
		t.Errorf("orderedKinds(markdown,pdf) = %v, %v; want [pdf markdown], nil", got, err)
	}
	got, err = orderedKinds([]string{"markdown"})
	if err != nil || !reflect.DeepEqual(got, []string{"markdown"}) {
		t.Errorf("orderedKinds(markdown) = %v, %v; want [markdown], nil", got, err)
	}
	if _, err = orderedKinds([]string{"pdf", "json"}); err == nil {
		t.Errorf("orderedKinds(pdf,json) should fail")
	}
	if _, err = orderedKinds([]string{"images"}); err == nil {
		t.Errorf("orderedKinds(images) should fail (images stage dropped)")
	}
}
