// Package downloadworker implements the outbound-only downloader fleet runner.
package downloadworker

import (
	"errors"
	"net/url"
	"time"
)

const MaxPDFBytes int64 = 100 << 20

// Config is deliberately independent of the main server configuration.
type Config struct {
	MasterURL       string
	EnrollmentToken string
	Name            string
	DataDir         string
	Concurrency     int
	MaxSpoolBytes   int64
	ResultTTL       time.Duration
	AllowHTTP       bool
	BrowserCDPURL   string
	BrowserBinary   string
	UnpaywallEmail  string
	S2APIKey        string
	PollInterval    time.Duration
	TaskTimeout     time.Duration
}

func DefaultConfig() Config {
	return Config{DataDir: "/var/lib/qatlas-downloader", Concurrency: 2, MaxSpoolBytes: 10 << 30, ResultTTL: 24 * time.Hour, PollInterval: 10 * time.Second, TaskTimeout: 6 * time.Minute}
}

func (c Config) Validate() error {
	u, err := url.Parse(c.MasterURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.AllowHTTP && u.Scheme == "http")) {
		return errors.New("master URL must be HTTPS without userinfo/query/fragment (HTTP requires --allow-http)")
	}
	if c.Name == "" || c.DataDir == "" {
		return errors.New("worker name and data directory are required")
	}
	if c.Concurrency < 1 || c.Concurrency > 32 {
		return errors.New("concurrency must be between 1 and 32")
	}
	if c.MaxSpoolBytes < MaxPDFBytes {
		return errors.New("max spool bytes must reserve at least one 100 MiB PDF")
	}
	if c.ResultTTL <= 0 || c.PollInterval <= 0 || c.TaskTimeout <= 0 {
		return errors.New("TTL, poll interval and task timeout must be positive")
	}
	return nil
}
