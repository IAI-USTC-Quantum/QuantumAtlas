package downloader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/html"
)

var (
	errNoCandidates           = errors.New("no candidates")
	errNoLandingCandidates    = errors.New("no PDF candidates on landing page")
	errNoRepositoryCandidates = errors.New("no PDF candidates on repository landing page")
)

// diagnosticError adds observations without changing the original error text or
// losing its errors.Is/As chain. Titles describe response HTML, never live DOM.
type diagnosticError struct {
	cause  error
	kind   string
	status int
	title  string
}

func (e *diagnosticError) Error() string { return e.cause.Error() }
func (e *diagnosticError) Unwrap() error { return e.cause }

func withDiagnostic(err error, kind string, status int, body []byte) error {
	if err == nil {
		return nil
	}
	title := safeHTMLTitle(body)
	var inner *diagnosticError
	if errors.As(err, &inner) {
		if inner.kind != "" {
			kind = inner.kind
		}
		if status == 0 {
			status, title = inner.status, inner.title
		}
	}
	return &diagnosticError{cause: err, kind: kind, status: status, title: title}
}

// safePageTitle uses exact whole-title matches, not substrings: an arbitrary
// title containing a challenge phrase may also contain credentials or PII.
func safePageTitle(title string) string {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "just a moment...":
		return "Just a moment..."
	case "just a moment…":
		return "Just a moment..."
	case "access denied":
		return "Access Denied"
	case "attention required! | cloudflare":
		return "Attention Required! | Cloudflare"
	case "verify you are human":
		return "Verify you are human"
	default:
		return ""
	}
}

func safeHTMLTitle(body []byte) string {
	// Only inspect the bounded HTML head; never interpret PDF bytes as HTML.
	if len(body) == 0 || ClassifyBody(body) == BodyPDF {
		return ""
	}
	if len(body) > 64<<10 {
		body = body[:64<<10]
	}
	z := html.NewTokenizer(strings.NewReader(string(body)))
	inTitle := false
	var title strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			name, _ := z.TagName()
			if string(name) == "title" {
				inTitle = true
			} else if string(name) == "body" {
				return ""
			}
		case html.TextToken:
			if inTitle {
				title.Write(z.Text())
				if title.Len() > 128 {
					return ""
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if inTitle && string(name) == "title" {
				return safePageTitle(title.String())
			}
		}
	}
}

func failureKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBrowserTimeout) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	for _, item := range []struct {
		err  error
		kind string
	}{
		{ErrChallenge, "challenge"}, {ErrPaywall, "paywall"}, {ErrRobots, "robots"},
		{ErrNotPDF, "not_pdf"}, {ErrTooLarge, "too_large"}, {ErrTooSmall, "too_small"}, {ErrTruncated, "truncated"},
		{errNoCandidates, "no_candidates"}, {errNoLandingCandidates, "no_candidates"}, {errNoRepositoryCandidates, "no_candidates"},
		{ErrBrowserNotConfigured, "not_configured"}, {ErrProxyNotConfigured, "not_configured"},
	} {
		if errors.Is(err, item.err) {
			return item.kind
		}
	}
	var de *diagnosticError
	if errors.As(err, &de) && de.kind == "body_read" {
		return "body_read"
	}
	if errors.Is(err, ErrHTTP) {
		return "http_status"
	}
	if errors.Is(err, ErrUpstream) {
		return "upstream"
	}
	if de != nil && safeFailureKind(de.kind) != "" {
		return de.kind
	}
	return "unknown"
}

func safeFailureKind(kind string) string {
	switch kind {
	case "cancelled", "timeout", "challenge", "paywall", "robots", "http_status", "upstream", "body_read", "not_pdf", "too_large", "too_small", "truncated", "no_candidates", "not_configured", "browser", "resolve", "unknown":
		return kind
	default:
		return ""
	}
}

// SafeAttemptDiagnostic formats only allowlisted structured observations. It
// never extracts values from Error, URL, Strategy, or arbitrary server titles.
// Legacy failures without observations retain a generic compatibility message.
func SafeAttemptDiagnostic(a Attempt) string {
	if a.Error == "" {
		return ""
	}
	kind := safeFailureKind(a.FailureKind)
	if kind == "" {
		return "strategy failed"
	}
	text := "failure_kind=" + kind
	if a.HTTPStatus >= 100 && a.HTTPStatus <= 599 {
		text += fmt.Sprintf(" http_status=%d", a.HTTPStatus)
	}
	if title := safePageTitle(a.PageTitle); title != "" {
		text += fmt.Sprintf(" page_title=%q", title)
	}
	return text
}
