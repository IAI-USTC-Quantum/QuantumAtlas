// downloaderworker is an outbound-only fleet runner. It never starts an HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloadworker"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
)

func config(args []string) (downloadworker.Config, bool, error) {
	c := downloadworker.DefaultConfig()
	c.Name, _ = os.Hostname()
	envString := func(name string, dst *string) {
		if value := os.Getenv(name); value != "" {
			*dst = value
		}
	}
	envString("DL_WORKER_MASTER_URL", &c.MasterURL)
	envString("DL_WORKER_NAME", &c.Name)
	envString("DL_WORKER_DATA_DIR", &c.DataDir)
	envString("DL_BROWSER_CDP_URL", &c.BrowserCDPURL)
	envString("DL_BROWSER_BINARY", &c.BrowserBinary)
	envString("DL_UNPAYWALL_EMAIL", &c.UnpaywallEmail)
	if value := os.Getenv("DL_WORKER_CONCURRENCY"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return c, false, errors.New("invalid DL_WORKER_CONCURRENCY")
		}
		c.Concurrency = n
	}
	if value := os.Getenv("DL_WORKER_MAX_SPOOL_BYTES"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return c, false, errors.New("invalid DL_WORKER_MAX_SPOOL_BYTES")
		}
		c.MaxSpoolBytes = n
	}
	if value := os.Getenv("DL_WORKER_RESULT_TTL"); value != "" {
		n, err := time.ParseDuration(value)
		if err != nil {
			return c, false, errors.New("invalid DL_WORKER_RESULT_TTL")
		}
		c.ResultTTL = n
	}
	f := flag.NewFlagSet("downloaderworker", flag.ContinueOnError)
	f.StringVar(&c.MasterURL, "master-url", c.MasterURL, "master origin (HTTPS required)")
	f.StringVar(&c.Name, "name", c.Name, "display name")
	f.StringVar(&c.DataDir, "data-dir", c.DataDir, "durable identity and spool volume")
	f.IntVar(&c.Concurrency, "concurrency", c.Concurrency, "maximum concurrent downloads (1..32)")
	f.Int64Var(&c.MaxSpoolBytes, "max-spool-bytes", c.MaxSpoolBytes, "PDF spool quota in bytes")
	f.DurationVar(&c.ResultTTL, "result-ttl", c.ResultTTL, "unarchived PDF retention")
	f.BoolVar(&c.AllowHTTP, "allow-http", false, "allow plaintext master HTTP ONLY on a trusted development network")
	f.StringVar(&c.BrowserCDPURL, "browser-cdp-url", c.BrowserCDPURL, "optional externally supervised CDP endpoint")
	f.StringVar(&c.BrowserBinary, "browser-binary", c.BrowserBinary, "local Chrome binary (auto prefers headless-shell)")
	f.StringVar(&c.UnpaywallEmail, "unpaywall-email", c.UnpaywallEmail, "Unpaywall contact email")
	f.DurationVar(&c.TaskTimeout, "task-timeout", c.TaskTimeout, "maximum per-download duration")
	health := f.Bool("healthcheck", false, "check local runner health file and exit; no listener")
	if err := f.Parse(args); err != nil {
		return c, false, err
	}
	if f.NArg() != 0 {
		return c, false, errors.New("unexpected positional arguments")
	}
	// Credentials are env-only so --help cannot accidentally print them as defaults.
	c.EnrollmentToken = os.Getenv("DL_WORKER_ENROLLMENT_TOKEN")
	c.S2APIKey = os.Getenv("DL_S2_API_KEY")
	c.MasterURL = strings.TrimRight(c.MasterURL, "/")
	return c, *health, nil
}
func run(args []string) error {
	cfg, health, err := config(args)
	if err != nil {
		return err
	}
	if health {
		return downloadworker.CheckHealth(cfg.DataDir, time.Now())
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	spool, err := downloadworker.OpenSpool(cfg.DataDir, cfg.MaxSpoolBytes, cfg.ResultTTL)
	if err != nil {
		return errors.New("cannot open durable worker spool")
	}
	defer spool.Close()
	identity, err := spool.LoadIdentity(cfg.MasterURL)
	if err != nil {
		return err
	}
	client, err := downloadworker.NewClient(cfg, identity)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	browser, err := downloadworker.StartBrowser(ctx, cfg.BrowserCDPURL, cfg.BrowserBinary)
	if err != nil {
		return err
	}
	defer browser.Close()
	publicHTTP := downloadworker.NewPublicHTTPClient()
	defer publicHTTP.CloseIdleConnections()
	fetcher, err := arxiv.New(arxiv.Config{UserAgent: arxiv.BuildUserAgent("downloaderworker", cfg.UnpaywallEmail), RPS: 1, HTTPClient: publicHTTP})
	if err != nil {
		return errors.New("cannot initialize arXiv fetcher")
	}
	resolver := openalex.New(openalex.Config{Mailto: cfg.UnpaywallEmail})
	// Proxy/Remote/Agent remain zero: a worker never delegates back to a master.
	dl := downloader.New(nil, nil, fetcher, resolver, downloader.Config{Concurrency: cfg.Concurrency, UnpaywallEmail: cfg.UnpaywallEmail, S2APIKey: cfg.S2APIKey, Fetch: downloader.FetchConfig{MaxPDFBytes: downloadworker.MaxPDFBytes, HTTPClient: publicHTTP}, Browser: downloader.BrowserConfig{CDPURL: browser.URL, MaxPDFBytes: downloadworker.MaxPDFBytes}})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dl.Shutdown(ctx)
	}()
	runner := downloadworker.Runner{Config: cfg, Spool: spool, Identity: identity, Client: client, Fetcher: dl, BrowserHealthy: browser.Healthy, Log: slog.Default()}
	return runner.Run(ctx)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "downloaderworker:", err)
		os.Exit(1)
	}
}
