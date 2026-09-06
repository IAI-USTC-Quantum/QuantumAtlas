package downloader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/safego"
)

// ErrNoPDF is the terminal error after every strategy failed.
var ErrNoPDF = errors.New("downloader: all strategies failed")

// ErrNoIdentity is returned when a ref has neither DOI nor arXiv id.
var ErrNoIdentity = errors.New("downloader: no DOI or arXiv identity")

// ErrProxyNotConfigured when the remote downloaderproxy is unset.
var ErrProxyNotConfigured = errors.New("downloader: remote proxy not configured")

// registryWriter is the slice of *registry.Store the downloader needs
// (factored out so tests can fake the catalog without a database).
type registryWriter interface {
	UpsertPDF(ctx context.Context, ref registry.PaperRef, version int, sha256 string, size int64, pdfPath string) (paperID string, assetID int64, err error)
	UpsertPDFByDOI(ctx context.Context, ref registry.PaperRef, sha256 string, size int64, pdfPath string) (paperID string, assetID int64, err error)
	UpdateStatus(ctx context.Context, paperID, status string) (found bool, err error)
}

// acquisitionEventWriter and assetLookup are optional registry
// capabilities — *registry.Store implements both.
type acquisitionEventWriter interface {
	RecordAcquisitionEvent(ctx context.Context, paperID, phase, state, detail string) error
}

type assetLookup interface {
	GetWithAssets(ctx context.Context, paperID string) (d *registry.PaperDetail, found bool, err error)
}

// DOIResolver is the slice of openalex.Resolver the ladder needs.
type DOIResolver interface {
	ResolveDOI(ctx context.Context, doi string) (openalex.Resolution, error)
}

// PDFReadyHook starts downstream work after a PDF asset is durably
// registered (isDOI distinguishes published DOI assets from arXiv
// assets). Same contract as ingest.PDFReadyHook.
type PDFReadyHook func(ctx context.Context, canonical string, isDOI bool)

// IndexPusher is the slice of the qatlas-rag client the downloader
// needs (best-effort index push after pdf_ready).
type IndexPusher interface {
	PushIndex(ctx context.Context, paperID string) error
}

// Config configures a Downloader.
type Config struct {
	Concurrency    int
	Fetch          FetchConfig
	Agent          AgentConfig
	Browser        BrowserConfig
	Proxy          *RemoteProxy
	UnpaywallEmail string
	S2APIKey       string
	// OA resolvers used by the oa-apis strategy, in try order. Empty →
	// the default set (Europe PMC, Unpaywall, OpenAlex, S2).
	OAResolvers []OAResolver
}

// Option configures a Downloader (test seams + wiring).
type Option func(*Downloader)

func WithLogger(l *slog.Logger) Option {
	return func(d *Downloader) {
		if l != nil {
			d.log = l
		}
	}
}

// WithOAResolvers replaces the OA-API strategy's resolver set.
func WithOAResolvers(rs ...OAResolver) Option {
	return func(d *Downloader) {
		if len(rs) > 0 {
			d.oaResolvers = rs
		}
	}
}

// WithAgent installs the fallback link extractor (nil = disabled).
func WithAgent(a LinkExtractor) Option {
	return func(d *Downloader) { d.agent = a }
}

// WithPDFReadyHook installs the downstream conversion trigger.
func WithPDFReadyHook(h PDFReadyHook) Option {
	return func(d *Downloader) {
		if h != nil {
			d.onPDFReady = h
		}
	}
}

// WithIndexPusher installs the qatlas-rag index-push client.
func WithIndexPusher(p IndexPusher) Option {
	return func(d *Downloader) {
		if p != nil {
			d.pusher = p
		}
	}
}

// Attempt is one strategy try in the trace (survives to the job
// snapshot and, on failure, the audit events).
type Attempt struct {
	Strategy string `json:"strategy"`
	URL      string `json:"url,omitempty"`
	Error    string `json:"error,omitempty"`
	Millis   int64  `json:"ms"`
}

// FetchOutcome is the ladder's result for one paper.
type FetchOutcome struct {
	Strategy string       // winning strategy id
	URL      string       // final source URL
	Result   *FetchResult // validated PDF body
	// Identity the outcome should be stored under (exactly one pair set).
	ArxivCanonical string
	ArxivVersion   int
	DOI            string
	Trace          []Attempt
}

// Downloader is the robust acquisition module. Construct with New; the
// worker pool starts immediately. Safe for concurrent use.
type Downloader struct {
	cfg Config
	log *slog.Logger

	fetch *FetchClient
	arxiv *arxiv.Fetcher
	oa    DOIResolver

	oaResolvers []OAResolver
	unpaywall   *Unpaywall
	epmc        *EuropePMC
	agent       LinkExtractor
	browser     *BrowserLane
	proxy       *RemoteProxy

	reg        registryWriter
	store      objstore.Store
	onPDFReady PDFReadyHook
	pusher     IndexPusher

	// queue plumbing (ingest-shaped)
	jobs      chan job
	done      chan struct{}
	sf        singleflight.Group
	wg        sync.WaitGroup
	closeOnce sync.Once
	closed    atomic.Bool

	progressMu sync.Mutex
	progress   map[string]*Progress

	queued    atomic.Int64
	inFlight  atomic.Int64
	succeeded atomic.Int64
	failed    atomic.Int64
	skipped   atomic.Int64
}

type job struct {
	paperID string
	input   string
	kind    IdentifierKind
	ref     registry.PaperRef
	ctx     context.Context
	done    chan struct{}
}

// New builds a Downloader over the production types. reg may be nil
// (fetch-only mode, used by `qatlasd downloader probe --dry-run`);
// fetcher (arXiv) may be nil (arXiv strategy disabled); oa (OpenAlex)
// may be nil.
func New(reg registryWriter, store objstore.Store, fetcher *arxiv.Fetcher, oa DOIResolver, cfg Config, opts ...Option) *Downloader {
	fetchClient, err := NewFetchClient(cfg.Fetch)
	if err != nil {
		fetchClient = &FetchClient{cfg: FetchConfig{}, now: time.Now} // unreachable; NewFetchClient never errors on sane input
		_ = err
	}
	d := &Downloader{
		cfg:      cfg,
		log:      slog.Default(),
		fetch:    fetchClient,
		arxiv:    fetcher,
		oa:       oa,
		reg:      reg,
		store:    store,
		jobs:     make(chan job, 128),
		done:     make(chan struct{}),
		progress: make(map[string]*Progress),
	}
	d.epmc = NewEuropePMC("", cfg.Fetch.RequestTimeout)
	d.unpaywall = NewUnpaywall("", cfg.UnpaywallEmail, cfg.Fetch.RequestTimeout)
	if cfg.OAResolvers != nil {
		d.oaResolvers = cfg.OAResolvers
	} else {
		d.oaResolvers = []OAResolver{d.epmc, d.unpaywall, &openalexAdapter{oa: oa}, NewSemanticScholar("", cfg.S2APIKey, cfg.Fetch.RequestTimeout)}
	}
	d.agent = NewLinkExtractor(cfg.Agent)
	d.browser = NewBrowserLane(cfg.Browser)
	if cfg.Proxy != nil && cfg.Proxy.Enabled() {
		d.proxy = cfg.Proxy
		if d.proxy.Client == nil {
			d.proxy.Client = &http.Client{Timeout: d.proxy.Timeout + 30*time.Second}
		}
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.cfg.Concurrency < 1 {
		d.cfg.Concurrency = 2
	}
	for w := 0; w < d.cfg.Concurrency; w++ {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					safego.LogPanic("downloader.worker", r)
				}
			}()
			d.worker()
		}()
	}
	return d
}

// openalexAdapter exposes the shared OpenAlex resolver as an
// OAResolver (best pdf_url first, then the locations fallback list).
type openalexAdapter struct {
	oa DOIResolver
}

func (a *openalexAdapter) Name() string { return "openalex" }

func (a *openalexAdapter) Candidates(ctx context.Context, doi string) ([]string, error) {
	if a.oa == nil {
		return nil, fmt.Errorf("openalex: resolver not configured")
	}
	res, err := a.oa.ResolveDOI(ctx, doi)
	if err != nil {
		return nil, fmt.Errorf("openalex: %w", err)
	}
	var out []string
	if res.OAPdfURL != "" {
		out = append(out, res.OAPdfURL)
	}
	out = append(out, res.LocationsPDFURLs...)
	return out, nil
}

// ---------------------------------------------------------------------------
// Strategy ladder
// ---------------------------------------------------------------------------

// FetchPDF runs the full strategy ladder for one paper identity. It is
// storage-free: the queue worker (and the probe subcommand) decide what
// to do with the outcome. The returned error is ErrNoPDF (with the
// trace on the outcome) when every strategy failed.
func (d *Downloader) FetchPDF(ctx context.Context, ref registry.PaperRef) (*FetchOutcome, error) {
	out := &FetchOutcome{DOI: ref.DOI}

	if ref.ArxivID != "" {
		if res := d.tryArxiv(ctx, ref.ArxivID, out); res != nil {
			return out, nil
		}
	}
	doi := strings.TrimSpace(ref.DOI)
	if IsPMCID(doi) {
		// Normalize a stashed PMCID into a real DOI via Europe PMC; the
		// EPMC render URL becomes an extra first candidate.
		pmcid := strings.TrimPrefix(strings.ToLower(doi), "pmc:")
		pmcid = strings.ToUpper(pmcid)
		cands, realDOI, err := d.epmc.CandidatesByPMCID(ctx, pmcid)
		out.Trace = append(out.Trace, Attempt{Strategy: "pmc-resolve", Error: errString(err), Millis: 0})
		if err != nil {
			return out, fmt.Errorf("%w: pmcid %s: %v", ErrNoPDF, pmcid, err)
		}
		if realDOI != "" {
			doi = realDOI
			out.DOI = doi
		}
		for _, u := range cands {
			if res := d.tryCandidate(ctx, "oa:europepmc", u, out); res != nil {
				return out, nil
			}
		}
	}
	if doi == "" {
		if ref.ArxivID == "" {
			return out, ErrNoIdentity
		}
		return out, fmt.Errorf("%w (arxiv fetch failed and no DOI)", ErrNoPDF)
	}

	// OpenAlex arXiv-twin resolution: a DOI-only paper that has an arXiv
	// version is fetched from arXiv (reliable, versioned) before any OA
	// guesswork — the same preference the ingest pipeline applies.
	if ref.ArxivID == "" && d.oa != nil {
		start := time.Now()
		if res, err := d.oa.ResolveDOI(ctx, doi); err == nil && res.ArxivID != "" {
			out.Trace = append(out.Trace, Attempt{Strategy: "twin-resolve", URL: res.ArxivID, Millis: time.Since(start).Milliseconds()})
			if res := d.tryArxiv(ctx, res.ArxivID, out); res != nil {
				return out, nil
			}
		}
	}

	// OA metadata APIs. Candidates that are actually article/landing
	// pages (green-OA repositories: HAL, university repos …) are mined
	// for the real PDF link instead of being fetched blind.
	for _, r := range d.oaResolvers {
		cands, err := r.Candidates(ctx, doi)
		if err != nil {
			out.Trace = append(out.Trace, Attempt{Strategy: "oa:" + r.Name(), Error: errString(err)})
			continue
		}
		if len(cands) == 0 {
			out.Trace = append(out.Trace, Attempt{Strategy: "oa:" + r.Name(), Error: "no candidates"})
		}
		for _, u := range cands {
			if looksLikePDF(u) {
				if res := d.tryCandidate(ctx, "oa:"+r.Name(), u, out); res != nil {
					return out, nil
				}
				continue
			}
			li, lerr := d.scrapeLanding(ctx, u)
			if lerr != nil {
				out.Trace = append(out.Trace, attemptOf("oa:"+r.Name()+"+landing", u, lerr, time.Now()))
				continue
			}
			if len(li.Candidates) == 0 {
				out.Trace = append(out.Trace, Attempt{Strategy: "oa:" + r.Name() + "+landing", URL: u, Error: "no PDF candidates on repository landing page"})
			}
			for _, u2 := range li.Candidates {
				if res := d.tryCandidate(ctx, "oa:"+r.Name()+"+landing", u2, out); res != nil {
					return out, nil
				}
			}
		}
	}

	// Publisher URL patterns derived from the DOI prefix.
	for _, u := range PatternCandidates(doi) {
		if res := d.tryCandidate(ctx, "pattern", u, out); res != nil {
			return out, nil
		}
	}

	// Landing page (+ IEEE stamp resolution) and the agent fallback.
	landing, err := d.FetchLanding(ctx, doi)
	if err != nil {
		out.Trace = append(out.Trace, Attempt{Strategy: "landing", Error: errString(err)})
	} else {
		for _, u := range landing.Candidates {
			if res := d.tryCandidate(ctx, "landing", u, out); res != nil {
				return out, nil
			}
		}
		if len(landing.Candidates) == 0 {
			out.Trace = append(out.Trace, Attempt{Strategy: "landing", URL: landing.FinalURL, Error: "no PDF candidates on landing page"})
		}
	}

	// Remote proxy FIRST: a standalone downloaderproxy on a
	// directly-entitled machine (campus egress) runs the full ladder —
	// including its own browser lane — so it beats every local last
	// resort when the local network is proxied or unentitled.
	if d.proxy.Enabled() && traceSawChallenge(out.Trace) {
		start := time.Now()
		res, attempts, strategy, perr := d.proxy.FetchPDF(ctx, ref)
		out.Trace = append(out.Trace, attempts...)
		if perr == nil {
			out.Strategy = strategy
			out.URL = res.URL
			out.Result = res
			out.Trace = append(out.Trace, Attempt{Strategy: strategy, URL: res.URL, Millis: time.Since(start).Milliseconds()})
			return out, nil
		}
		out.Trace = append(out.Trace, attemptOf("remote-proxy", "", perr, start))
	}

	// Browser lane: replay the publisher PDF/landing URLs through a
	// real Chromium when the plain-HTTP attempts hit bot walls — the
	// most common terminal failure on entitled networks. Enabled via
	// downloader.browser.cdp_url.
	if d.browser.Enabled() && traceSawChallenge(out.Trace) {
		start := time.Now()
		for _, u := range browserTargets(out, doi) {
			res, berr := d.browser.FetchPDF(ctx, u)
			if berr == nil {
				out.Strategy = "browser"
				out.URL = res.URL
				out.Result = res
				out.Trace = append(out.Trace, Attempt{Strategy: "browser", URL: u, Millis: time.Since(start).Milliseconds()})
				return out, nil
			}
			out.Trace = append(out.Trace, attemptOf("browser", u, berr, start))
		}
	}

	if landing != nil && d.agent != nil {
		cands, err := d.agent.Extract(ctx, ExtractInput{DOI: doi, LandingURL: landing.FinalURL, HTML: landing.HTML})
		if err != nil {
			out.Trace = append(out.Trace, Attempt{Strategy: d.agent.Name(), Error: errString(err)})
		}
		for _, u := range cands {
			if res := d.tryCandidate(ctx, d.agent.Name(), u, out); res != nil {
				return out, nil
			}
		}
	}

	return out, fmt.Errorf("%w (%d attempts, doi=%s)", ErrNoPDF, len(out.Trace), doi)
}

// tryArxiv runs the arXiv strategy: parse, resolve the latest version
// when unversioned, fetch via the shared rate-limited fetcher. Returns
// the result or nil (with the attempt appended to the trace).
func (d *Downloader) tryArxiv(ctx context.Context, arxivID string, out *FetchOutcome) *FetchResult {
	start := time.Now()
	if d.arxiv == nil {
		out.Trace = append(out.Trace, Attempt{Strategy: "arxiv", Error: "arxiv fetcher not configured", Millis: 0})
		return nil
	}
	parsed, err := paperassets.Parse(arxivID)
	if err != nil {
		out.Trace = append(out.Trace, attemptOf("arxiv", "", err, start))
		return nil
	}
	if parsed.Version == "" {
		parsed, err = d.arxiv.ResolveLatestVersion(ctx, parsed)
		if err != nil {
			out.Trace = append(out.Trace, attemptOf("arxiv", "", err, start))
			return nil
		}
	}
	res, err := d.arxiv.Fetch(ctx, parsed)
	if err != nil {
		out.Trace = append(out.Trace, attemptOf("arxiv", "", err, start))
		return nil
	}
	out.Strategy = "arxiv"
	out.URL = d.arxivBaseURL() + parsed.Canonical
	out.ArxivCanonical = parsed.Canonical
	out.ArxivVersion = registry.ArxivVersionOf(parsed.Canonical)
	out.Result = &FetchResult{Body: res.Body, Size: res.Size, Sha256: res.Sha256, URL: out.URL, Attempts: res.Attempts}
	out.Trace = append(out.Trace, attemptOf("arxiv", out.URL, nil, start))
	return out.Result
}

func (d *Downloader) arxivBaseURL() string { return "https://arxiv.org/pdf/" }

// tryCandidate fetches one candidate URL through the validation
// pipeline. Returns the result on success (also recorded on out), nil
// otherwise (attempt appended to the trace either way).
func (d *Downloader) tryCandidate(ctx context.Context, strategy, rawURL string, out *FetchOutcome) *FetchResult {
	start := time.Now()
	res, err := d.fetch.FetchPDF(ctx, rawURL)
	if err != nil {
		out.Trace = append(out.Trace, attemptOf(strategy, rawURL, err, start))
		return nil
	}
	out.Strategy = strategy
	out.URL = res.URL
	out.Result = res
	out.Trace = append(out.Trace, attemptOf(strategy, res.URL, nil, start))
	return res
}

func attemptOf(strategy, url string, err error, start time.Time) Attempt {
	a := Attempt{Strategy: strategy, URL: url, Millis: time.Since(start).Milliseconds()}
	if err != nil {
		a.Error = err.Error()
	}
	return a
}

// traceSawChallenge reports whether any attempt so far hit a bot wall
// (Cloudflare/Radware challenge, IEEE-style 202/403 gateway) — the
// trigger for the browser lane.
func traceSawChallenge(trace []Attempt) bool {
	for _, a := range trace {
		if a.Error == "" {
			continue
		}
		switch {
		case strings.Contains(a.Error, "bot challenge"),
			strings.Contains(a.Error, "pow_challenge"),
			strings.Contains(a.Error, "http 202"),
			strings.Contains(a.Error, "http 403"),
			strings.Contains(a.Error, "identity-provider handshake"):
			return true
		}
	}
	return false
}

// browserTargets collects the URLs worth replaying in the browser:
// every PDF-ish candidate already tried (they carry the publisher's
// canonical PDF endpoints) plus the DOI landing URL itself (challenge
// pages that resolve to a PDF link inside a real browser).
func browserTargets(out *FetchOutcome, doi string) []string {
	var urls []string
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	for _, a := range out.Trace {
		if a.URL != "" && (looksLikePDF(a.URL) || strings.Contains(a.Strategy, "landing")) {
			add(a.URL)
		}
	}
	if doi != "" {
		add("https://doi.org/" + doi)
	}
	return urls
}

// looksLikePDF reports whether a candidate URL plausibly serves PDF
// bytes directly (vs. an article/landing page that must be mined).
func looksLikePDF(rawURL string) bool {
	u := strings.ToLower(rawURL)
	if strings.Contains(u, ".pdf") {
		return true
	}
	if strings.Contains(u, "/pdf/") || strings.HasSuffix(u, "/pdf") {
		return true
	}
	if strings.Contains(u, "type=printable") || strings.Contains(u, "pdf=render") ||
		strings.Contains(u, "download=1") || strings.Contains(u, "/stamp/") {
		return true
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

// Enqueue schedules one paper for robust acquisition. Duplicate paper
// ids coalesce (singleflight window spans queue+process). Returns false
// when the pool is shutting down.
func (d *Downloader) Enqueue(ctx context.Context, paperID, input string, kind IdentifierKind, ref registry.PaperRef) bool {
	if d.closed.Load() {
		return false
	}
	d.sf.DoChan(paperID, func() (any, error) {
		jobCtx := context.WithoutCancel(ctx)
		d.beginProgress(jobCtx, paperID, input, kind)
		j := job{paperID: paperID, input: input, kind: kind, ref: ref, ctx: jobCtx, done: make(chan struct{})}
		select {
		case d.jobs <- j:
			d.queued.Add(1)
			<-j.done
		case <-d.done:
		}
		return nil, nil
	})
	return true
}

func (d *Downloader) worker() {
	for {
		select {
		case j := <-d.jobs:
			d.process(j)
		case <-d.done:
			for {
				select {
				case j := <-d.jobs:
					d.process(j)
				default:
					return
				}
			}
		}
	}
}

// process runs check→ladder→store→register for one job and resolves
// the job to a terminal state.
func (d *Downloader) process(j job) {
	defer close(j.done)
	defer d.sf.Forget(j.paperID)
	d.inFlight.Add(1)
	defer d.inFlight.Add(-1)
	ctx := j.ctx
	log := d.log.With("paper_id", j.paperID)

	// Already-stored PDF? Skip straight to the downstream hook.
	if d.reg != nil {
		if lookup, ok := d.reg.(assetLookup); ok {
			if detail, found, err := lookup.GetWithAssets(ctx, j.paperID); err == nil && found && hasPDFAsset(detail) {
				d.transition(ctx, j.paperID, "pdf_ready", "done", "pdf already stored", true)
				d.fireHooks(ctx, j.ref, log)
				d.skipped.Add(1)
				d.finishProgress(j.paperID, "done", "", "already-stored")
				return
			}
		}
	}

	d.transition(ctx, j.paperID, "downloading_pdf", "running", "", false)
	outcome, err := d.FetchPDF(ctx, j.ref)
	d.recordTrace(ctx, j.paperID, outcome)
	if err != nil {
		d.fail(ctx, log, j.paperID, outcome, err)
		return
	}

	if d.store == nil {
		// Fetch-only mode (probe --dry-run): nothing to persist.
		d.transition(ctx, j.paperID, "validated", "done", outcome.Strategy, true)
		d.succeeded.Add(1)
		d.finishProgress(j.paperID, "done", outcome.Strategy, "")
		return
	}

	d.transition(ctx, j.paperID, "storing_pdf", "running", outcome.Strategy, false)
	if err := d.storeOutcome(ctx, j, outcome); err != nil {
		d.fail(ctx, log, j.paperID, outcome, fmt.Errorf("store/register: %w", err))
		return
	}

	d.transition(ctx, j.paperID, "registering_asset", "running", "", false)
	d.transition(ctx, j.paperID, "pdf_ready", "done", outcome.Strategy, true)
	d.fireHooks(ctx, j.ref, log)
	d.succeeded.Add(1)
	d.finishProgress(j.paperID, "done", outcome.Strategy, "")
	log.Info("downloader: pdf acquired",
		"strategy", outcome.Strategy, "url", outcome.URL, "size", outcome.Result.Size, "attempts", len(outcome.Trace))
}

// storeOutcome persists the validated PDF under the canonical asset
// key and records it in the registry, mirroring the ingest pipeline
// (IfNoneMatch:"*" conditional write + provenance metadata).
func (d *Downloader) storeOutcome(ctx context.Context, j job, out *FetchOutcome) error {
	var assetKey string
	isDOI := false
	switch {
	case out.ArxivCanonical != "":
		parsed, err := paperassets.Parse(out.ArxivCanonical)
		if err != nil {
			return fmt.Errorf("parse canonical %q: %w", out.ArxivCanonical, err)
		}
		assetKey = paperassets.AssetKeyFor("pdf", parsed)
	default:
		doi, ok := paperassets.ValidateDOI(out.DOI)
		if !ok {
			return fmt.Errorf("outcome has neither arXiv id nor valid DOI")
		}
		assetKey = paperassets.DOIAssetKey("pdf", doi)
		out.DOI = doi
		isDOI = true
	}
	if assetKey == "" {
		return fmt.Errorf("no asset key for outcome")
	}
	_, putErr := d.store.PutWithOptions(ctx, assetKey, out.Result.Body, out.Result.Size, objstore.PutOptions{
		ContentType: "application/pdf",
		IfNoneMatch: "*",
		Metadata: map[string]string{
			"sha256":     out.Result.Sha256,
			"source":     "downloader:" + out.Strategy,
			"source_url": out.URL,
			"fetched_by": "qatlasd-downloader",
			"fetched_at": time.Now().UTC().Format(time.RFC3339),
		},
	})
	if putErr != nil && !errors.Is(putErr, objstore.ErrPreconditionFailed) {
		return fmt.Errorf("store pdf: %w", putErr)
	}

	ref := j.ref
	if isDOI {
		ref.DOI = out.DOI
		if _, _, err := d.reg.UpsertPDFByDOI(ctx, ref, out.Result.Sha256, out.Result.Size, bucketRelKey(assetKey)); err != nil {
			return fmt.Errorf("upsert pdf by doi: %w", err)
		}
	} else {
		ref.ArxivID = out.ArxivCanonical
		if _, _, err := d.reg.UpsertPDF(ctx, ref, out.ArxivVersion, out.Result.Sha256, out.Result.Size, bucketRelKey(assetKey)); err != nil {
			return fmt.Errorf("upsert pdf: %w", err)
		}
	}
	return nil
}

// fireHooks triggers downstream conversion and the (best-effort) rag
// index push, selecting the DOI/arXiv variant from the outcome.
func (d *Downloader) fireHooks(ctx context.Context, ref registry.PaperRef, log *slog.Logger) {
	canonical, isDOI := ref.DOI, true
	if ref.ArxivID != "" {
		canonical, isDOI = ref.ArxivID, false
	}
	if canonical == "" {
		return
	}
	if d.onPDFReady != nil {
		d.onPDFReady(ctx, canonical, isDOI)
	}
	if d.pusher != nil {
		if err := d.pusher.PushIndex(ctx, canonical); err != nil {
			log.Warn("downloader: rag index push failed", "error", err)
		}
	}
}

func (d *Downloader) fail(ctx context.Context, log *slog.Logger, paperID string, outcome *FetchOutcome, err error) {
	d.failed.Add(1)
	detail := err.Error()
	if outcome != nil && len(outcome.Trace) > 0 {
		last := outcome.Trace[len(outcome.Trace)-1]
		detail = fmt.Sprintf("%s [last: %s %s]", detail, last.Strategy, last.Error)
	}
	d.transition(ctx, paperID, "fetch", "failed", detail, true)
	strategy := ""
	if outcome != nil {
		strategy = outcome.Strategy
	}
	d.finishProgress(paperID, "failed", strategy, detail)
	log.Warn("downloader: failed", "error", err)
	if d.reg != nil {
		if _, uerr := d.reg.UpdateStatus(ctx, paperID, "failed"); uerr != nil {
			log.Warn("downloader: could not mark paper failed", "error", uerr)
		}
	}
}

// Shutdown drains the queue like the ingester.
func (d *Downloader) Shutdown(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.done)
	})
	drained := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SnapshotCounters returns healthz-style counters.
func (d *Downloader) SnapshotCounters() map[string]int64 {
	return map[string]int64{
		"queued":    d.queued.Load(),
		"in_flight": d.inFlight.Load(),
		"succeeded": d.succeeded.Load(),
		"failed":    d.failed.Load(),
		"skipped":   d.skipped.Load(),
	}
}

func hasPDFAsset(detail *registry.PaperDetail) bool {
	if detail == nil {
		return false
	}
	for _, a := range detail.Assets {
		if a.PDFPath != "" {
			return true
		}
	}
	return false
}

// bucketRelKey mirrors the ingest convention (strip "<kind>/").
func bucketRelKey(assetKey string) string {
	if i := strings.IndexByte(assetKey, '/'); i >= 0 {
		return assetKey[i+1:]
	}
	return assetKey
}
