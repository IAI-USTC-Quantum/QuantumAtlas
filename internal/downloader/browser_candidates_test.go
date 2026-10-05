package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Issue #25: a valid PDF linked as a reference is not necessarily the
// requested article. Browser fallback must not undo the HTTP miner's
// citation-metadata priority or follow arbitrary external PDF anchors.
func TestBrowserCandidatesArticleScope(t *testing.T) {
	const base = "https://publisher.example/doi/10.1145/3800579"
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "citation metadata excludes all reference links",
			html: `<meta name="citation_pdf_url" content="https://cdn.example/article.pdf?sig=a&amp;b=2"><a href="/references/old.pdf">Reference</a><a href="https://author.example/jacokes.pdf">Reference</a>`,
			want: []string{"https://cdn.example/article.pdf?sig=a&b=2"},
		},
		{
			name: "content first citation metadata wins",
			html: `<meta content="/article.pdf" name="citation_pdf_url"><script>{"pdfUrl":"/other.pdf"}</script><a href="/related.pdf">Related</a>`,
			want: []string{"https://publisher.example/article.pdf"},
		},
		{
			name: "external reference without citation metadata is not a candidate",
			html: `<a href="https://author.example/jacokes.pdf">An Improved Quantum Algorithm for Searching an Ordered List</a>`,
		},
		{
			name: "protocol relative reference is also external",
			html: `<a href="//author.example/jacokes.pdf">Reference</a>`,
		},
		{
			name: "same host and relative fallback",
			html: `<a href="/article.pdf?token=a&amp;b=2">PDF</a><a href="https://publisher.example/second.pdf">PDF</a><a href="/article.pdf?token=a&amp;b=2">Duplicate</a>`,
			want: []string{"https://publisher.example/article.pdf?token=a&b=2", "https://publisher.example/second.pdf"},
		},
		{
			name: "same host inline pdf metadata precedes raw links",
			html: `<script>{"pdfUrl":"/article.pdf"}</script><a href="/references/old.pdf">Reference</a>`,
			want: []string{"https://publisher.example/article.pdf"},
		},
		{
			name: "external inline reference is not trusted metadata",
			html: `<script>{"pdf_url":"https://author.example/jacokes.pdf"}</script>`,
		},
		{
			name: "publisher download endpoint shapes remain supported",
			html: `<a href="/doi/pdf/10.1145/3800579">PDF</a><a href="/article/pdf">PDF</a><a href="/getPDF?id=10">PDF</a><a href="/stamp/stamp.jsp?arnumber=10">PDF</a><a href="/doi/epdf/10.1145/3800579">Viewer</a>`,
			want: []string{"https://publisher.example/doi/pdf/10.1145/3800579", "https://publisher.example/article/pdf", "https://publisher.example/getPDF?id=10", "https://publisher.example/stamp/stamp.jsp?arnumber=10"},
		},
		{
			name: "non HTTP citation URL is not navigable",
			html: `<meta name="citation_pdf_url" content="javascript:download()">`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mineBrowserHTML(&browserBody{url: base, body: []byte(tt.html)})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("candidates = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// These tests exercise the actual DOM snapshot and Fetch listener against a
// disposable local Chrome, not just the URL extraction helper. No real service
// flags, publisher network, or operator browser profile are used.
func localProvenanceChrome(t *testing.T) (context.Context, string) {
	t.Helper()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		for _, name := range []string{"chromium", "chromium-browser", "headless-shell"} {
			if chrome, err = exec.LookPath(name); err == nil {
				break
			}
		}
	}
	if err != nil {
		t.Skip("local Chromium not installed")
	}
	profile := t.TempDir()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(chrome), chromedp.UserDataDir(profile), chromedp.NoSandbox,
		chromedp.Flag("remote-debugging-port", "0"), chromedp.Flag("disable-dev-shm-usage", true))
	alloc, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(ctx, 5*time.Second)
		_ = chromedp.Cancel(closeCtx)
		closeCancel()
		cancel()
		allocCancel()
	})
	if err := chromedp.Run(ctx); err != nil {
		t.Fatalf("start disposable local Chrome: %v", err)
	}
	activePort, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		t.Fatal(err)
	}
	port := strings.SplitN(string(activePort), "\n", 2)[0]
	return ctx, "http://127.0.0.1:" + port
}

func TestBrowserProvenanceRenderedBaseURI(t *testing.T) {
	ctx, _ := localProvenanceChrome(t)
	cdn := httptest.NewServer(http.NotFoundHandler())
	defer cdn.Close()
	var html string
	publisher := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	}))
	defer publisher.Close()
	b := &BrowserLane{}
	for _, tt := range []struct {
		name, html string
		want       []string
	}{
		{"citation resolves external base", `<base href="` + cdn.URL + `/assets/"><meta name="citation_pdf_url" content="article.pdf">`, []string{cdn.URL + "/assets/article.pdf"}},
		{"weak relative target cannot inherit base host", `<base href="` + cdn.URL + `/assets/"><a href="reference.pdf">PDF</a>`, nil},
		{"weak publisher absolute target survives external base", `<base href="` + cdn.URL + `/assets/"><a href="` + publisher.URL + `/article.pdf">PDF</a>`, []string{publisher.URL + "/article.pdf"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			html = tt.html
			if err := chromedp.Run(ctx, chromedp.Navigate(publisher.URL+"/paper")); err != nil {
				t.Fatal(err)
			}
			if got := b.mineRenderedDOM(ctx); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("live DOM candidates = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBrowserProvenanceCaptureScope(t *testing.T) {
	_, cdpURL := localProvenanceChrome(t)
	const correct = "%PDF-1.4\nCORRECT ARTICLE\nstartxref\n0\n%%EOF"
	const reference = "%PDF-1.4\nWRONG REFERENCE\nstartxref\n0\n%%EOF"
	for _, tt := range []struct {
		name, injection string
		wantPDF         bool
	}{
		{"iframe PDF cannot win", `<iframe src="/reference.pdf"></iframe>`, true},
		{"preloaded reference PDF cannot win", `<script>fetch('/reference.pdf')</script>`, true},
		{"iframe HTML cannot steer navigation", `<iframe src="/reference-frame"></iframe>`, true},
		{"unselected main frame navigation cannot win", `<script>setTimeout(()=>location.href='/reference.pdf',20)</script>`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/paper":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<html><head></head><body>Article`+tt.injection)
					if tt.wantPDF {
						fmt.Fprint(w, `<script>setTimeout(()=>{let m=document.createElement('meta');m.name='citation_pdf_url';m.content='/selected';document.head.appendChild(m)},100)</script>`)
					}
				case "/reference-frame":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, `<meta name="citation_pdf_url" content="/reference.pdf">`)
				case "/reference.pdf":
					w.Header().Set("Content-Type", "application/pdf")
					_, _ = io.WriteString(w, reference)
				case "/selected":
					http.Redirect(w, r, "/article.pdf", http.StatusFound)
				case "/article.pdf":
					w.Header().Set("Content-Type", "application/pdf")
					_, _ = io.WriteString(w, correct)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			b := NewBrowserLane(BrowserConfig{CDPURL: cdpURL, Timeout: 4 * time.Second})
			body, err := b.navigate(context.Background(), server.URL+"/paper")
			if tt.wantPDF {
				if err != nil || body == nil || string(body.body) != correct || body.url != server.URL+"/article.pdf" {
					t.Fatalf("capture = %+v, err=%v; expected only actively selected article redirect", body, err)
				}
			} else if err == nil && body != nil && body.kind == BodyPDF {
				t.Fatalf("accepted unselected main-frame PDF: %s", body.body)
			}
		})
	}
}

func TestLandingProvenanceBaseURI(t *testing.T) {
	cdn := httptest.NewServer(http.NotFoundHandler())
	defer cdn.Close()
	for _, tt := range []struct {
		name, html string
		want       []string
	}{
		{"citation respects base", `<base href="` + cdn.URL + `/assets/"><meta name="citation_pdf_url" content="article.pdf">`, []string{cdn.URL + "/assets/article.pdf"}},
		{"weak base host stays untrusted", `<base href="` + cdn.URL + `/assets/"><a href="reference.pdf">PDF</a>`, nil},
		{"iframe and preload are not PDF anchors", `<iframe src="/reference.pdf"></iframe><link rel="preload" href="/reference.pdf">`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tt.html) }))
			defer server.Close()
			fetcher, err := NewFetchClient(FetchConfig{})
			if err != nil {
				t.Fatal(err)
			}
			d := &Downloader{fetch: fetcher}
			info, err := d.scrapeLanding(context.Background(), server.URL+"/paper")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(info.Candidates, tt.want) {
				t.Fatalf("HTTP landing candidates = %#v, want %#v", info.Candidates, tt.want)
			}
		})
	}
}

func TestSameHostOrRelativeResolvedURL(t *testing.T) {
	const base = "https://publisher.example/article"
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{"/paper.pdf", true},
		{"paper.pdf", true},
		{"//publisher.example/paper.pdf", true},
		{"https://PUBLISHER.EXAMPLE/paper.pdf", true},
		{"//author.example/reference.pdf", false},
		{"https://author.example/reference.pdf", false},
		{"https://publisher.example.evil.test/reference.pdf", false},
		{"javascript:download()", false},
		{"data:application/pdf;base64,x", false},
		{"", false},
	} {
		t.Run(tt.url, func(t *testing.T) {
			if got := sameHostOrRelative(tt.url, base); got != tt.want {
				t.Fatalf("sameHostOrRelative(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
