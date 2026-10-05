package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

const externalFixtureTitle = "A Secure Quantum Algorithm for External Source Registration"

// Real PDF fixture, not a mock extractor: the production VerifyPublishedPDF
// invokes actual pdftotext on the bytes served by this local fixture.
func externalFixturePDF(lines ...string) []byte {
	var stream strings.Builder
	stream.WriteString("BT /F1 12 Tf 40 780 Td 20 TL\n")
	for _, line := range lines {
		fmt.Fprintf(&stream, "(%s) Tj T*\n", strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(line))
	}
	stream.WriteString("ET\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 842] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", stream.Len(), stream.String()),
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return pdf.Bytes()
}

// Map a public-looking URL to the test's local server only via an explicit
// transport. Production has no setting enabling loopback or this transport.
type externalFixtureTransport struct {
	client *http.Client
	base   string
}

func (f externalFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u, _ := url.Parse(f.base)
	clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
	response, err := f.client.Transport.RoundTrip(clone)
	if response != nil {
		response.Request = req
	}
	return response, err
}

func externalLocalFetcher(t *testing.T, handler http.Handler) *externalSourceFetcher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &externalSourceFetcher{client: &http.Client{
		Transport: externalFixtureTransport{client: server.Client(), base: server.URL},
		Timeout:   2 * time.Second, CheckRedirect: externalSourceCheckRedirect,
	}, maxBytes: externalSourceMaxBytes}
}

type externalFixtureCatalog struct {
	*fakeBlockCatalog
	byExternal    map[string]string
	registrations int
	err           error
}

func (c *externalFixtureCatalog) RegisterExternalSource(_ context.Context, ref registry.ExternalSourceRef) (registry.ExternalSourceRegistration, error) {
	c.registrations++
	if c.err != nil {
		return registry.ExternalSourceRegistration{}, c.err
	}
	normalized, identity, _, err := registry.ExternalSourceIdentity(ref.SourceURL)
	if err != nil {
		return registry.ExternalSourceRegistration{}, err
	}
	paperID, exists := c.byExternal[identity]
	if !exists {
		paperID = "qa_external_fixture_" + fmt.Sprint(len(c.byExternal))
		c.byExternal[identity] = paperID
		c.papers[paperID] = &registry.Paper{PaperID: paperID, PaperRef: identity, ExternalID: identity, Title: ref.Title, Status: "ready"}
		c.sources[paperID] = map[string]registry.PaperSource{}
	}
	for _, source := range c.sources[paperID] {
		if source.Sha256 == ref.Source.Sha256 {
			return registry.ExternalSourceRegistration{PaperID: paperID, Created: false, ExternalID: identity, Source: source}, nil
		}
	}
	src := ref.Source
	src.SourceID, src.PaperID, src.Origin, src.SourceURL, src.CreatedAt = registry.NewSourceID(), paperID, identity, normalized, time.Now().UTC()
	c.sources[paperID][src.SourceID] = src
	return registry.ExternalSourceRegistration{PaperID: paperID, Created: !exists, ExternalID: identity, Source: src}, nil
}

func newExternalFixtureCatalog(t *testing.T) (*externalFixtureCatalog, objstore.Store) {
	base, store := newFakeBlockCatalog(t)
	return &externalFixtureCatalog{fakeBlockCatalog: base, byExternal: map[string]string{}}, store
}
func callExternalRegistration(t *testing.T, catalog externalSourceCatalog, store objstore.Store, fetcher *externalSourceFetcher, raw string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	re := &core.RequestEvent{}
	re.Request = httptest.NewRequest(http.MethodPost, "/api/papers/source-register", strings.NewReader(raw))
	re.Response = httptest.NewRecorder()
	if err := paperExternalSourceRegisterHandler(re, store, catalog, fetcher); err != nil {
		t.Fatal(err)
	}
	rec := re.Response.(*httptest.ResponseRecorder)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return rec, body
}
func externalRequestJSON(source string) string {
	encoded, _ := json.Marshal(externalSourceRequest{SourceURL: source, Title: externalFixtureTitle, Authors: []string{"Fixture Author"}, Year: 2026})
	return string(encoded)
}

func TestExternalSourceRegisterRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	catalog, store := newExternalFixtureCatalog(t)
	pdf := externalFixturePDF(externalFixtureTitle, "Fixture Author", "Abstract", "Original version")
	fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	}))
	rec, body := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/original.pdf"))
	if rec.Code != 200 || body["created"] != true {
		t.Fatalf("registration=%d %v", rec.Code, body)
	}
	paperID := body["paper_id"].(string)
	source := body["source"].(map[string]any)
	sourceID := source["source_id"].(string)
	sum := sha256.Sum256(pdf)
	if source["sha256"] != hex.EncodeToString(sum[:]) || source["source_url"] != body["source_url"] || source["retrieved_at"] == "" || source["retrieved_url"] != body["source_url"] {
		t.Fatalf("provenance=%v", source)
	}
	read, _ := callBlockOriginals(t, catalog, store, source["pdf_endpoint"].(string), map[string]string{"Range": "bytes=0-31"})
	if read.Code != http.StatusPartialContent || !bytes.Equal(read.Body.Bytes(), pdf[:32]) {
		t.Fatalf("source read=%d %q", read.Code, read.Body.Bytes())
	}
	rec, retry := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/original.pdf"))
	if rec.Code != 200 || retry["created"] != false || retry["source"].(map[string]any)["source_id"] != sourceID {
		t.Fatalf("retry=%d %v", rec.Code, retry)
	}
	pdf = externalFixturePDF(externalFixtureTitle, "Fixture Author", "Abstract", "Revised bytes")
	rec, revised := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/original.pdf"))
	if rec.Code != 200 || revised["paper_id"] != paperID || revised["source"].(map[string]any)["source_id"] == sourceID || revised["source"].(map[string]any)["sha256"] == source["sha256"] {
		t.Fatalf("revision=%d %v", rec.Code, revised)
	}
	old, _ := callBlockOriginals(t, catalog, store, source["pdf_endpoint"].(string), nil)
	if old.Code != 200 || bytes.Equal(old.Body.Bytes(), pdf) {
		t.Fatal("old original overwritten")
	}
	rec, distinct := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/different.pdf"))
	if rec.Code != 200 || distinct["paper_id"] == paperID {
		t.Fatalf("same title differentURL merged: %v", distinct)
	}
}

func TestExternalSourceRegisterRejectsWrongPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	for _, lines := range [][]string{
		{"An Entirely Different Classical Research Paper", "Fixture Author", "Abstract", "References", externalFixtureTitle},
		{"An Entirely Different Classical Research Paper", "We cite " + externalFixtureTitle + " in this work."},
	} {
		catalog, store := newExternalFixtureCatalog(t)
		pdf := externalFixturePDF(lines...)
		fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write(pdf) }))
		rec, body := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/wrong.pdf"))
		if rec.Code != 422 || catalog.registrations != 0 {
			t.Fatalf("wrong original accepted: %d %v writes=%d", rec.Code, body, catalog.registrations)
		}
		objects, err := store.ListPrefix(t.Context(), "pdf/external-sources/", 0)
		if err != nil || len(objects) != 0 {
			t.Fatalf("unproven PDF stored: %+v %v", objects, err)
		}
	}
}

func TestExternalSourceRegisterValidationAndLimits(t *testing.T) {
	catalog, store := newExternalFixtureCatalog(t)
	fetches := 0
	fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fetches++
		_, _ = w.Write([]byte("%PDF-" + strings.Repeat("x", 256)))
	}))
	for _, raw := range []string{"{}", externalRequestJSON("http://papers.example.org/a.pdf"), externalRequestJSON("https://user:secret@papers.example.org/a.pdf"), externalRequestJSON("https://papers.example.org:443/a.pdf"), externalRequestJSON("https://papers.example.org/a.pdf#fragment"), externalRequestJSON("https://papers.example.org/a.pdf") + "{}", `{"source_url":"https://papers.example.org/a.pdf","title":"x","authors":["x"],"year":2026,"transport":"unsafe"}`} {
		rec, body := callExternalRegistration(t, catalog, store, fetcher, raw)
		if rec.Code != 400 {
			t.Fatalf("accepted invalid JSON: %d %v", rec.Code, body)
		}
	}
	if fetches != 0 || catalog.registrations != 0 {
		t.Fatal("validation allowed side effects")
	}
	fetcher.maxBytes = 32
	rec, _ := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/a.pdf"))
	if rec.Code != 422 || catalog.registrations != 0 {
		t.Fatal("oversized bytes accepted")
	}
	fetcher = externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte("<html>not a PDF</html>")) }))
	rec, _ = callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://papers.example.org/a.pdf"))
	if rec.Code != 422 {
		t.Fatal("HTML accepted")
	}
}

func TestExternalSourceRegisterChunkedAndTimeout(t *testing.T) {
	catalog, store := newExternalFixtureCatalog(t)
	fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.(http.Flusher).Flush() // no Content-Length: enforce the streamed limit too
		_, _ = w.Write([]byte("%PDF-" + strings.Repeat("x", 256)))
	}))
	fetcher.maxBytes = 32
	if rec, _ := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://fixture.example.org/chunked.pdf")); rec.Code != 422 || catalog.registrations != 0 {
		t.Fatal("chunked oversized body accepted")
	}
	fetcher = externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { <-req.Context().Done() }))
	fetcher.client.Timeout = 20 * time.Millisecond
	start := time.Now()
	if rec, _ := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://fixture.example.org/stalled.pdf")); rec.Code != 422 || time.Since(start) > time.Second || catalog.registrations != 0 {
		t.Fatal("stalled upstream did not fail boundedly")
	}
}

func TestExternalSourceRegisterImmutableCorruptionAndCatalogRetry(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	pdf := externalFixturePDF(externalFixtureTitle, "Fixture Author", "Abstract")
	sum := sha256.Sum256(pdf)
	key := "pdf/external-sources/" + hex.EncodeToString(sum[:]) + ".pdf"
	fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write(pdf) }))
	t.Run("corrupt existing object is never overwritten", func(t *testing.T) {
		catalog, store := newExternalFixtureCatalog(t)
		corrupt := []byte("corrupt bytes")
		if _, err := store.Put(t.Context(), key, bytes.NewReader(corrupt), int64(len(corrupt)), "application/pdf"); err != nil {
			t.Fatal(err)
		}
		rec, _ := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://fixture.example.org/corrupt.pdf"))
		if rec.Code != 500 || catalog.registrations != 0 {
			t.Fatal("corrupt hash object accepted")
		}
		rc, _, err := store.Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		stored, err := io.ReadAll(rc)
		if err != nil || !bytes.Equal(stored, corrupt) {
			t.Fatal("existing immutable object replaced")
		}
	})
	t.Run("catalog failure leaves verified blob for safe retry", func(t *testing.T) {
		catalog, store := newExternalFixtureCatalog(t)
		catalog.err = registry.ErrCatalogUnavailable
		rec, _ := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://fixture.example.org/retry.pdf"))
		if rec.Code != 503 {
			t.Fatalf("catalog failure=%d", rec.Code)
		}
		if _, exists, err := store.Stat(t.Context(), key); err != nil || !exists {
			t.Fatalf("verified retry blob absent: %v %v", exists, err)
		}
		catalog.err = nil
		rec, _ = callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://fixture.example.org/retry.pdf"))
		if rec.Code != 200 {
			t.Fatalf("retry failed=%d", rec.Code)
		}
	})
}

func TestExternalSourceRegisterEprintDirectPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	catalog, store := newExternalFixtureCatalog(t)
	pdf := externalFixturePDF(externalFixtureTitle, "Fixture Author", "Abstract")
	fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/2026/1591.pdf" {
			t.Errorf("eprint landing page not resolved to PDF: %s", req.URL.Path)
		}
		_, _ = w.Write(pdf)
	}))
	rec, body := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON("https://eprint.iacr.org/2026/1591"))
	if rec.Code != 200 || body["external_id"] != "eprint:2026/1591" || body["source"].(map[string]any)["retrieved_url"] != "https://eprint.iacr.org/2026/1591.pdf" {
		t.Fatalf("eprint registration=%d %v", rec.Code, body)
	}
}

func TestExternalSourcePinnedDialRejectsSSRF(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "172.16.2.3", "192.168.1.2", "169.254.169.254", "168.63.129.16", "100.100.100.200", "0.0.0.0", "224.0.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:7f00:1::", "2001:db8::1", "3fff::1"} {
		t.Run(address, func(t *testing.T) {
			dialCalls := 0
			dial := externalSourcePinnedDial(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(address)}, nil
			}, func(context.Context, string, string) (net.Conn, error) {
				dialCalls++
				return nil, errors.New("should not dial")
			})
			if _, err := dial(t.Context(), "tcp", "fixture.example.org:443"); err == nil || dialCalls != 0 {
				t.Fatalf("SSRF allowed: %v calls=%d", err, dialCalls)
			}
		})
	}
}

func TestExternalSourcePinnedDialPinsResolvedIP(t *testing.T) {
	lookups, calls := 0, 0
	peer, client := net.Pipe()
	defer peer.Close()
	defer client.Close()
	dial := externalSourcePinnedDial(func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(_ context.Context, network, address string) (net.Conn, error) {
		calls++
		if address != "93.184.216.34:443" {
			t.Fatalf("hostname re-resolved instead of pinned: %s", address)
		}
		return client, nil
	})
	if _, err := dial(t.Context(), "tcp", "rebind.example.org:443"); err != nil || lookups != 1 || calls != 1 {
		t.Fatalf("pin failed %v %d %d", err, lookups, calls)
	}
	mixed := externalSourcePinnedDial(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("mixed private/public answers dialed")
		return nil, nil
	})
	if _, err := mixed(t.Context(), "tcp", "mixed.example.org:443"); err == nil {
		t.Fatal("mixed answer accepted")
	}
}

func TestExternalSourceRedirectAndProductionLoopbackRejected(t *testing.T) {
	for _, target := range []string{"http://fixture.example.org/a.pdf", "https://fixture.example.org:444/a.pdf", "https://user:secret@fixture.example.org/a.pdf", "https://fixture.example.org/a.pdf#fragment", "https://fixture.example.org/a.pdf#"} {
		fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, target, http.StatusFound) }))
		if _, err := fetcher.fetch(t.Context(), "https://fixture.example.org/start.pdf"); err == nil {
			t.Fatalf("redirect accepted: %s", target)
		}
	}
	production := newExternalSourceFetcher()
	for _, target := range []string{"https://127.0.0.1/a.pdf", "https://[::1]/a.pdf", "https://169.254.169.254/latest/meta-data"} {
		if _, err := production.fetch(t.Context(), target); err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Fatalf("production private destination accepted: %s %v", target, err)
		}
	}
	// A redirect itself is valid HTTPS, but its *new connection* must still
	// pass the production IP check (not just the first URL).
	prodTransport := production.client.Transport
	production.client.Transport = externalRedirectToPrivateTransport{underlying: prodTransport}
	if _, err := production.fetch(t.Context(), "https://fixture.example.org/start.pdf"); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("redirect IP was not checked: %v", err)
	}
}

type externalRedirectToPrivateTransport struct{ underlying http.RoundTripper }

func (t externalRedirectToPrivateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == "fixture.example.org" {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1/a.pdf"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	return t.underlying.RoundTrip(req)
}

func TestExternalSourceRegisterScopeGuard(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	base, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	se := &core.ServeEvent{App: app, Router: base}
	var mux http.Handler
	if err := app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterPaperExternalSources(e, nil, nil, enforcer)
		var err error
		mux, err = e.Router.BuildMux()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h := &blockOriginalsHarness{t: t, app: app, mux: mux}
	for _, tc := range []struct {
		scopes []string
		want   int
	}{{nil, 403}, {[]string{"papers:read"}, 403}, {[]string{"papers:write"}, 503}} {
		patToken := h.mintPAT(t, tc.scopes)
		status, _, body := h.do(http.MethodPost, "/api/papers/source-register", map[string]string{"Authorization": "Bearer " + patToken})
		if status != tc.want {
			t.Fatalf("scopes=%v status=%d body=%v", tc.scopes, status, body)
		}
	}
	status, _, body := h.do(http.MethodPost, "/api/papers/source-register", nil)
	if status != 401 {
		t.Fatalf("anonymous=%d %v", status, body)
	}
}
