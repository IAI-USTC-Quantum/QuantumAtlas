package routes

import (
	"archive/zip"
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
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
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

// The current HTTP fixture records full logical reads and identity lookups.
// Old DOI bytes may exist, but only a published complete bundle is ready.
type issue25FrozenReadStore struct {
	objstore.Store
	readErr error
	mu      sync.Mutex
	reads   []string
}

func (s *issue25FrozenReadStore) record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, key)
}
func (s *issue25FrozenReadStore) Stat(ctx context.Context, key string) (objstore.ObjectInfo, bool, error) {
	s.record(key)
	if s.readErr != nil {
		return objstore.ObjectInfo{}, false, s.readErr
	}
	return s.Store.Stat(ctx, key)
}
func (s *issue25FrozenReadStore) Get(ctx context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	s.record(key)
	if s.readErr != nil {
		return nil, objstore.ObjectInfo{}, s.readErr
	}
	return s.Store.Get(ctx, key)
}

type issue25ContentCatalog struct {
	*doiReadingCatalog
	mu      sync.Mutex
	lookups [][2]string
}

func (c *issue25ContentCatalog) GetPaperIDByIdentity(ctx context.Context, scheme, id string) (string, bool, error) {
	c.mu.Lock()
	c.lookups = append(c.lookups, [2]string{scheme, id})
	c.mu.Unlock()
	return c.doiReadingCatalog.GetPaperIDByIdentity(ctx, scheme, id)
}

func TestIssue25DOIAssetDispatchHTTP(t *testing.T) {
	for _, doi := range []string{"10.1145/3530258", "10.1145/3488559"} {
		for _, scenario := range []string{"cached", "missing", "legacy MD only", "storage failure", "disabled"} {
			t.Run(doi+"/"+scenario, func(t *testing.T) {
				dc, base, bundle := newDOIReadingFixture(t)
				dc.doi = doi
				dc.source.Origin = "doi:" + doi
				catalog := &issue25ContentCatalog{doiReadingCatalog: dc}
				pdf, err := readAllFromStore(t, base, dc.source.ObjstoreKey)
				if err != nil {
					t.Fatal(err)
				}
				markdown, err := readAllFromStore(t, base, paperbundle.FileKey(readTestPaper, readTestSource, bundle.RevisionID, bundle.MarkdownPath))
				if err != nil {
					t.Fatal(err)
				}
				const imageName = "nested/figure (1).jpg"
				image, err := readAllFromStore(t, base, paperbundle.FileKey(readTestPaper, readTestSource, bundle.RevisionID, "images/"+imageName))
				if err != nil {
					t.Fatal(err)
				}
				manifest, err := paperbundle.New(base).VerifyBundle(t.Context(), readTestPaper, readTestSource, bundle.RevisionID)
				if err != nil {
					t.Fatal(err)
				}
				images := map[string][]byte{}
				for _, member := range manifest.Files {
					if isBundleImage(member.Path) {
						raw, err := readAllFromStore(t, base, paperbundle.FileKey(readTestPaper, readTestSource, bundle.RevisionID, member.Path))
						if err != nil {
							t.Fatal(err)
						}
						images[member.Path] = raw
					}
				}
				store := &issue25FrozenReadStore{Store: base}
				cfg := &config.Config{PaperAccessEnabled: true}
				switch scenario {
				case "missing":
					dc.omitSources = true
					dc.bundles = map[string]registry.ParseBundle{}
				case "legacy MD only":
					dc.bundles = map[string]registry.ParseBundle{}
					legacy := []byte("FORBIDDEN legacy DOI-only bytes")
					if _, err := base.Put(t.Context(), paperassets.DOIAssetKey("markdown", doi), bytes.NewReader(legacy), int64(len(legacy)), "text/markdown"); err != nil {
						t.Fatal(err)
					}
				case "storage failure":
					store.readErr = errors.New("issue25 private-storage-host unavailable")
				case "disabled":
					cfg.PaperAccessEnabled = false
				}
				// Mount exactly the early convenience-content dispatcher used by GET,
				// before the historical DOI dispatcher can mistake a numeric suffix for
				// an arXiv identity. No PB authorization layer is mocked here.
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					raw := strings.TrimPrefix(req.URL.Path, "/api/papers/")
					re := newTestReqEvent(req, w)
					handled, err := dispatchContentGET(re, cfg, store, catalog, nil, raw)
					if err != nil {
						t.Errorf("early content dispatch: %v", err)
					}
					if !handled {
						t.Errorf("content route was not intercepted: %s", raw)
						http.NotFound(w, req)
					}
				}))
				defer server.Close()
				for _, action := range []string{"read", "read/status", "markdown", "markdown/status", "pdf", "pdf/status", "images/zip", "images/" + imageName, "figures"} {
					response, err := server.Client().Get(server.URL + "/api/papers/" + doi + "/" + action)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					want := 200
					switch scenario {
					case "disabled":
						want = 404
					case "storage failure":
						want = 503
					case "missing":
						want = 404
						if strings.HasSuffix(action, "/status") {
							want = 200
						}
					case "legacy MD only":
						if action != "pdf" && action != "pdf/status" {
							want = 503
							if strings.HasSuffix(action, "/status") {
								want = 200
							}
						}
					}
					if response.StatusCode != want {
						t.Errorf("%s: HTTP %d %s want %d", action, response.StatusCode, body, want)
					}
					if bytes.Contains(body, []byte("invalid arxiv_id")) || bytes.Contains(body, []byte(`"arxiv_id"`)) {
						t.Errorf("%s fabricated arxiv identity: %s", action, body)
					}
					if bytes.Contains(body, []byte("FORBIDDEN legacy")) {
						t.Error("old DOI markdown was served or used as ready")
					}
					if scenario == "cached" || scenario == "legacy MD only" && strings.HasPrefix(action, "pdf") {
						if got := response.Header.Get("X-QAtlas-Requested-Id"); got != doi {
							t.Errorf("%s requested id %q want exact numeric DOI %q", action, got, doi)
						}
						if got := response.Header.Get("X-QAtlas-Resolved-Id"); got != readTestPaper {
							t.Errorf("%s resolved id %q want registered paper %q", action, got, readTestPaper)
						}
						if got := response.Header.Get("X-QAtlas-Source-Id"); got != readTestSource {
							t.Errorf("%s source id %q", action, got)
						}
						expected := map[string][]byte{"markdown": markdown, "images/" + imageName: image, "pdf": pdf}[action]
						if expected != nil && !bytes.Equal(body, expected) {
							t.Errorf("%s did not return exact frozen/published bytes", action)
						}
					}
					if scenario == "cached" && action == "images/zip" {
						archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
						if err != nil {
							t.Fatal(err)
						}
						if len(archive.File) != len(images) {
							t.Fatal("image ZIP member inventory changed")
						}
						for _, member := range archive.File {
							original, found := images[member.Name]
							if !found {
								t.Errorf("image ZIP changed original relative path: %s", member.Name)
								continue
							}
							r, err := member.Open()
							if err != nil {
								t.Fatal(err)
							}
							actual, err := io.ReadAll(r)
							r.Close()
							if err != nil || !bytes.Equal(actual, original) {
								t.Errorf("image ZIP changed %s bytes: %v", member.Name, err)
							}
						}
						if response.Header.Get("X-QAtlas-Artifact-SHA256") != paperbundle.SHA256(body) {
							t.Fatal("download archive SHA not exact-byte pinned")
						}
					}
					if scenario == "missing" && strings.HasSuffix(action, "/status") {
						var status map[string]any
						if err := json.Unmarshal(body, &status); err != nil {
							t.Fatal(err)
						}
						if status["pdf_ready"] != false || status["md_ready"] != false || status["paper_id"] != readTestPaper {
							t.Errorf("missing DOI status fabricated readiness/identity: %s", body)
						}
					}
					if scenario == "storage failure" {
						var problem map[string]any
						if err := json.Unmarshal(body, &problem); err != nil {
							t.Fatal(err)
						}
						if response.Header.Get("Retry-After") == "" || problem["retryable"] != true || problem["code"] != "asset_store_unavailable" || bytes.Contains(body, []byte("private-storage-host")) {
							t.Errorf("%s storage retry/privacy contract: %s", action, body)
						}
					}
					if strings.HasSuffix(action, "/status") && (scenario == "cached" || scenario == "legacy MD only") {
						var status map[string]any
						if err := json.Unmarshal(body, &status); err != nil {
							t.Fatal(err)
						}
						mdReady := scenario == "cached" && !strings.HasPrefix(action, "pdf")
						if status["md_ready"] != mdReady || status["pdf_ready"] != true || status["paper_id"] != readTestPaper || status["source_id"] != readTestSource {
							t.Errorf("%s readiness/identity %s", action, body)
						}
					}
				}
				catalog.mu.Lock()
				for _, lookup := range catalog.lookups {
					if lookup != [2]string{"doi", doi} {
						t.Errorf("numeric DOI reached another identity: %+v", lookup)
					}
				}
				lookups := len(catalog.lookups)
				catalog.mu.Unlock()
				store.mu.Lock()
				defer store.mu.Unlock()
				if scenario == "disabled" {
					if lookups != 0 || len(store.reads) != 0 {
						t.Fatal("master switch off still resolved/read content")
					}
					return
				}
				if lookups == 0 {
					t.Fatal("HTTP dispatcher never resolved numeric DOI identity")
				}
				for _, key := range store.reads {
					if !strings.HasPrefix(key, "content/"+readTestPaper+"/"+readTestSource+"/") && key != paperassets.DOIAssetKey("pdf", doi) {
						t.Errorf("read outside selected immutable/DOI-PDF namespace: %q", key)
					}
				}
			})
		}
	}
}
