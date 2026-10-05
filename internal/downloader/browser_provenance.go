package downloader

import (
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"golang.org/x/net/html"
)

// browserNavigationScope is the acceptance boundary, not a PDF URL heuristic.
// Fetch Request + Response interception ties bytes to the actively chosen main
// frame navigation. A redirect is admitted only via redirectedRequestId AND
// the preceding response's Location header. Iframes, preloads, XHRs, unrelated
// JS navigations, and late responses from an abandoned navigation never qualify.
// Methods run synchronously in ListenTarget before any body-read goroutine.
type browserNavigationScope struct {
	mu         sync.Mutex
	frame      cdp.FrameID
	selected   string
	generation uint64
	requests   map[fetch.RequestID]browserScopedRequest
}

type browserScopedRequest struct {
	url       string
	nextURL   string
	redirects int
}

type browserCapturePermit struct {
	generation uint64
	request    fetch.RequestID
	url        string
}

func (s *browserNavigationScope) selectNavigation(frame cdp.FrameID, rawURL string) bool {
	u := browserHTTPURL(rawURL)
	if frame == "" || u == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frame, s.selected = frame, u
	s.generation++
	s.requests = make(map[fetch.RequestID]browserScopedRequest)
	return true
}

func (s *browserNavigationScope) observe(e *fetch.EventRequestPaused) (browserCapturePermit, bool) {
	if e == nil || e.Request == nil || e.ResourceType != network.ResourceTypeDocument {
		return browserCapturePermit{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := browserHTTPURL(e.Request.URL)
	if e.FrameID == "" || e.FrameID != s.frame || u == "" || s.selected == "" {
		return browserCapturePermit{}, false
	}
	isResponse := e.ResponseStatusCode != 0 || e.ResponseErrorReason != ""
	if !isResponse {
		request := browserScopedRequest{url: u}
		if e.RedirectedRequestID != "" {
			parent, ok := s.requests[e.RedirectedRequestID]
			if !ok || parent.nextURL != u || parent.redirects >= 8 {
				return browserCapturePermit{}, false
			}
			request.redirects = parent.redirects + 1
		} else if u != s.selected {
			return browserCapturePermit{}, false
		}
		s.requests[e.RequestID] = request
		return browserCapturePermit{}, false // request stage never has a body
	}
	request, ok := s.requests[e.RequestID]
	if !ok || request.url != u {
		return browserCapturePermit{}, false
	}
	switch e.ResponseStatusCode {
	case 301, 302, 303, 307, 308:
		for _, header := range e.ResponseHeaders {
			if strings.EqualFold(header.Name, "Location") {
				request.nextURL = browserHTTPURL(absolutize(header.Value, u))
				break
			}
		}
		s.requests[e.RequestID] = request
		return browserCapturePermit{}, false
	}
	if e.ResponseErrorReason != "" {
		return browserCapturePermit{}, false
	}
	return browserCapturePermit{s.generation, e.RequestID, u}, true
}

func (s *browserNavigationScope) current(permit browserCapturePermit) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[permit.request]
	return ok && permit.generation == s.generation && request.url == permit.url
}

func (s *browserNavigationScope) currentBody(body *browserBody) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return body != nil && body.navigation == s.generation
}

// Normalize fragments away (they are not in the network request) but preserve
// paths, signed query strings, and ports. Invalid/non-HTTP URLs fail closed.
func browserHTTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment, u.RawFragment = "", ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// documentBaseURL mirrors the first <base href> in the parsed document. It is
// for URL resolution ONLY: the authoritative page host always stays pageURL.
// The live DOM passes document.baseURI directly, including dynamic <base>.
func documentBaseURL(source []byte, pageURL string) string {
	tokens := html.NewTokenizer(strings.NewReader(string(source)))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return pageURL
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			if token.Data != "base" {
				continue
			}
			for _, attr := range token.Attr {
				if attr.Key == "href" {
					href := strings.TrimSpace(attr.Val)
					if href == "" {
						return pageURL
					}
					if _, err := url.Parse(href); err != nil {
						return pageURL
					}
					if resolved := absolutize(href, pageURL); resolved != "" {
						return resolved
					}
					return pageURL
				}
			}
		}
	}
}
