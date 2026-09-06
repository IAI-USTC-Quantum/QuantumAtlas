// Command-line surface for the robust downloader.
//
// `qatlasd downloader probe` is the live robustness harness: it runs
// the full strategy ladder against real papers and prints a per-paper
// result table plus a failure-taxonomy summary. It powers the
// auto-debug loop ("random search results must all download"):
//
//	qatlasd downloader probe 10.1038/s41586-024-07806-9 arXiv:2401.12345
//	qatlasd downloader probe --random 30 --search "quantum computing"
//
// Exit code is 1 when any paper failed, so loops/scripts can branch.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/spf13/cobra"
)

type downloaderProbeFlags struct {
	random      int
	search      string
	dryRun      bool
	agent       bool
	browserCDP  string
	browserOn   bool
	concurrency int
	jsonOut     bool
	perPaperTO  time.Duration
}

func newDownloaderProbeFlags() downloaderProbeFlags {
	return downloaderProbeFlags{perPaperTO: 4 * time.Minute}
}

func NewDownloaderCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "downloader",
		Short: "Robust paper downloader utilities",
		Long: `Utilities around the robust multi-paradigm paper downloader
(internal/downloader; POST /api/downloader/* on the server side).

probe runs the acquisition ladder (arXiv direct -> OA metadata APIs ->
publisher URL patterns -> landing page -> agent fallback) against real
papers and reports per-paper outcomes plus a failure taxonomy. It is
the harness behind the robustness acceptance loop; the exit code is 1
when any paper failed.`,
	}
	root.AddCommand(newDownloaderProbeCmd())
	return root
}

func newDownloaderProbeCmd() *cobra.Command {
	flags := newDownloaderProbeFlags()
	cmd := &cobra.Command{
		Use:   "probe [identifier ...]",
		Short: "Run the download ladder against papers and report outcomes",
		Long: `Run the robust downloader's strategy ladder against the given
identifiers (DOIs / arXiv ids / paper URLs), or against N random works
sampled from OpenAlex (--random N, optionally filtered with --search).

The probe never writes to the registry or the object store; it validates
each fetched body exactly like the server path would (%PDF- magic,
trailer, size bounds) and reports which strategy succeeded.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDownloaderProbe(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, flags)
		},
	}
	cmd.Flags().IntVar(&flags.random, "random", 0, "sample N random works from OpenAlex instead of positional identifiers")
	cmd.Flags().StringVar(&flags.search, "search", "", "OpenAlex search filter for --random (e.g. \"quantum computing\")")
	cmd.Flags().BoolVar(&flags.agent, "agent", false, "force-enable the agent fallback (requires downloader.agent.* in config)")
	cmd.Flags().StringVar(&flags.browserCDP, "browser", "", "browser lane CDP endpoint override (e.g. ws://127.0.0.1:9222); implies --browser-on)")
	cmd.Flags().BoolVar(&flags.browserOn, "browser-on", false, "enable the browser lane using downloader.browser.cdp_url from config")
	cmd.Flags().IntVar(&flags.concurrency, "concurrency", 3, "parallel papers")
	cmd.Flags().BoolVar(&flags.jsonOut, "json", false, "emit machine-readable JSON instead of the table")
	cmd.Flags().DurationVar(&flags.perPaperTO, "timeout", flags.perPaperTO, "per-paper ladder budget")
	return cmd
}

// probeTarget is one unit of work.
type probeTarget struct {
	input string
	ref   registry.PaperRef
}

// probeResult is the per-paper outcome.
type probeResult struct {
	Input    string               `json:"input"`
	OK       bool                 `json:"ok"`
	Strategy string               `json:"strategy,omitempty"`
	URL      string               `json:"url,omitempty"`
	Size     int64                `json:"size,omitempty"`
	Sha256   string               `json:"sha256,omitempty"`
	Error    string               `json:"error,omitempty"`
	Attempts []downloader.Attempt `json:"attempts"`
}

// browserCDP resolves the probe's browser-lane endpoint: --browser
// wins, else --browser-on uses the config value.
func browserCDP(cfg *config.Config, flags downloaderProbeFlags) string {
	if flags.browserCDP != "" {
		return flags.browserCDP
	}
	if flags.browserOn {
		return cfg.DownloaderBrowserCDPURL
	}
	return ""
}

func runDownloaderProbe(stdout, stderr io.Writer, args []string, flags downloaderProbeFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if !cfg.PaperAccessEnabled {
		return errors.New("paper_access.enabled is off — the downloader needs the fetcher/resolver wiring")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	targets, err := collectProbeTargets(ctx, cfg, args, flags, stderr)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return errors.New("no targets (pass identifiers or --random N)")
	}

	contact := strings.TrimSpace(cfg.OpenAlexMailto)
	fetcher, ferr := arxiv.New(arxiv.Config{UserAgent: arxiv.BuildUserAgent(Version, contact), RPS: int(cfg.ArxivFetchRPS)})
	if ferr != nil {
		return ferr
	}
	resolver := openalex.New(openalex.Config{Mailto: contact})

	agentCfg := downloader.AgentConfig{
		Backend:      cfg.DownloaderAgentBackend,
		BaseURL:      cfg.DownloaderAgentBaseURL,
		APIKey:       cfg.DownloaderAgentAPIKey,
		Model:        cfg.DownloaderAgentModel,
		MaxTokens:    cfg.DownloaderAgentMaxTokens,
		ClaudeBin:    cfg.DownloaderAgentClaudeBin,
		ClaudeModel:  cfg.DownloaderAgentClaudeModel,
		Timeout:      cfg.DownloaderAgentTimeout,
		MaxBudgetUSD: cfg.DownloaderAgentMaxBudgetUSD,
	}
	if flags.agent {
		if agentCfg.Backend == "" {
			fmt.Fprintln(stderr, "note: --agent requested but downloader.agent.backend is unset; fallback stays off")
		}
	}
	unpaywallEmail := cfg.DownloaderUnpaywallEmail
	if unpaywallEmail == "" {
		unpaywallEmail = contact
	}
	dl := downloader.New(nil, nil, fetcher, resolver, downloader.Config{
		Concurrency:    flags.concurrency,
		UnpaywallEmail: unpaywallEmail,
		S2APIKey:       cfg.DownloaderS2APIKey,
		Fetch: downloader.FetchConfig{
			RespectRobots: cfg.DownloaderRespectRobots,
		},
		Agent: agentCfg,
		Browser: downloader.BrowserConfig{
			CDPURL:  browserCDP(cfg, flags),
			Timeout: cfg.DownloaderBrowserTimeout,
		},
	})
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = dl.Shutdown(shutCtx)
	}()

	sem := make(chan struct{}, flags.concurrency)
	var wg sync.WaitGroup
	results := make([]probeResult, len(targets))
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t probeTarget) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = probeResult{Input: t.input, Error: "cancelled"}
				return
			}
			defer func() { <-sem }()
			pctx, cancel := context.WithTimeout(ctx, flags.perPaperTO)
			defer cancel()
			res := probeResult{Input: t.input}
			outcome, err := dl.FetchPDF(pctx, t.ref)
			if outcome != nil {
				res.Attempts = outcome.Trace
			}
			if err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
				res.Strategy = outcome.Strategy
				res.URL = outcome.URL
				res.Size = outcome.Result.Size
				res.Sha256 = outcome.Result.Sha256
			}
			results[i] = res
		}(i, t)
	}
	wg.Wait()

	return reportProbe(stdout, results, flags)
}

// collectProbeTargets resolves positional identifiers and/or samples
// random works from OpenAlex.
func collectProbeTargets(ctx context.Context, cfg *config.Config, args []string, flags downloaderProbeFlags, stderr io.Writer) ([]probeTarget, error) {
	var targets []probeTarget
	for _, raw := range args {
		id, err := downloader.ParseIdentifier(raw)
		if err != nil {
			fmt.Fprintf(stderr, "skipping %q: %v\n", raw, err)
			continue
		}
		targets = append(targets, probeTarget{input: raw, ref: id.Ref})
	}
	if flags.random > 0 {
		dois, err := sampleOpenAlexDOIs(ctx, flags.random, flags.search, cfg.OpenAlexMailto)
		if err != nil {
			return targets, fmt.Errorf("openalex sample: %w", err)
		}
		for _, doi := range dois {
			targets = append(targets, probeTarget{input: doi, ref: registry.PaperRef{DOI: doi}})
		}
	}
	return targets, nil
}

// sampleOpenAlexDOIs draws n random works from OpenAlex (sample=N),
// optionally scoped by a search filter, and returns their DOIs.
func sampleOpenAlexDOIs(ctx context.Context, n int, search, mailto string) ([]string, error) {
	q := url.Values{}
	q.Set("per-page", fmt.Sprintf("%d", n))
	q.Set("filter", "has_doi:true")
	if search == "" {
		// sample cannot be combined with search; without a search we
		// sample uniformly so every draw is a fresh random set.
		q.Set("sample", fmt.Sprintf("%d", n))
	}
	if search != "" {
		q.Set("search", search)
	}
	if mailto != "" {
		q.Set("mailto", mailto)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openalex.org/works?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %.200s", resp.StatusCode, body)
	}
	var parsed struct {
		Results []struct {
			DOI string `json:"doi"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var out []string
	for _, w := range parsed.Results {
		doi := strings.TrimPrefix(w.DOI, "https://doi.org/")
		if doi == "" || w.DOI == "" {
			continue
		}
		out = append(out, doi)
	}
	return out, nil
}

// reportProbe prints the table (or JSON) and the summary; returns an
// error when any paper failed (non-nil error → exit code 1).
func reportProbe(stdout io.Writer, results []probeResult, flags downloaderProbeFlags) error {
	if flags.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"results": results})
	} else {
		for _, r := range results {
			if r.OK {
				fmt.Fprintf(stdout, "OK    %-45s strategy=%-22s %8d B  %s\n", truncate(r.Input, 45), r.Strategy, r.Size, truncate(r.URL, 60))
			} else {
				last := ""
				if n := len(r.Attempts); n > 0 {
					a := r.Attempts[n-1]
					last = fmt.Sprintf(" [%s: %s]", a.Strategy, firstLine(a.Error))
				}
				fmt.Fprintf(stdout, "FAIL  %-45s %s%s\n", truncate(r.Input, 45), firstLine(r.Error), last)
				if !flags.jsonOut {
					for _, a := range r.Attempts {
						status := "err "
						if a.Error == "" {
							status = "ok  "
						}
						fmt.Fprintf(stdout, "        %s %-24s %-60s %s\n", status, a.Strategy, truncate(a.URL, 60), firstLine(a.Error))
					}
				}
			}
		}
	}

	okCount := 0
	byStrategy := map[string]int{}
	failTaxonomy := map[string]int{}
	for _, r := range results {
		if r.OK {
			okCount++
			byStrategy[r.Strategy]++
			continue
		}
		cls := classifyFailure(r)
		failTaxonomy[cls]++
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "summary: %d/%d ok\n", okCount, len(results))
	for s, n := range byStrategy {
		fmt.Fprintf(stdout, "  strategy %-24s %d\n", s, n)
	}
	for c, n := range failTaxonomy {
		fmt.Fprintf(stdout, "  failure  %-24s %d\n", c, n)
	}
	if okCount < len(results) {
		return errors.New("probe had failures")
	}
	return nil
}

// classifyFailure buckets a failed result for the summary.
func classifyFailure(r probeResult) string {
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		a := r.Attempts[i]
		if a.Error == "" {
			continue
		}
		switch {
		case strings.Contains(a.Error, "bot challenge"), strings.Contains(a.Error, "pow_challenge"):
			return "bot_challenge"
		case strings.Contains(a.Error, "paywall"):
			return "paywall"
		case strings.Contains(a.Error, "robots.txt"):
			return "robots_blocked"
		case strings.Contains(a.Error, "not a PDF"):
			return "not_pdf"
		case strings.Contains(a.Error, "http 404"):
			return "404"
		case strings.Contains(a.Error, "http 403"):
			return "403"
		case strings.Contains(a.Error, "rate"):
			return "rate_limited"
		}
	}
	if len(r.Attempts) == 0 {
		return "no_candidates"
	}
	return "other"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s, 120)
}
