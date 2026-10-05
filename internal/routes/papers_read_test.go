package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperread"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

const readTestPaper = "qa_readfixture"
const readTestSource = "src_readfixture"

type readTestCatalog struct {
	contentCatalog
	source                            registry.PaperSource
	bundles                           map[string]registry.ParseBundle
	current                           string
	readyErr                          error
	getCalls, pinnedCalls, readyCalls int
}

func (c *readTestCatalog) Get(_ context.Context, id string) (*registry.Paper, bool, error) {
	c.getCalls++
	if id == readTestPaper {
		return &registry.Paper{PaperID: id, ArxivID: "2501.09999"}, true, nil
	}
	return nil, false, nil
}
func (c *readTestCatalog) GetPaperIDByIdentity(_ context.Context, scheme, id string) (string, bool, error) {
	if scheme == "arxiv" && strings.HasPrefix(id, "2501.09999") {
		return readTestPaper, true, nil
	}
	return "", false, nil
}
func (c *readTestCatalog) GetWithAssets(ctx context.Context, id string) (*registry.PaperDetail, bool, error) {
	p, ok, err := c.Get(ctx, id)
	return &registry.PaperDetail{Paper: p}, ok, err
}
func (c *readTestCatalog) ListPaperSources(_ context.Context, id string) ([]registry.PaperSource, error) {
	if id == readTestPaper && c.source.SourceID != "" {
		return []registry.PaperSource{c.source}, nil
	}
	return nil, nil
}
func (c *readTestCatalog) GetPaperSource(_ context.Context, paper, source string) (registry.PaperSource, bool, error) {
	return c.source, paper == readTestPaper && source == c.source.SourceID, nil
}
func (c *readTestCatalog) ListParseRevisions(_ context.Context, id string) ([]registry.ParseRevision, error) {
	var out []registry.ParseRevision
	for _, b := range c.bundles {
		if b.PaperID == id {
			r := b.ParseRevision
			r.IsCurrent = r.RevisionID == c.current
			out = append(out, r)
		}
	}
	return out, nil
}
func (c *readTestCatalog) GetParseRevision(_ context.Context, paper, revision string) (registry.ParseRevision, bool, error) {
	b, ok := c.bundles[revision]
	return b.ParseRevision, ok && b.PaperID == paper, nil
}
func (c *readTestCatalog) GetParseBundle(_ context.Context, paper, revision string) (registry.ParseBundle, bool, error) {
	c.pinnedCalls++
	b, ok := c.bundles[revision]
	return b, ok && b.PaperID == paper, nil
}
func (c *readTestCatalog) FreezePaperSource(ctx context.Context, store objstore.Store, source registry.PaperSource) (registry.PaperSource, error) {
	_, err := paperbundle.New(store).ReadPDF(ctx, source.PaperID, source.SourceID, source.Sha256, source.SizeBytes)
	return source, err
}
func (c *readTestCatalog) GetReadyParseBundle(ctx context.Context, store objstore.Store, paper, source string) (registry.ParseBundle, bool, error) {
	c.readyCalls++
	if c.readyErr != nil {
		return registry.ParseBundle{}, false, c.readyErr
	}
	b, ok := c.bundles[c.current]
	if !ok || b.PaperID != paper || b.SourceID != source {
		return registry.ParseBundle{}, false, nil
	}
	if _, err := verifiedContentBundle(ctx, store, b, c.source); err != nil {
		if contentBundleCorrupt(err) {
			return registry.ParseBundle{}, false, nil
		}
		return registry.ParseBundle{}, false, err
	}
	return b, true, nil
}
func (c *readTestCatalog) GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error) {
	return registry.PaperSource{}, false, nil
}

type readTestConverter struct {
	ensureCalls, lookupCalls int
	job                      *mineru.Job
}

func (c *readTestConverter) EnsureSource(context.Context, string, string) *mineru.Job {
	c.ensureCalls++
	return c.job
}
func (c *readTestConverter) LookupSource(string, string) (*mineru.Job, bool) {
	c.lookupCalls++
	return c.job, c.job != nil
}

func newReadingFixture(t *testing.T) (*readTestCatalog, objstore.Store, registry.ParseBundle) {
	t.Helper()
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\nimmutable source fixture\n")
	frozen, err := paperbundle.New(store).FreezePDF(t.Context(), readTestPaper, readTestSource, pdf, "")
	if err != nil {
		t.Fatal(err)
	}
	catalog := &readTestCatalog{source: registry.PaperSource{PaperID: readTestPaper, SourceID: readTestSource, Origin: "arxiv:v2", Sha256: frozen.SHA256, ObjstoreKey: frozen.Key, SizeBytes: frozen.SizeBytes}, bundles: map[string]registry.ParseBundle{}}
	b := writeReadingBundle(t, catalog, store, "pr_readold", strings.Repeat("原始😀内容 ", 80))
	catalog.current = b.RevisionID
	return catalog, store, b
}
func writeReadingBundle(t *testing.T, c *readTestCatalog, store objstore.Store, revision, text string) registry.ParseBundle {
	t.Helper()
	middle, err := json.Marshal(map[string]any{"schema": "docvortex.middle", "schema_version": "2.0", "pages": []any{map[string]any{"page_idx": 0, "blocks": []any{map[string]any{"type": "text", "index": 0, "content": []any{map[string]any{"type": "text", "content": text}}}, map[string]any{"type": "image", "index": 4, "content": []any{map[string]any{"type": "image_body", "index": 4, "content": "", "image_path": "images/nested/figure (1).jpg"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"middle_json.json": middle, "markdown.md": []byte("original parser markdown"), "structured_content.json": []byte(`{"view":"consumer-only"}`), "images/nested/figure (1).jpg": []byte("original image bytes"), "extra/unknown.json": []byte(`{"original":true}`), "extra/active.svg": []byte(`<svg onload="bad()"></svg>`)}
	m, err := paperbundle.New(store).WriteBundle(t.Context(), paperbundle.Input{PaperID: readTestPaper, SourceID: c.source.SourceID, RevisionID: revision, SourcePDFSHA256: c.source.Sha256, Files: files, MiddlePath: "middle_json.json", MarkdownPath: "markdown.md"})
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, _ := json.Marshal(m)
	b := registry.ParseBundle{ParseRevision: registry.ParseRevision{PaperID: readTestPaper, SourceID: c.source.SourceID, RevisionID: revision, Schema: mineru.MiddleSchema, SchemaVersion: mineru.MiddleSchemaVersion, ArtifactSha256: paperbundle.SHA256(middle), ObjstoreKey: paperbundle.FileKey(readTestPaper, c.source.SourceID, revision, m.MiddlePath), Tier: "standard", CreatedAt: time.Unix(1700000000, 0)}, ManifestKey: paperbundle.ManifestKey(readTestPaper, c.source.SourceID, revision), SourcePDFSHA256: c.source.Sha256, MiddlePath: m.MiddlePath, MarkdownPath: m.MarkdownPath, ManifestSHA256: paperbundle.SHA256(manifestBytes)}
	c.bundles[revision] = b
	return b
}
func readRouteRequest(t *testing.T, path string, handler func(*core.RequestEvent) error) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = httptest.NewRequest(http.MethodGet, path, nil)
	re.Response = rec
	if err := handler(re); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}
func callReading(t *testing.T, c *readTestCatalog, store objstore.Store, converter contentReadConverter, query string) (*httptest.ResponseRecorder, map[string]any) {
	return readRouteRequest(t, "/api/papers/"+readTestPaper+"/read?"+query, func(re *core.RequestEvent) error {
		return contentReadHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, converter, readTestPaper)
	})
}

func TestContentReadDerivedWindowAndCursorPinsOldRevision(t *testing.T) {
	c, store, old := newReadingFixture(t)
	converter := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
	rec, body := callReading(t, c, store, converter, "source_id="+readTestSource+"&limit=40")
	if rec.Code != 200 || body["format"] != "markdown" || body["revision"] != old.RevisionID || body["truncated"] != true {
		t.Fatalf("first window %d %+v", rec.Code, body)
	}
	if _, ok := body["content"].(string); !ok {
		t.Fatal("read content must be Markdown string")
	}
	if strings.Contains(body["content"].(string), "consumer-only") {
		t.Fatal("read served structured content")
	}
	cursor := body["next_request"].(map[string]any)["cursor"].(string)
	newBundle := writeReadingBundle(t, c, store, "pr_readnew", "CURRENT DIFFERENT CONTENT")
	c.current = newBundle.RevisionID
	before := c.readyCalls
	follow, continued := callReading(t, c, store, converter, "cursor="+url.QueryEscape(cursor))
	if follow.Code != 200 || continued["revision"] != old.RevisionID || c.readyCalls != before || converter.ensureCalls != 0 {
		t.Fatalf("cursor used current/acquired: %d %+v ready calls %d/%d", follow.Code, continued, before, c.readyCalls)
	}
	for _, query := range []string{"cursor=" + url.QueryEscape(cursor) + "&revision=" + newBundle.RevisionID, "cursor=" + url.QueryEscape(cursor) + "&source_id=other", "cursor=" + url.QueryEscape(cursor) + "&limit=41"} {
		bad, _ := callReading(t, c, store, converter, query)
		if bad.Code != 409 {
			t.Fatalf("conflicting pin accepted: %s %d", query, bad.Code)
		}
	}
	if imageURL := bundleMemberURL(readTestPaper, old.RevisionID, "images/nested/figure (1).jpg"); !strings.Contains(imageURL, "images/nested/figure%20%281%29.jpg") {
		t.Fatal(imageURL)
	}
}

func TestContentReadGatesAndValidatesBeforeAcquisition(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	converter := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
	before := c.getCalls
	rec, _ := readRouteRequest(t, "/api/papers/qa/read?cursor=bad", func(re *core.RequestEvent) error {
		return contentReadHandler(re, &config.Config{}, store, c, converter, readTestPaper)
	})
	if rec.Code != 404 || c.getCalls != before || converter.ensureCalls != 0 {
		t.Fatal("disabled gate had effects")
	}
	for _, query := range []string{"cursor=bad!", "limit=0", "limit=100001", "page=-1", "block=5", "page=1&page=2", "limit="} {
		bad, _ := callReading(t, c, store, converter, query)
		if bad.Code != 400 {
			t.Fatalf("bad query accepted %q: %d", query, bad.Code)
		}
	}
	if converter.ensureCalls != 0 {
		t.Fatal("invalid request submitted inference")
	}
}

func TestContentReadMissingPublishedBundleReturnsLazy202AndPollNeverSubmits(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	c.bundles = map[string]registry.ParseBundle{}
	converter := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued, RevisionID: "pr_future"}}
	rec, body := callReading(t, c, store, converter, "source_id="+readTestSource)
	if rec.Code != 202 || body["ready"] != false || converter.ensureCalls != 1 || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("cold read %d %+v", rec.Code, body)
	}
	location := rec.Header().Get("Operation-Location")
	if location != "/api/papers/"+readTestPaper+"/read/status?source_id="+readTestSource {
		t.Fatal(location)
	}
	converter.job = &mineru.Job{State: mineru.JobStateDone, RevisionID: "pr_stale"}
	status, statusBody := readRouteRequest(t, location, func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, converter, readTestPaper)
	})
	if status.Code != 202 || statusBody["state"] != "pending" || statusBody["ready"] != false || converter.ensureCalls != 1 || converter.lookupCalls != 1 {
		t.Fatalf("poll lied about Done or submitted: %d %+v %+v", status.Code, statusBody, converter)
	}
	// Full old members/manifest exist physically, but no publication row: not ready.
	if strings.Contains(rec.Body.String(), "original parser markdown") {
		t.Fatal("legacy artifact fallback")
	}
}

func TestContentReadStatusReadyWithoutConverterAndSourceScoped(t *testing.T) {
	c, store, b := newReadingFixture(t)
	rec, body := readRouteRequest(t, "/api/papers/"+readTestPaper+"/read/status?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, readTestPaper)
	})
	if rec.Code != 200 || body["state"] != "cached" || body["ready"] != true || body["md_ready"] != true || body["pdf_ready"] != true || body["revision"] != b.RevisionID || body["source_id"] != readTestSource {
		t.Fatalf("ready poll %d %+v", rec.Code, body)
	}
}

func TestContentReadPinnedCorruptOrMissingNeverRepairs(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "missing"}[missing], func(t *testing.T) {
			c, store, b := newReadingFixture(t)
			converter := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
			key := paperbundle.FileKey(readTestPaper, readTestSource, b.RevisionID, "extra/unknown.json")
			if missing {
				if err := store.Delete(t.Context(), key); err != nil {
					t.Fatal(err)
				}
			} else {
				data := []byte("tampered")
				if _, err := store.Put(t.Context(), key, bytes.NewReader(data), int64(len(data)), "application/json"); err != nil {
					t.Fatal(err)
				}
			}
			rec, _ := callReading(t, c, store, converter, "revision="+b.RevisionID)
			want := 422
			if missing {
				want = 404
			}
			if rec.Code != want || converter.ensureCalls != 0 {
				t.Fatalf("pinned corrupt repaired/switched %d %+v", rec.Code, converter)
			}
		})
	}
}

func TestContentReadBackendUnavailableDoesNotSubmit(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	c.readyErr = errors.Join(objstore.ErrUnavailable, errors.New("backend down"))
	converter := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
	rec, _ := callReading(t, c, store, converter, "source_id="+readTestSource)
	if rec.Code != 503 || converter.ensureCalls != 0 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("outage treated as cache miss %d %+v", rec.Code, converter)
	}
}

func TestContentReadExactNonContiguousSelectorsAndTypedNilConverter(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	for _, query := range []string{"source_id=" + readTestSource + "&page=1&block=3", "source_id=" + readTestSource + "&page=2"} {
		rec, _ := callReading(t, c, store, nil, query)
		if rec.Code != 404 {
			t.Fatalf("fabricated page/block %q %d", query, rec.Code)
		}
	}
	rec, body := callReading(t, c, store, (*mineru.Converter)(nil), "source_id="+readTestSource+"&page=1&block=5")
	if rec.Code != 200 || !strings.Contains(body["content"].(string), "images/nested/figure%20%281%29.jpg") {
		t.Fatalf("image block selection %d %+v", rec.Code, body)
	}
	c.bundles = map[string]registry.ParseBundle{}
	rec, _ = callReading(t, c, store, (*mineru.Converter)(nil), "source_id="+readTestSource)
	if rec.Code != 503 {
		t.Fatalf("typed-nil converter did not fail closed: %d", rec.Code)
	}
}

type fullReadTestConverter struct {
	*readTestConverter
	fullCalls, pdfOnlyCalls, externalLookupCalls int
	lastIdentity                                 string
	onEnsure                                     func(string) *mineru.Job
}

func (c *fullReadTestConverter) Ensure(_ context.Context, id string) *mineru.Job {
	c.fullCalls++
	c.lastIdentity = id
	if c.onEnsure != nil {
		return c.onEnsure(id)
	}
	return c.job
}
func (c *fullReadTestConverter) EnsureByDOI(ctx context.Context, doi, oa string) *mineru.Job {
	return c.Ensure(ctx, doi)
}
func (c *fullReadTestConverter) EnsurePDF(context.Context, string) *mineru.Job {
	c.pdfOnlyCalls++
	return c.job
}
func (c *fullReadTestConverter) Lookup(string) (*mineru.Job, bool) {
	c.externalLookupCalls++
	return c.job, c.job != nil
}

func TestContentReadFreshPDFContentIntentChainAndPurePoll(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	c.source = registry.PaperSource{}
	c.bundles = map[string]registry.ParseBundle{}
	converter := &fullReadTestConverter{readTestConverter: &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued, PaperID: readTestPaper, Phase: mineru.PhaseFetchingPDF}}}
	rec, body := readRouteRequest(t, "/api/papers/2501.09999v2/read?limit=30", func(re *core.RequestEvent) error {
		return contentReadHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, converter, "2501.09999v2")
	})
	if rec.Code != 202 || converter.fullCalls != 1 || converter.pdfOnlyCalls != 0 || converter.ensureCalls != 0 || converter.lastIdentity != "2501.09999v2" || body["pdf_ready"] != false || body["md_ready"] != false {
		t.Fatalf("fresh fetch intent %d %+v %+v", rec.Code, body, converter)
	}
	location := rec.Header().Get("Operation-Location")
	if !strings.Contains(location, "2501.09999v2/read/status") || !strings.Contains(location, "limit=30") {
		t.Fatal("pending fetch lost exact original version/selection: " + location)
	}
	poll, pollBody := readRouteRequest(t, location, func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, converter, "2501.09999v2")
	})
	if poll.Code != 202 || pollBody["md_ready"] != false || converter.fullCalls != 1 || converter.pdfOnlyCalls != 0 || converter.externalLookupCalls != 1 {
		t.Fatalf("poll submitted/claimedready %d %+v %+v", poll.Code, pollBody, converter)
	}
}

func TestContentReadFreshEagerDoneRequiresDurableSourceAndBundle(t *testing.T) {
	c, store, b := newReadingFixture(t)
	savedSource := c.source
	c.source = registry.PaperSource{}
	c.bundles = map[string]registry.ParseBundle{}
	converter := &fullReadTestConverter{readTestConverter: &readTestConverter{}}
	converter.onEnsure = func(id string) *mineru.Job {
		c.source = savedSource
		c.bundles[b.RevisionID] = b
		return &mineru.Job{State: mineru.JobStateDone, PaperID: readTestPaper, SourceID: readTestSource, RevisionID: b.RevisionID}
	}
	rec, _ := readRouteRequest(t, "/api/papers/2501.09999v2/read", func(re *core.RequestEvent) error {
		return contentReadHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, converter, "2501.09999v2")
	})
	if rec.Code != 200 || converter.fullCalls != 1 || converter.ensureCalls != 0 {
		t.Fatalf("eager completion not verified %d %+v", rec.Code, converter)
	}
}

func TestContentReadMissingFrozenSourceNeverFallsBackToFreshFetch(t *testing.T) {
	c, store, b := newReadingFixture(t)
	if err := store.Delete(t.Context(), c.source.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	converter := &fullReadTestConverter{readTestConverter: &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}}
	for _, query := range []string{"source_id=" + readTestSource, "revision=" + b.RevisionID, ""} {
		rec, _ := callReading(t, c, store, converter, query)
		if rec.Code != 404 {
			t.Fatalf("missing frozen source response %d", rec.Code)
		}
	}
	if converter.fullCalls != 0 || converter.pdfOnlyCalls != 0 || converter.ensureCalls != 0 {
		t.Fatal("missing immutable source was reacquired")
	}
}

func TestContentReadDefaultLimit(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	rec, body := callReading(t, c, store, nil, "source_id="+readTestSource)
	if rec.Code != 200 || body["request_scope"].(map[string]any)["limit"] != float64(paperread.DefaultLimit) {
		t.Fatalf("default limit not preserved: %d %+v", rec.Code, body)
	}
}
