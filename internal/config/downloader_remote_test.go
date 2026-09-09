package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloaderRemoteDefaults(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "paper_access:\n  enabled: true\ndownloader:\n  remote:\n    enabled: true\n")
	cfg := mustLoad(t, path)
	if !cfg.DownloaderRemoteEnabled || cfg.DownloaderRemoteMaxInFlight != 6 || cfg.DownloaderRemoteMaxWorkerInFlight != 2 || cfg.DownloaderRemoteMaxAttempts != 3 {
		t.Fatalf("unexpected worker defaults: %+v", cfg)
	}
	if cfg.DownloaderRemoteTaskTimeout != 15*time.Minute || cfg.DownloaderRemoteWorkerTimeout != 6*time.Minute || cfg.DownloaderRemoteLeaseDuration != time.Minute {
		t.Fatal("unexpected timeout defaults")
	}
	if cfg.DownloaderRemoteSpoolDir != filepath.Join(cfg.PBDataDir, "downloader-spool") || cfg.DownloaderRemoteSpoolMaxBytes != 2<<30 {
		t.Fatal("unexpected spool defaults")
	}
}
func TestDownloaderRemoteValidation(t *testing.T) {
	clearConfigEnv(t)
	for name, body := range map[string]string{
		"proxy conflict":   "paper_access:\n  enabled: true\ndownloader:\n  proxy:\n    url: http://legacy:8602\n  remote:\n    enabled: true\n",
		"paper access off": "downloader:\n  remote:\n    enabled: true\n",
		"no capacity":      "paper_access:\n  enabled: true\ndownloader:\n  remote:\n    enabled: true\n    max_in_flight: 0\n",
		"worker budget":    "paper_access:\n  enabled: true\ndownloader:\n  remote:\n    enabled: true\n    worker_timeout: 20m\n",
		"lease too short":  "paper_access:\n  enabled: true\ndownloader:\n  remote:\n    enabled: true\n    lease_duration: 1s\n",
		"spool quota":      "paper_access:\n  enabled: true\ndownloader:\n  remote:\n    enabled: true\n    spool_max_bytes: 1000\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "downloader.remote") {
				t.Fatalf("wanted remote validation error, got %v", err)
			}
		})
	}
}
func TestDownloaderRemoteSpoolRelativeToConfig(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "downloader:\n  remote:\n    spool_dir: uploads\n")
	cfg := mustLoad(t, path)
	if cfg.DownloaderRemoteSpoolDir != filepath.Join(filepath.Dir(path), "uploads") {
		t.Fatalf("spool=%s", cfg.DownloaderRemoteSpoolDir)
	}
}
