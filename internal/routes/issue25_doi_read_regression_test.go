package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// A real DOI-only registry row has an identity mapping and a PaperDetail,
// unlike the missing-identity fixture in the older DOI fallback tests.
// Ready is catalog metadata, not evidence of a readable DOI object or an
// arXiv identity. In particular an asset version cannot manufacture an id.
func issue25DOIOnlyCatalog(doi string) *fakePaperCatalog {
	c := newFakePaperCatalog()
	const pid = "qa_issue25_doi_only"
	c.doiRows[doi] = true
	c.identity["doi:"+doi] = pid
	c.papers[pid] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: pid, DOI: doi, Status: "ready"},
		Assets: []registry.Asset{{AssetID: 25, Source: "arxiv", ArxivVersion: 1}},
	}
	return c
}

// These fixtures intentionally reproduce the reported ready/empty-metadata
// shape. They are not snapshots of current production (which may since have
// changed acquisition state or merged an identity).
func TestIssue25DOIOnlyDoesNotResolveToEmptyArxiv(t *testing.T) {
	for _, doi := range []string{"10.1145/3530258", "10.1145/3488559"} {
		t.Run(doi, func(t *testing.T) {
			outcome, twin, err := decideLocalDOIServing(context.Background(), issue25DOIOnlyCatalog(doi), storeWithKeys(), doi)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == doiServeArxiv {
				// Capture the exact reported failure produced by the next
				// dispatch step, rather than accepting a fabricated arxiv id.
				rec := httptest.NewRecorder()
				re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/api/papers/"+doi+"/markdown", nil), rec)
				if err := markdownHandler(re, &config.Config{}, storeWithKeys(), nil, twin); err != nil {
					t.Fatal(err)
				}
				t.Fatalf("DOI-only paper routed to arxiv %q: HTTP %d %s", twin, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
			if outcome != doiServeDefer || twin != "" {
				t.Fatalf("got (%v, %q), want defer to DOI resolution without arxiv fallback", outcome, twin)
			}
		})
	}
}

func TestIssue25MergedDOIUsesExplicitSurvivingAssetIdentity(t *testing.T) {
	const doi = "10.1145/3488559"
	catalog := issue25DOIOnlyCatalog(doi)
	catalog.papers["qa_issue25_doi_only"].Paper.Status = "merged_into:qa_issue25_survivor"
	catalog.papers["qa_issue25_survivor"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_issue25_survivor", ArxivID: "2109.06917", Status: "ready", Title: "Open Problems Related to Quantum Query Complexity"},
		Assets: []registry.Asset{{AssetID: 26, Source: "arxiv", ArxivVersion: 1, PDFPath: "2109/2109.06917v1.pdf"}},
	}
	outcome, twin, err := decideLocalDOIServing(context.Background(), catalog, storeWithKeys(), doi)
	if err != nil || outcome != doiServeArxiv || twin != "2109.06917v1" {
		t.Fatalf("merged DOI target = (%v, %q, %v), want registered survivor arxiv identity", outcome, twin, err)
	}
	// The qa_ spelling must follow the same explicit merge, too.
	target, err := resolvePaperAssetTarget(context.Background(), catalog, "qa_issue25_doi_only")
	if err != nil || target.ArxivVersioned != twin || target.DOI != "" {
		t.Fatalf("merged qa_ target = (%+v, %v)", target, err)
	}
	// Identifier metadata must render the same survivor, not an empty
	// title/authors tombstone. No external metadata is invented here.
	handled, rec, body := dispatchDetail(t, catalog, doi)
	if !handled || rec.Code != http.StatusOK || body["paper_id"] != "qa_issue25_survivor" || body["arxiv_id"] != "2109.06917" || body["title"] != catalog.papers["qa_issue25_survivor"].Paper.Title {
		t.Fatalf("merged DOI detail: handled=%v HTTP %d %s", handled, rec.Code, rec.Body.String())
	}
	if catalog.papers["qa_issue25_doi_only"].Paper.ArxivID != "" {
		t.Fatal("reading mutated the tombstone's identity")
	}
}

func TestIssue25MergedDOICycleFailsWithoutFallback(t *testing.T) {
	const doi = "10.1145/3488559"
	catalog := issue25DOIOnlyCatalog(doi)
	catalog.papers["qa_issue25_doi_only"].Paper.Status = "merged_into:qa_issue25_doi_only"
	outcome, twin, err := decideLocalDOIServing(context.Background(), catalog, storeWithKeys(), doi)
	if err == nil || outcome != doiServeDefer || twin != "" {
		t.Fatalf("cycle target = (%v, %q, %v), want explicit error without fallback", outcome, twin, err)
	}
}

func TestIssue25InvalidMergeCannotFallBackToTombstone(t *testing.T) {
	for _, target := range []string{"", "2109.06917", "qa_", "qa_missing", "qa_bad/child", "qa_bad "} {
		t.Run(target, func(t *testing.T) {
			const doi = "10.1145/3488559"
			catalog := issue25DOIOnlyCatalog(doi)
			catalog.papers["qa_issue25_doi_only"].Paper.Status = "merged_into:" + target
			outcome, twin, err := decideLocalDOIServing(context.Background(), catalog, storeWithKeys(), doi)
			if err == nil || outcome != doiServeDefer || twin != "" {
				t.Fatalf("invalid target %q: got (%v, %q, %v), want error without fallback", target, outcome, twin, err)
			}
			handled, rec, _ := dispatchDetail(t, catalog, doi)
			if !handled || rec.Code != http.StatusInternalServerError {
				t.Fatalf("invalid merge metadata: handled=%v HTTP %d %s", handled, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIssue25DOIStorageErrorDoesNotSelectArxiv(t *testing.T) {
	const doi = "10.1145/3530258"
	catalog := issue25DOIOnlyCatalog(doi)
	catalog.papers["qa_issue25_doi_only"].Paper.ArxivID = "2109.06917"
	store := &issue25ReadStore{readErr: errors.New("unavailable")}
	outcome, twin, err := decideLocalDOIServing(context.Background(), catalog, store, doi)
	if !errors.Is(err, objstore.ErrUnavailable) || outcome != doiServeDefer || twin != "" {
		t.Fatalf("store error yielded (%v, %q, %v), want unavailable without arxiv fallback", outcome, twin, err)
	}
}

func TestIssue25DOIObjectsWithoutPublishedRowUseDOINamespace(t *testing.T) {
	const doi = "10.1145/3530258"
	for _, kind := range []string{"markdown", "pdf"} {
		catalog := issue25DOIOnlyCatalog(doi)
		store := storeWithKeys(paperassets.DOIAssetKey(kind, doi))
		outcome, twin, err := decideLocalDOIServing(context.Background(), catalog, store, doi)
		if err != nil || outcome != doiServeDOI || twin != "" {
			t.Fatalf("%s local DOI bytes yielded (%v, %q, %v)", kind, outcome, twin, err)
		}
	}
}

// Fail only the second-stage registry lookup: the initial DOI row probe
// succeeded, but this must not be mistaken for a definite missing twin.
type issue25LookupFailure struct {
	*fakePaperCatalog
	failDetail bool
}

func (c issue25LookupFailure) GetPaperIDByIdentity(ctx context.Context, scheme, id string) (string, bool, error) {
	if !c.failDetail {
		return "", false, registry.ErrCatalogUnavailable
	}
	return c.fakePaperCatalog.GetPaperIDByIdentity(ctx, scheme, id)
}

func (c issue25LookupFailure) GetWithAssets(context.Context, string) (*registry.PaperDetail, bool, error) {
	return nil, false, registry.ErrCatalogUnavailable
}

func TestIssue25DOITwinLookupFailureIsNotAMiss(t *testing.T) {
	const doi = "10.1145/3530258"
	for _, failDetail := range []bool{false, true} {
		c := issue25LookupFailure{fakePaperCatalog: issue25DOIOnlyCatalog(doi), failDetail: failDetail}
		outcome, twin, err := decideLocalDOIServing(context.Background(), c, storeWithKeys(), doi)
		if !errors.Is(err, registry.ErrCatalogUnavailable) || outcome != doiServeDefer || twin != "" {
			t.Fatalf("failDetail=%v: got (%v, %q, %v), want catalog error without fallback", failDetail, outcome, twin, err)
		}
	}
}

// Reads are recorded so successful and missing-object tests can prove that
// only DOI keys were consulted, never a made-up or unrelated arXiv identity.
type issue25ReadStore struct {
	canonicalNoopStore
	objects map[string][]byte
	readErr error
	mu      sync.Mutex
	reads   []string
}

func (s *issue25ReadStore) record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, key)
}

func (s *issue25ReadStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	s.record(key)
	b, ok := s.objects[key]
	return objstore.ObjectInfo{Key: key, Size: int64(len(b))}, ok, s.readErr
}

func (s *issue25ReadStore) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	s.record(key)
	if s.readErr != nil {
		return nil, objstore.ObjectInfo{}, s.readErr
	}
	b, ok := s.objects[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, objstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), objstore.ObjectInfo{Key: key, Size: int64(len(b))}, nil
}

func TestIssue25DOIAssetDispatchHTTP(t *testing.T) {
	// Exercise the real DOI handler dispatcher and net/http transport, not
	// just a recorder. The outer PB authorization/catch-all is not replaced
	// or tested here; its local DOI decision is covered separately above.
	for _, doi := range []string{"10.1145/3530258", "10.1145/3488559"} {
		for _, scenario := range []string{"cached", "missing", "storage failure"} {
			t.Run(doi+"/"+scenario, func(t *testing.T) {
				name := strings.Repeat("a", 64) + ".png"
				markdown := []byte("# DOI-only fixture\n\n![](images/" + name + ")\n\nFig. 1: Fixture\n")
				image := []byte("issue25 image bytes")
				images, err := mineru.BuildImagesZip(map[string][]byte{"images/" + name: image})
				if err != nil {
					t.Fatal(err)
				}
				store := &issue25ReadStore{objects: map[string][]byte{}}
				if scenario == "cached" {
					store.objects[paperassets.DOIAssetKey("markdown", doi)] = markdown
					store.objects[paperassets.DOIAssetKey("pdf", doi)] = []byte("%PDF-1.7 fixture")
					store.objects[paperassets.DOIAssetKey("images", doi)] = images
				} else if scenario == "storage failure" {
					store.readErr = errors.New("issue25 storage unavailable")
				}
				catalog := issue25DOIOnlyCatalog(doi)
				catalog.publishedAssets[doi] = true
				catalog.papers["qa_issue25_doi_only"].Assets = []registry.Asset{{AssetID: 25, Source: "published"}}
				outcome, twin, err := decideLocalDOIServing(context.Background(), catalog, store, doi)
				if err != nil || outcome != doiServeDOI || twin != "" {
					t.Fatalf("published DOI decision = (%v, %q, %v)", outcome, twin, err)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					action := strings.TrimPrefix(req.URL.Path, "/api/papers/"+doi+"/")
					statusKind := ""
					if kind, ok := strings.CutSuffix(action, "/status"); ok {
						action, statusKind = "status", kind
					}
					re := newTestReqEvent(req, w)
					applyDOICanonicalHeaders(re, doi, doi, "")
					if err := dispatchGETDOIHandlers(re, &config.Config{}, store, catalog, nil, doi, action, statusKind, doi+"/"+action, ""); err != nil {
						t.Errorf("DOI dispatch: %v", err)
					}
				}))
				defer srv.Close()

				for _, action := range []string{"markdown", "markdown/status", "pdf", "pdf/status", "images/zip", "images/" + name, "figures"} {
					resp, err := srv.Client().Get(srv.URL + "/api/papers/" + doi + "/" + action)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusOK
					switch {
					case action == "pdf":
						want = http.StatusGone // legacy PDF delivery remains disabled
					case scenario == "storage failure":
						want = http.StatusInternalServerError
						if action == "markdown" || strings.HasSuffix(action, "/status") {
							want = http.StatusServiceUnavailable
						}
					case scenario == "missing" && (action == "markdown" || strings.HasPrefix(action, "images/")):
						want = http.StatusNotFound
					}
					if resp.StatusCode != want {
						t.Errorf("%s: HTTP %d %s, want %d", action, resp.StatusCode, body, want)
					}
					if got := resp.Header.Get("X-QAtlas-Resolved-Id"); got != doi {
						t.Errorf("%s resolved id = %q, want DOI %q", action, got, doi)
					}
					if bytes.Contains(body, []byte("invalid arxiv_id")) || bytes.Contains(body, []byte(`"arxiv_id"`)) {
						t.Errorf("%s leaked arxiv identity: %s", action, body)
					}
					if scenario == "cached" {
						expectedBytes := map[string][]byte{"markdown": markdown, "images/zip": images, "images/" + name: image}[action]
						if expectedBytes != nil && !bytes.Equal(body, expectedBytes) {
							t.Errorf("%s did not return this DOI's exact bytes", action)
						}
					}
					if strings.HasSuffix(action, "/status") && scenario != "storage failure" {
						var status map[string]any
						if err := json.Unmarshal(body, &status); err != nil {
							t.Fatal(err)
						}
						ready := scenario == "cached"
						if status["md_ready"] != ready || status["pdf_ready"] != ready || status["doi"] != doi {
							t.Errorf("%s wrong readiness/identity: %s", action, body)
						}
					}
				}
				store.mu.Lock()
				defer store.mu.Unlock()
				for _, key := range store.reads {
					valid := false
					for _, kind := range []string{"markdown", "pdf", "images"} {
						base := paperassets.DOIAssetKey(kind, doi)
						valid = valid || key == base || (kind == "images" && strings.HasPrefix(key, strings.TrimSuffix(base, ".zip")+"/"))
					}
					if !valid {
						t.Errorf("read outside requested DOI namespace: %q", key)
					}
				}
			})
		}
	}
}
