package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

func TestAttemptDiagnosticClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{context.Canceled, "cancelled"}, {context.DeadlineExceeded, "timeout"}, {ErrBrowserTimeout, "timeout"},
		{ErrChallenge, "challenge"}, {ErrPaywall, "paywall"}, {ErrRobots, "robots"}, {ErrHTTP, "http_status"}, {ErrUpstream, "upstream"},
		{ErrNotPDF, "not_pdf"}, {ErrTooLarge, "too_large"}, {ErrTooSmall, "too_small"}, {ErrTruncated, "truncated"},
		{errNoCandidates, "no_candidates"}, {errNoLandingCandidates, "no_candidates"}, {ErrBrowserNotConfigured, "not_configured"},
		{errors.New("http 403 SECRET <title>Access Denied</title>"), "unknown"},
	} {
		a := attemptOf("test", "https://SECRET", fmt.Errorf("SECRET: %w", tc.err), time.Now().Add(-25*time.Millisecond))
		if a.FailureKind != tc.kind || a.HTTPStatus != 0 || a.PageTitle != "" || a.Millis < 25 {
			t.Fatalf("attempt=%+v want %s", a, tc.kind)
		}
		if strings.Contains(SafeAttemptDiagnostic(a), "SECRET") {
			t.Fatal("raw error leaked")
		}
	}
}

func TestSafeAttemptDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		a    Attempt
		want string
	}{
		{Attempt{}, ""}, {Attempt{Error: "SECRET"}, "strategy failed"},
		{Attempt{Error: "SECRET", FailureKind: "SECRET", HTTPStatus: 403, PageTitle: "SECRET"}, "strategy failed"},
		{Attempt{Error: "SECRET", FailureKind: "challenge", HTTPStatus: 403, PageTitle: "Just a moment..."}, `failure_kind=challenge http_status=403 page_title="Just a moment..."`},
		{Attempt{Error: "SECRET", FailureKind: "unknown", HTTPStatus: 999, PageTitle: "Access Denied SECRET"}, "failure_kind=unknown"},
		{Attempt{FailureKind: "challenge", HTTPStatus: 403, PageTitle: "Access Denied"}, ""},
	} {
		if got := SafeAttemptDiagnostic(tc.a); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	raw, err := json.Marshal(Attempt{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"failure_kind", "http_status", "page_title"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("optional field present: %s", raw)
		}
	}
}

func TestSafeHTMLTitle(t *testing.T) {
	for _, tc := range []struct{ html, want string }{
		{`<html><title>Just a moment...</title></html>`, "Just a moment..."},
		{`<html><TITLE> Access Denied </TITLE></html>`, "Access Denied"},
		{`<html><title>Just a moment&#8230;</title></html>`, "Just a moment..."},
		{`<html><title>Access Denied token=SECRET</title></html>`, ""},
		{`<html><title>Private paper SECRET</title></html>`, ""},
		{`<html><title>SECRET</title><title>Access Denied</title></html>`, ""},
		{`<html><title>Access Denied`, ""},
		{`%PDF-1.4 <title>Access Denied</title>`, ""},
	} {
		if got := safeHTMLTitle([]byte(tc.html)); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

func TestFetchDiagnosticsRealStatusAndTitle(t *testing.T) {
	for _, tc := range []struct {
		status      int
		title, kind string
		sentinel    error
	}{
		{403, "Just a moment...", "challenge", ErrChallenge},
		{403, "Access Denied", "paywall", ErrPaywall},
		{404, "SECRET", "http_status", ErrHTTP},
		{200, "Just a moment...", "challenge", ErrChallenge},
		{200, "SECRET", "not_pdf", ErrNotPDF},
	} {
		t.Run(fmt.Sprintf("%d-%s", tc.status, tc.title), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, "<html><title>%s</title></html>", tc.title)
			}))
			defer srv.Close()
			_, err := newClient(t, srv.URL, false).FetchPDF(context.Background(), srv.URL+"/test.pdf?token=SECRET")
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("error chain: %v", err)
			}
			a := attemptOf("fetch", "", err, time.Now())
			if a.FailureKind != tc.kind || a.HTTPStatus != tc.status || a.PageTitle != safePageTitle(tc.title) {
				t.Fatalf("attempt=%+v", a)
			}
			if requests != 1 {
				t.Fatalf("unexpected request count %d", requests)
			}
			if strings.Contains(SafeAttemptDiagnostic(a), "SECRET") {
				t.Fatal("secret leaked")
			}
		})
	}
}

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type diagnosticFailReader struct{}

func (diagnosticFailReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (diagnosticFailReader) Close() error             { return nil }

func TestFetchBodyAndTransportDiagnosticChains(t *testing.T) {
	c, _ := NewFetchClient(FetchConfig{HTTPClient: &http.Client{Transport: diagnosticTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: diagnosticFailReader{}, Request: r, Header: make(http.Header)}, nil
	})}})
	_, err := c.FetchPDF(context.Background(), "https://example.test/paper.pdf")
	a := attemptOf("fetch", "", err, time.Now())
	if !errors.Is(err, ErrUpstream) || !errors.Is(err, io.ErrUnexpectedEOF) || a.FailureKind != "body_read" || a.HTTPStatus != 200 {
		t.Fatalf("attempt=%+v err=%v", a, err)
	}
	c, _ = NewFetchClient(FetchConfig{HTTPClient: &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}})
	_, err = c.FetchPDF(context.Background(), "https://example.test/paper.pdf?token=SECRET")
	a = attemptOf("fetch", "", err, time.Now())
	if !errors.Is(err, ErrUpstream) || !errors.Is(err, context.DeadlineExceeded) || a.FailureKind != "timeout" || a.HTTPStatus != 0 {
		t.Fatalf("attempt=%+v err=%v", a, err)
	}
}

func TestResolverHTTPDiagnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	for _, key := range []string{"", "SECRET"} {
		var out any
		err := getS2JSON(context.Background(), srv.Client(), key, srv.URL, &out)
		a := attemptOf("oa:test", "", withDiagnostic(err, "resolve", 0, nil), time.Now())
		if a.FailureKind != "http_status" || a.HTTPStatus != 403 || a.PageTitle != "" || err.Error() != "http 403" {
			t.Fatalf("attempt=%+v err=%v", a, err)
		}
	}
}

func TestDownloaderOAHTTPDiagnostic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	d := New(nil, nil, nil, nil, Config{
		Fetch:       FetchConfig{LandingBaseURL: srv.URL, HostRPS: 10000},
		OAResolvers: []OAResolver{NewUnpaywall(srv.URL, "SECRET@example.test", time.Second)},
	})
	defer d.Shutdown(context.Background())
	out, err := d.FetchPDF(context.Background(), registry.PaperRef{DOI: "10.999999/test"})
	if !errors.Is(err, ErrNoPDF) || len(out.Trace) == 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	a := out.Trace[0]
	if a.Strategy != "oa:unpaywall" || a.FailureKind != "http_status" || a.HTTPStatus != 403 || a.Error != "unpaywall: http 403" {
		t.Fatalf("OA diagnostic=%+v", a)
	}
	if SafeAttemptDiagnostic(a) != "failure_kind=http_status http_status=403" {
		t.Fatalf("unsafe diagnostic: %s", SafeAttemptDiagnostic(a))
	}
}

func TestBrowserResponseDiagnostics(t *testing.T) {
	b := &BrowserLane{cfg: BrowserConfig{MaxPDFBytes: 1024}}
	for _, title := range []string{"Just a moment...", "Access Denied", "SECRET", "Just a moment... SECRET"} {
		body := []byte("<html><title>" + title + "</title></html>")
		_, err := b.validateBody(&browserBody{url: "https://example.test/SECRET", body: body, kind: ClassifyBody(body), status: 403})
		a := attemptOf("browser", "", err, time.Now())
		if a.HTTPStatus != 403 || a.PageTitle != safePageTitle(title) {
			t.Fatalf("attempt=%+v", a)
		}
		if strings.Contains(SafeAttemptDiagnostic(a), "SECRET") {
			t.Fatal("secret leaked")
		}
		if title == "Just a moment..." && (!errors.Is(err, ErrChallenge) || a.FailureKind != "challenge") {
			t.Fatalf("challenge lost: %+v", a)
		}
	}
}

type timedDiagnosticResolver struct {
	name       string
	candidates []string
	err        error
}

func (r timedDiagnosticResolver) Name() string { return r.name }
func (r timedDiagnosticResolver) Candidates(context.Context, string) ([]string, error) {
	time.Sleep(20 * time.Millisecond)
	return r.candidates, r.err
}

func TestResolverAndLandingAttemptDurations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		if r.URL.Path == "/error" {
			w.WriteHeader(403)
			fmt.Fprint(w, "<html><title>Just a moment...</title></html>")
			return
		}
		fmt.Fprint(w, "<html><title>SECRET</title></html>")
	}))
	defer srv.Close()
	d := New(nil, nil, nil, nil, Config{Fetch: FetchConfig{LandingBaseURL: srv.URL, HostRPS: 10000}, OAResolvers: []OAResolver{
		timedDiagnosticResolver{name: "empty"}, timedDiagnosticResolver{name: "error", err: errors.New("SECRET")},
		timedDiagnosticResolver{name: "repository", candidates: []string{srv.URL + "/empty", srv.URL + "/error"}},
	}})
	defer d.Shutdown(context.Background())
	out, err := d.FetchPDF(context.Background(), registry.PaperRef{DOI: "10.999999/test"})
	if !errors.Is(err, ErrNoPDF) {
		t.Fatalf("err=%v", err)
	}
	if len(out.Trace) != 5 {
		t.Fatalf("unexpected trace=%+v", out.Trace)
	}
	for _, a := range out.Trace {
		if a.Millis < 15 {
			t.Errorf("missing actual duration: %+v", a)
		}
		if a.FailureKind == "" {
			t.Errorf("missing kind: %+v", a)
		}
	}
	if a := out.Trace[3]; a.HTTPStatus != 403 || a.FailureKind != "challenge" {
		t.Fatalf("landing=%+v", a)
	}
}

func TestBrowserAttemptDurationsNotCumulative(t *testing.T) {
	var actual []int64
	fetch := func(_ context.Context, url string) (*FetchResult, error) {
		start := time.Now()
		if url == "first" {
			time.Sleep(100 * time.Millisecond)
		} else {
			time.Sleep(20 * time.Millisecond)
		}
		actual = append(actual, time.Since(start).Milliseconds())
		return nil, ErrBrowserTimeout
	}
	_, _, first := browserAttempt(context.Background(), "first", fetch)
	_, _, second := browserAttempt(context.Background(), "second", fetch)
	if first.Millis < 100 || second.Millis < 20 {
		t.Fatalf("durations missing: %d,%d", first.Millis, second.Millis)
	}
	if second.Millis-actual[1] >= first.Millis/2 {
		t.Fatalf("cumulative duration: first=%d second=%d actual=%v", first.Millis, second.Millis, actual)
	}
}
