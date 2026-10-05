package downloader

import (
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
)

// Controlled CDP event regression coverage for the same guard called directly
// by ListenTarget. The real Chrome tests cover that listener/capture integration;
// these cover malformed/misordered/stale events that a fixture server cannot
// reliably force a browser to produce.
func TestBrowserProvenanceCDPEvents(t *testing.T) {
	const main = cdp.FrameID("main")
	const selected = "https://publisher.example/paper"
	const final = "https://cdn.example/article.pdf?sig=a&b=2"
	event := func(id, rawURL string, frame cdp.FrameID, resource network.ResourceType, status int64) *fetch.EventRequestPaused {
		return &fetch.EventRequestPaused{RequestID: fetch.RequestID(id), Request: &network.Request{URL: rawURL}, FrameID: frame, ResourceType: resource, ResponseStatusCode: status}
	}
	s := &browserNavigationScope{}
	if !s.selectNavigation(main, selected) {
		t.Fatal("selected navigation not armed")
	}
	for _, e := range []*fetch.EventRequestPaused{
		event("iframe", selected, "child", network.ResourceTypeDocument, 0),
		event("prefetch", selected, main, network.ResourceTypeFetch, 0),
		event("unselected", final, main, network.ResourceTypeDocument, 0),
	} {
		s.observe(e)
		e.ResponseStatusCode = 200
		if _, capture := s.observe(e); capture {
			t.Fatalf("off-scope event accepted: %+v", e)
		}
	}
	if _, capture := s.observe(event("without-request", selected, main, network.ResourceTypeDocument, 200)); capture {
		t.Fatal("response without authorized request accepted")
	}
	s.observe(event("root", selected, main, network.ResourceTypeDocument, 0))
	redirect := event("root", selected, main, network.ResourceTypeDocument, 302)
	redirect.ResponseHeaders = []*fetch.HeaderEntry{{Name: "Location", Value: final}}
	if _, capture := s.observe(redirect); capture {
		t.Fatal("redirect body must not be captured as a PDF")
	}
	for _, tt := range []struct{ id, parent, url string }{
		{"wrong-parent", "not-root", final},
		{"wrong-location", "root", "https://cdn.example/reference.pdf"},
		{"unsafe-scheme", "root", "file:///tmp/reference.pdf"},
	} {
		e := event(tt.id, tt.url, main, network.ResourceTypeDocument, 0)
		e.RedirectedRequestID = fetch.RequestID(tt.parent)
		s.observe(e)
		e.ResponseStatusCode = 200
		if _, capture := s.observe(e); capture {
			t.Fatalf("forged redirect accepted: %+v", e)
		}
	}
	child := event("article", final, main, network.ResourceTypeDocument, 0)
	child.RedirectedRequestID = "root"
	s.observe(child)
	child.ResponseStatusCode = 200
	permit, capture := s.observe(child)
	if !capture || !s.current(permit) {
		t.Fatal("selected HTTP redirect chain not captured")
	}
	previous := &browserBody{navigation: permit.generation}
	s.selectNavigation(main, "https://publisher.example/next.pdf")
	if s.current(permit) || s.currentBody(previous) {
		t.Fatal("abandoned navigation can still win via a late body/channel item")
	}
	if _, capture := s.observe(child); capture {
		t.Fatal("late response can reauthorize an abandoned navigation")
	}
}

func TestBrowserProvenanceRedirectBound(t *testing.T) {
	s := &browserNavigationScope{}
	const main = cdp.FrameID("main")
	u := "https://publisher.example/start"
	s.selectNavigation(main, u)
	var parent fetch.RequestID
	for hop := 0; hop <= 9; hop++ {
		id := fetch.RequestID(fmt.Sprintf("hop-%d", hop))
		request := &fetch.EventRequestPaused{RequestID: id, Request: &network.Request{URL: u}, FrameID: main, ResourceType: network.ResourceTypeDocument, RedirectedRequestID: parent}
		s.observe(request)
		response := *request
		response.ResponseStatusCode = 200
		_, accepted := s.observe(&response)
		if accepted != (hop <= 8) {
			t.Fatalf("hop %d accepted=%v, expected bounded root+8 redirect responses", hop, accepted)
		}
		next := fmt.Sprintf("https://cdn.example/redirect-%d", hop+1)
		response.ResponseStatusCode = 302
		response.ResponseHeaders = []*fetch.HeaderEntry{{Name: "Location", Value: next}}
		s.observe(&response)
		parent, u = id, next
	}
}

func TestBrowserProvenanceRawBaseResolution(t *testing.T) {
	const pageURL = "https://publisher.example/paper"
	for _, tt := range []struct{ html, want string }{
		{`<base href=""><base href="https://cdn.example/">`, pageURL},
		{`<base target="_blank"><base href=/assets/><base href="https://cdn.example/">`, "https://publisher.example/assets/"},
		{`<!-- <base href="https://bad.example/"> --><BASE HREF="//cdn.example/assets/">`, "https://cdn.example/assets/"},
		{`<base href="https://%zz">`, pageURL},
	} {
		if got := documentBaseURL([]byte(tt.html), pageURL); got != tt.want {
			t.Errorf("base(%q)=%q, want %q", tt.html, got, tt.want)
		}
	}
}
