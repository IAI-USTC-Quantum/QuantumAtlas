package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// makePDF builds a minimal valid PDF body (magic + objects + startxref +
// %%EOF) padded past the minimum size.
func makePDF(padTo int) []byte {
	body := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Size 1 >>\nstartxref\n0\n%%EOF\n"
	if padTo > len(body) {
		body += strings.Repeat("% padding to satisfy the minimum size check; harmless PDF comment\n", (padTo-len(body))/57+1)
	}
	return []byte(body)
}

// pdfServer serves a valid PDF for /ok and classified HTML for others.
func pdfServer(t *testing.T, mux func(*http.ServeMux)) *httptest.Server {
	t.Helper()
	m := http.NewServeMux()
	m.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(makePDF(0))
	})
	m.HandleFunc("/paper.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(makePDF(0))
	})
	if mux != nil {
		mux(m)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv
}

// newLocalStore builds a LocalStore objstore on a temp dir.
func newLocalStore(t *testing.T, dir string) objstore.Store {
	t.Helper()
	st, err := objstore.NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return st
}

func newClient(t *testing.T, srvURL string, respectRobots bool) *FetchClient {
	t.Helper()
	c, err := NewFetchClient(FetchConfig{
		RespectRobots: respectRobots,
		HostRPS:       1000,
		MinPDFBytes:   64,
		HTTPClient:    &http.Client{},
		Now:           time.Now,
	})
	if err != nil {
		t.Fatalf("NewFetchClient: %v", err)
	}
	return c
}

// --- validation pipeline ------------------------------------------------------

func TestFetchClient_Validation(t *testing.T) {
	srv := pdfServer(t, func(m *http.ServeMux) {
		m.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><title>Just a moment...</title><body>Verify you are human</body></html>"))
		})
		m.HandleFunc("/paywall", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html><body>Please sign in to purchase this article</body></html>"))
		})
		m.HandleFunc("/pow", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html><title>Preparing to download…</title>POW_CHALLENGE</html>"))
		})
		m.HandleFunc("/truncated", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("%PDF-1.4 no trailer here\n" + strings.Repeat("% pad pad pad pad pad pad pad pad pad pad\n", 4)))
		})
		m.HandleFunc("/tiny", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("%PDF-1.4"))
		})
		m.HandleFunc("/notfound", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
	})
	c := newClient(t, srv.URL, false)
	ctx := context.Background()

	if res, err := c.FetchPDF(ctx, srv.URL+"/ok"); err != nil || res.Size == 0 || res.Sha256 == "" {
		t.Fatalf("valid PDF: res=%v err=%v", res, err)
	}
	for path, want := range map[string]error{
		"/challenge": ErrChallenge,
		"/paywall":   ErrPaywall,
		"/pow":       ErrChallenge,
		"/truncated": ErrTruncated,
		"/tiny":      ErrTooSmall,
		"/notfound":  ErrHTTP,
	} {
		if _, err := c.FetchPDF(ctx, srv.URL+path); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", path, err, want)
		}
	}
}

func TestFetchClient_RobotsDisallows(t *testing.T) {
	srv := pdfServer(t, func(m *http.ServeMux) {
		m.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /ok\n"))
		})
	})
	c := newClient(t, srv.URL, true)
	if _, err := c.FetchPDF(context.Background(), srv.URL+"/ok"); !errors.Is(err, ErrRobots) {
		t.Fatalf("err = %v, want ErrRobots", err)
	}
	// Hostile/missing robots fails open.
	srv2 := pdfServer(t, func(m *http.ServeMux) {
		m.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	})
	c2 := newClient(t, srv2.URL, true)
	if _, err := c2.FetchPDF(context.Background(), srv2.URL+"/ok"); err != nil {
		t.Fatalf("403 robots should fail open, got %v", err)
	}
}

func TestRobotsParse_LongestMatch(t *testing.T) {
	group, delay := parseRobots("User-agent: *\nDisallow: /private/\nAllow: /private/ok\nCrawl-delay: 3\n")
	if robotsAllows(group, "/private/secret") {
		t.Error("/private/secret should be disallowed")
	}
	if !robotsAllows(group, "/private/ok/fine") {
		t.Error("/private/ok should win by longest match")
	}
	if !robotsAllows(group, "/public") {
		t.Error("/public should be allowed")
	}
	if delay != 3*time.Second {
		t.Errorf("delay = %v", delay)
	}
	// Agent-token group outranks *.
	group2, _ := parseRobots("User-agent: *\nDisallow: /\nUser-agent: " + robotsAgentToken + "\nAllow: /")
	if !robotsAllows(group2, "/anything") {
		t.Error("specific group should allow")
	}
}

// --- identifiers ----------------------------------------------------------------

func TestParseIdentifier(t *testing.T) {
	cases := []struct {
		in      string
		kind    IdentifierKind
		doi     string
		arxiv   string
		wantErr bool
	}{
		{in: "10.1038/s41586-024-07806-9", kind: KindDOI, doi: "10.1038/s41586-024-07806-9"},
		{in: "https://doi.org/10.1103/PhysRevA.109.012601", kind: KindURL, doi: "10.1103/physreva.109.012601"},
		{in: "arXiv:2401.12345", kind: KindArxiv, arxiv: "2401.12345"},
		{in: "2401.12345v2", kind: KindArxiv, arxiv: "2401.12345v2"},
		{in: "quant-ph/9508027", kind: KindArxiv, arxiv: "quant-ph/9508027"},
		{in: "https://arxiv.org/abs/2501.00010", kind: KindURL, arxiv: "2501.00010"},
		{in: "https://arxiv.org/pdf/2501.00010v2", kind: KindURL, arxiv: "2501.00010v2"},
		{in: "https://www.biorxiv.org/content/10.1101/2020.01.01.123456v1", kind: KindURL, doi: "10.1101/2020.01.01.123456v1"},
		{in: "https://pmc.ncbi.nlm.nih.gov/articles/PMC4775821/", kind: KindURL, doi: "pmc:PMC4775821"},
		{in: "https://dl.acm.org/doi/10.1145/3442188.3445922", kind: KindDOI, doi: "10.1145/3442188.3445922"},
		{in: "not a paper", wantErr: true},
		{in: "https://example.com/random", wantErr: true},
	}
	for _, tc := range cases {
		id, err := ParseIdentifier(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", tc.in, id)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if id.Kind != tc.kind || id.Ref.DOI != tc.doi || id.Ref.ArxivID != tc.arxiv {
			t.Errorf("%q: got kind=%s doi=%q arxiv=%q, want kind=%s doi=%q arxiv=%q",
				tc.in, id.Kind, id.Ref.DOI, id.Ref.ArxivID, tc.kind, tc.doi, tc.arxiv)
		}
	}
	if !IsPMCID("pmc:PMC123") || IsPMCID("10.1/x") {
		t.Error("IsPMCID")
	}
}

// --- patterns -----------------------------------------------------------------

func TestPatternCandidates(t *testing.T) {
	cands := PatternCandidates("10.1007/s11222-022-01574-4")
	if len(cands) == 0 || !strings.Contains(cands[0], url2F("10.1007/s11222-022-01574-4")) {
		t.Errorf("springer candidates = %v", cands)
	}
	cands = PatternCandidates("10.1371/journal.pone.0123456")
	if len(cands) != 1 || !strings.Contains(cands[0], "journals.plos.org/journal.pone/article/file") {
		t.Errorf("plos candidates = %v", cands)
	}
	if got := PatternCandidates("10.9999/nothing"); len(got) != 0 {
		t.Errorf("unknown prefix should give no candidates, got %v", got)
	}
}

func url2F(s string) string { return strings.ReplaceAll(s, "/", "%2F") }

// --- ladder ---------------------------------------------------------------------

type stubOA struct {
	name    string
	cands   []string
	err     error
	peerURL string
}

// PeerURL returns the URL substituted for the "PEER" marker when the
// test wraps this resolver in urlRewriteOA.
func (s *stubOA) PeerURL() string { return s.peerURL }

func (s *stubOA) Name() string { return s.name }

func (s *stubOA) Candidates(ctx context.Context, doi string) ([]string, error) {
	return s.cands, s.err
}

// ladderHarness bundles a stub server (PDF + landing routes) with a
// fetch-only Downloader wired onto it — no production endpoint is
// reachable.
type ladderHarness struct {
	srv *httptest.Server
	dl  *Downloader
}

func newLadderHarness(t *testing.T, mux func(*http.ServeMux), oa []OAResolver, agent LinkExtractor) *ladderHarness {
	t.Helper()
	srv := pdfServer(t, mux)
	dl := New(nil, nil, nil, nil, Config{
		Concurrency: 1,
		Fetch: FetchConfig{
			RespectRobots:  false,
			MinPDFBytes:    64,
			HTTPClient:     srv.Client(),
			LandingBaseURL: srv.URL + "/doi/",
		},
	}, WithOAResolvers(oa...))
	if agent != nil {
		dl.agent = agent
	}
	return &ladderHarness{srv: srv, dl: dl}
}

func TestLadder_OAResolverWins(t *testing.T) {
	// 10.1000 has no constructor pattern, so nothing outside the stub
	// server is ever contacted.
	h := newLadderHarness(t, nil, nil, nil)
	oa := &stubOA{name: "unpaywall", cands: []string{"PEER"}, peerURL: h.srv.URL + "/paper.pdf"}
	h.dl = New(nil, nil, nil, nil, Config{
		Concurrency: 1,
		Fetch: FetchConfig{
			RespectRobots:  false,
			MinPDFBytes:    64,
			HTTPClient:     h.srv.Client(),
			LandingBaseURL: h.srv.URL + "/doi/",
		},
	}, WithOAResolvers(&urlRewriteOA{inner: oa, name: "unpaywall"}))
	out, err := h.dl.FetchPDF(context.Background(), registry.PaperRef{DOI: "10.1000/x"})
	if err != nil {
		t.Fatalf("FetchPDF: %v (trace: %+v)", err, out.Trace)
	}
	if out.Strategy != "oa:unpaywall" || out.Result == nil || out.Result.Size == 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

// urlRewriteOA adapts a stub resolver whose candidate is the literal
// marker "PEER" into the harness server's /ok PDF.
type urlRewriteOA struct {
	inner interface {
		OAResolver
		PeerURL() string
	}
	name string
}

func (u *urlRewriteOA) Name() string { return u.name }

func (u *urlRewriteOA) Candidates(ctx context.Context, doi string) ([]string, error) {
	cands, err := u.inner.Candidates(ctx, doi)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cands {
		if c == "PEER" {
			c = u.inner.PeerURL()
		}
		out = append(out, c)
	}
	return out, nil
}

func TestLadder_LandingFallback(t *testing.T) {
	// OA dead; DOI prefix (10.1093) has no constructor pattern; the
	// landing page under /doi/ carries a relative citation_pdf_url that
	// must be absolutized against the final URL and fetched.
	h := newLadderHarness(t, func(m *http.ServeMux) {
		m.HandleFunc("/doi/10.1093/xyz/article", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><meta name="citation_pdf_url" content="/article-pdf/file.pdf"/></head></html>`))
		})
		m.HandleFunc("/article-pdf/file.pdf", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(makePDF(0))
		})
	}, []OAResolver{&stubOA{name: "unpaywall", err: errors.New("no result")}}, nil)
	out, err := h.dl.FetchPDF(context.Background(), registry.PaperRef{DOI: "10.1093/xyz/article"})
	if err != nil {
		t.Fatalf("FetchPDF: %v (trace: %+v)", err, out.Trace)
	}
	if out.Strategy != "landing" {
		t.Fatalf("strategy = %s, want landing (trace %+v)", out.Strategy, out.Trace)
	}
}

type fakeAgent struct{ urls []string }

func (f *fakeAgent) Name() string { return "agent:fake" }

func (f *fakeAgent) Extract(ctx context.Context, in ExtractInput) ([]string, error) {
	return f.urls, nil
}

func TestLadder_AgentFallback(t *testing.T) {
	var agentURL string
	h := newLadderHarness(t, func(m *http.ServeMux) {
		m.HandleFunc("/agent-found.pdf", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(makePDF(0))
		})
		m.HandleFunc("/doi/10.1099/agent", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html><body>nothing here</body></html>`))
		})
	}, []OAResolver{&stubOA{name: "unpaywall", err: errors.New("no result")}}, nil)
	agentURL = h.srv.URL + "/agent-found.pdf"
	h.dl.agent = &fakeAgent{urls: []string{agentURL}}
	out, err := h.dl.FetchPDF(context.Background(), registry.PaperRef{DOI: "10.1099/agent"})
	if err != nil {
		t.Fatalf("FetchPDF: %v (trace: %+v)", err, out.Trace)
	}
	if out.Strategy != "agent:fake" {
		t.Fatalf("strategy = %s, want agent:fake", out.Strategy)
	}
}

func TestLandingExtraction_MetaAbsolutized(t *testing.T) {
	h := newLadderHarness(t, func(m *http.ServeMux) {
		m.HandleFunc("/doi/10.1098/meta", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head>` +
				`<meta name="citation_pdf_url" content="/pdfs/paper1.pdf?a=1&amp;b=2"/>` +
				`</head><body><a href="/pdfs/paper2.pdf">PDF</a></body></html>`))
		})
	}, nil, nil)
	info, err := h.dl.FetchLanding(context.Background(), "10.1098/meta")
	if err != nil {
		t.Fatalf("FetchLanding: %v", err)
	}
	// citation_pdf_url wins exclusively — bare same-host .pdf anchors
	// are only mined when no meta exists (cross-host false positives).
	if len(info.Candidates) != 1 {
		t.Fatalf("candidates = %v", info.Candidates)
	}
	if first := info.Candidates[0]; first != h.srv.URL+"/pdfs/paper1.pdf?a=1&b=2" {
		t.Errorf("first candidate = %q, want absolutized+unescaped meta url", first)
	}
}

// --- openai extractor ------------------------------------------------------------

func TestOpenAIExtractor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"candidates\\\":[\\\"https://x/p.pdf\\\",\\\"not-a-url\\\"]}\\n```\"}}]}"))
	}))
	t.Cleanup(srv.Close)
	x := &OpenAIExtractor{cfg: AgentConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"}}
	cands, err := x.Extract(context.Background(), ExtractInput{DOI: "10.1/x", HTML: []byte("<html/>")})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(cands) != 1 || cands[0] != "https://x/p.pdf" {
		t.Fatalf("cands = %v", cands)
	}
}

func TestClaudeExtractor(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-claude")
	body := "#!/bin/sh\nprintf '%s' '{\"result\":\"{\\\"candidates\\\":[\\\"https://x/p.pdf\\\"]}\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	x := &ClaudeExtractor{cfg: AgentConfig{ClaudeBin: script, Timeout: 10 * time.Second}}
	cands, err := x.Extract(context.Background(), ExtractInput{HTML: []byte("<html/>")})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(cands) != 1 || cands[0] != "https://x/p.pdf" {
		t.Fatalf("cands = %v", cands)
	}
}

// --- queue ----------------------------------------------------------------------

type fakeReg struct {
	mu        sync.Mutex
	upsertDOI map[string]string // paperID -> sha
	statuses  map[string]string
	events    []string
}

func newFakeReg() *fakeReg {
	return &fakeReg{upsertDOI: map[string]string{}, statuses: map[string]string{}}
}

func (f *fakeReg) UpsertPDF(ctx context.Context, ref registry.PaperRef, version int, sha256 string, size int64, pdfPath string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertDOI["arxiv:"+ref.ArxivID] = sha256
	return "paper-1", 1, nil
}

func (f *fakeReg) UpsertPDFByDOI(ctx context.Context, ref registry.PaperRef, sha256 string, size int64, pdfPath string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertDOI[ref.DOI] = sha256
	return "paper-1", 1, nil
}

func (f *fakeReg) UpdateStatus(ctx context.Context, paperID, status string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[paperID] = status
	return true, nil
}

func (f *fakeReg) RecordAcquisitionEvent(ctx context.Context, paperID, phase, state, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, paperID+"|"+phase+"|"+state)
	return nil
}

func TestQueueStoresAndRegisters(t *testing.T) {
	srv := pdfServer(t, nil)
	reg := newFakeReg()
	dir := t.TempDir()
	// LocalStore implements objstore.Store.
	store := newLocalStore(t, dir)
	d := New(reg, store, nil, nil, Config{
		Concurrency: 1,
		Fetch: FetchConfig{
			RespectRobots: false, MinPDFBytes: 64,
			LandingBaseURL: srv.URL + "/doi/",
			HTTPClient:     srv.Client(),
		},
	}, WithOAResolvers(&stubOA{name: "unpaywall", cands: []string{srv.URL + "/paper.pdf"}}))

	ref := registry.PaperRef{DOI: "10.1000/queued"}
	if !d.Enqueue(context.Background(), "paper-1", "10.1000/queued", KindDOI, ref) {
		t.Fatal("Enqueue rejected")
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, p := range d.Snapshot() {
			if p.PaperID == "paper-1" && !p.Active {
				return true
			}
		}
		return false
	})
	reg.mu.Lock()
	sha, ok := reg.upsertDOI["10.1000/queued"]
	reg.mu.Unlock()
	if !ok || sha == "" {
		t.Fatalf("UpsertPDFByDOI not called: %+v", reg.upsertDOI)
	}
	if len(reg.events) == 0 {
		t.Error("no acquisition events recorded")
	}
	snaps := d.Snapshot()
	if len(snaps) != 1 || snaps[0].State != "done" || snaps[0].Strategy != "oa:unpaywall" {
		t.Fatalf("snapshot = %+v", snaps)
	}
	if c := d.SnapshotCounters(); c["succeeded"] != 1 {
		t.Errorf("counters = %v", c)
	}
}

func TestQueueFailureMarksFailed(t *testing.T) {
	reg := newFakeReg()
	store := newLocalStore(t, t.TempDir())
	deadOA := &stubOA{name: "unpaywall", err: errors.New("no result")}
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(deadSrv.Close)
	d := New(reg, store, nil, nil, Config{
		Concurrency: 1,
		Fetch: FetchConfig{
			RespectRobots: false, MinPDFBytes: 64, RequestTimeout: 2 * time.Second,
			LandingBaseURL: deadSrv.URL + "/doi/",
			HTTPClient:     deadSrv.Client(),
		},
	}, WithOAResolvers(deadOA))
	// All strategies dead: DOI unknown everywhere, landing fetch will
	// fail fast against a reserved address.
	if !d.Enqueue(context.Background(), "paper-2", "10.1000/dead", KindDOI, registry.PaperRef{DOI: "10.1000/dead"}) {
		t.Fatal("Enqueue rejected")
	}
	waitFor(t, 30*time.Second, func() bool {
		reg.mu.Lock()
		defer reg.mu.Unlock()
		return reg.statuses["paper-2"] == "failed"
	})
	for _, p := range d.Snapshot() {
		if p.PaperID == "paper-2" && p.State != "failed" {
			t.Fatalf("state = %s, want failed (p=%+v)", p.State, p)
		}
	}
}

// --- helpers ---------------------------------------------------------------------

// waitFor polls cond until the deadline.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", d)
}
