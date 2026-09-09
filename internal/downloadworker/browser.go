package downloadworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Browser owns exactly one local process tree, or probes an externally owned CDP.
// The shell entrypoint must NOT launch Chrome. No CDP port is published.
type Browser struct {
	URL    string
	local  bool
	alive  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
}

func browserArgs(binary, profile string) []string {
	args := []string{"--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--user-data-dir=" + profile}
	// headless-shell is already headless and does not support full Chrome's mode selector.
	if !strings.Contains(filepath.Base(binary), "headless-shell") {
		args = append(args, "--headless=new")
	}
	return append(args, "about:blank")
}

func StartBrowser(ctx context.Context, external, binary string) (*Browser, error) {
	if external != "" {
		u, err := url.Parse(external)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
			return nil, errors.New("invalid external CDP URL")
		}
		return &Browser{URL: external}, nil
	}
	if binary == "" {
		for _, candidate := range []string{"headless-shell", "google-chrome", "chrome", "chromium"} {
			if found, err := exec.LookPath(candidate); err == nil {
				binary = found
				break
			}
		}
	}
	if binary == "" {
		return nil, errors.New("no browser found; install headless-shell or set DL_BROWSER_CDP_URL")
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &Browser{URL: "http://127.0.0.1:9222", local: true, cancel: cancel, done: make(chan struct{})}
	if probeCDP(ctx, b.URL) {
		cancel()
		return nil, errors.New("local CDP port already occupied; use explicit external CDP URL")
	}
	profile, err := os.MkdirTemp("", "qatlas-worker-chrome-")
	if err != nil {
		cancel()
		return nil, errors.New("cannot create browser profile")
	}
	go func() {
		defer close(b.done)
		defer os.RemoveAll(profile)
		for bctx.Err() == nil {
			cmd := exec.Command(binary, browserArgs(binary, profile)...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			if cmd.Start() == nil {
				b.alive.Store(true)
				exited := make(chan error, 1)
				go func() { exited <- cmd.Wait() }()
				select {
				case <-exited:
					// Chromium can leave helpers behind even when the root exits.
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				case <-bctx.Done():
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
					timer := time.NewTimer(5 * time.Second)
					select {
					case <-exited:
						if !timer.Stop() {
							<-timer.C
						}
					case <-timer.C:
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						<-exited
					}
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
				b.alive.Store(false)
			}
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-bctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return b, nil
}

func probeCDP(ctx context.Context, endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	u.Path = "/json/version"
	u.RawQuery = ""
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&version) == nil && version.WebSocketDebuggerURL != ""
}

func (b *Browser) Healthy(ctx context.Context) bool {
	return b != nil && (!b.local || b.alive.Load()) && probeCDP(ctx, b.URL)
}
func (b *Browser) Close() {
	if b != nil && b.cancel != nil {
		b.cancel()
		<-b.done
	}
}
