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
	"net/netip"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

const externalSourceMaxBytes int64 = 64 << 20
const externalSourceTimeout = 45 * time.Second

// This capability is intentionally independent of paperCatalog/blockCatalog:
// adding source registration must not enlarge every existing route fake.
type externalSourceCatalog interface {
	RegisterExternalSource(context.Context, registry.ExternalSourceRef) (registry.ExternalSourceRegistration, error)
}

var _ externalSourceCatalog = (*registry.Store)(nil)

// RegisterPaperExternalSources is the single RegisterPapers wiring hook. No
// operator setting can substitute a transport or relax the production policy.
func RegisterPaperExternalSources(se *core.ServeEvent, store objstore.Store, catalog any, enforcer *casbin.Enforcer) {
	capability, _ := catalog.(externalSourceCatalog)
	fetcher := newExternalSourceFetcher()
	// Bound memory-heavy PDF downloads/extractors for this process. Reject excess
	// work before acquisition rather than retaining an unbounded request queue.
	limited := externalSourceLimit(make(chan struct{}, 2), func(re *core.RequestEvent) error {
		return paperExternalSourceRegisterHandler(re, store, capability, fetcher)
	})
	se.Router.POST("/api/papers/source-register", scopeGuard(enforcer, "papers", "write", limited))
}

func externalSourceLimit(slots chan struct{}, handler func(*core.RequestEvent) error) func(*core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			return handler(re)
		default:
			re.Response.Header().Set("Retry-After", "5")
			return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "external source registration busy; retry later"})
		}
	}
}

type externalSourceRequest struct {
	SourceURL string   `json:"source_url" binding:"required"`
	Title     string   `json:"title" binding:"required"`
	Authors   []string `json:"authors" binding:"required"`
	Year      int      `json:"year" binding:"required" minimum:"1" maximum:"9999"`
}

// externalSourceFetcher has a private fixture seam; ONLY tests construct one
// with an explicit custom transport. Production always uses the pinned dialer.
type externalSourceFetcher struct {
	client   *http.Client
	maxBytes int64
}

func newExternalSourceFetcher() *externalSourceFetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy:                  nil, // Never inherit an environment proxy that bypasses IP checks.
		DialContext:            externalSourcePinnedDial(net.DefaultResolver.LookupNetIP, dialer.DialContext),
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     true,
	}
	return &externalSourceFetcher{client: &http.Client{
		Transport: transport, Timeout: externalSourceTimeout, CheckRedirect: externalSourceCheckRedirect,
	}, maxBytes: externalSourceMaxBytes}
}

func externalSourceCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("external source: too many redirects")
	}
	// URL.String drops an empty trailing '#'; inspect the original Location
	// too so fragment syntax cannot be normalized away before validation.
	if req.Response != nil && strings.Contains(req.Response.Header.Get("Location"), "#") {
		return errors.New("external source: redirect fragments are not allowed")
	}
	_, err := registry.NormalizeExternalSourceURL(req.URL.String())
	return err
}

// Reject non-public routes, including cloud metadata, CGNAT, benchmark and
// documentation ranges. IPv4-mapped IPv6 is checked after unmapping. IPv6
// transition/NAT64 ranges are blocked so their embedded IPv4 cannot bypass it.
var externalSourceBlockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
}

func externalSourcePublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range externalSourceBlockedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	// Currently routable global IPv6 is 2000::/3, excluding ranges above.
	return ip.Is4() || netip.MustParsePrefix("2000::/3").Contains(ip)
}

// Resolve once per connection, validate ALL answers, then dial a numeric
// address from that exact set. net.Dialer never receives the hostname, closing
// the preflight-check/DNS-rebinding gap. TLS still verifies the original host.
func externalSourcePinnedDial(
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("external source: only HTTPS port 443 is allowed")
		}
		var ips []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			ips = []netip.Addr{ip}
		} else {
			ips, err = lookup(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("external source: DNS lookup failed: %w", err)
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("external source: DNS returned no addresses")
		}
		for _, ip := range ips {
			if !externalSourcePublicIP(ip) {
				return nil, errors.New("external source: destination is not a public address")
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	}
}

func (f *externalSourceFetcher) fetch(ctx context.Context, sourceURL string) (*downloader.FetchResult, error) {
	fetchURL, err := registry.ExternalSourcePDFURL(sourceURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/pdf")
	req.Header.Set("User-Agent", "QuantumAtlas external-source-registration")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("external source: upstream status %d", resp.StatusCode)
	}
	if resp.ContentLength > f.maxBytes {
		return nil, errors.New("external source: PDF exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > f.maxBytes {
		return nil, errors.New("external source: PDF exceeds size limit")
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, errors.New("external source: response is not a PDF")
	}
	sum := sha256.Sum256(data)
	return &downloader.FetchResult{Body: bytes.NewReader(data), Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:]), URL: resp.Request.URL.String(), Attempts: 1}, nil
}

func paperExternalSourceRegisterHandler(re *core.RequestEvent, store objstore.Store, catalog externalSourceCatalog, fetcher *externalSourceFetcher) error {
	fail := func(status int, detail string) error { return re.JSON(status, map[string]string{"detail": detail}) }
	if catalog == nil || store == nil {
		return fail(http.StatusServiceUnavailable, "external source registration requires catalog and object store")
	}
	if configured, ok := catalog.(interface{ Configured() bool }); ok && !configured.Configured() {
		return fail(http.StatusServiceUnavailable, "catalog unavailable; retry shortly")
	}
	var request externalSourceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(re.Response, re.Request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fail(http.StatusBadRequest, "expected JSON {source_url,title,authors,year}")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fail(http.StatusBadRequest, "request must contain exactly one JSON object")
	}
	request.Title = strings.TrimSpace(request.Title)
	if request.Title == "" || len(request.Title) > 2000 || request.Year < 1 || request.Year > 9999 || len(request.Authors) == 0 || len(request.Authors) > 100 {
		return fail(http.StatusBadRequest, "title, nonempty authors and year (1..9999) are required")
	}
	for i, author := range request.Authors {
		request.Authors[i] = strings.TrimSpace(author)
		if request.Authors[i] == "" || len(author) > 500 {
			return fail(http.StatusBadRequest, "authors must be nonempty strings of at most 500 bytes")
		}
	}
	normalized, _, _, err := registry.ExternalSourceIdentity(request.SourceURL)
	if err != nil {
		return fail(http.StatusBadRequest, err.Error())
	}
	ctx, cancel := context.WithTimeout(re.Request.Context(), externalSourceTimeout)
	defer cancel()
	result, err := fetcher.fetch(ctx, normalized)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, "source acquisition rejected: "+err.Error())
	}
	if err := downloader.VerifyPublishedPDF(ctx, result, downloader.PublishedIdentity{Title: request.Title}, downloader.PDFProvenanceConfig{}); err != nil {
		return fail(http.StatusUnprocessableEntity, "source title/provenance rejected: "+err.Error())
	}
	if closer, ok := result.Body.(io.Closer); ok {
		defer closer.Close()
	}
	key := "pdf/external-sources/" + result.Sha256 + ".pdf"
	n, err := store.PutWithOptions(ctx, key, result.Body, result.Size, objstore.PutOptions{
		ContentType: "application/pdf", IfNoneMatch: "*", Metadata: map[string]string{"sha256": result.Sha256},
	})
	if errors.Is(err, objstore.ErrPreconditionFailed) {
		// Never overwrite a content-addressed object, even if corrupt. Verify
		// existing bytes before accepting an idempotent race/retry.
		var rc io.ReadCloser
		rc, _, err = store.Get(ctx, key)
		if err == nil {
			h := sha256.New()
			n, err = io.Copy(h, io.LimitReader(rc, result.Size+1))
			_ = rc.Close()
			if err == nil && (n != result.Size || hex.EncodeToString(h.Sum(nil)) != result.Sha256) {
				err = errors.New("existing immutable PDF failed hash verification")
			}
		}
	}
	if err != nil || n != result.Size {
		return fail(http.StatusInternalServerError, "failed to persist immutable source PDF")
	}
	registered, err := catalog.RegisterExternalSource(ctx, registry.ExternalSourceRef{
		SourceURL: normalized, Title: request.Title, Authors: request.Authors, Year: request.Year,
		Source: registry.PaperSource{Sha256: result.Sha256, ObjstoreKey: key, SizeBytes: result.Size, RetrievedURL: result.URL, RetrievedAt: time.Now().UTC()},
	})
	if err != nil {
		// The verified hash object may remain as an unreferenced retryable blob;
		// deleting it could remove a concurrent successful registration's bytes.
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return fail(http.StatusServiceUnavailable, "catalog unavailable; retry registration")
		}
		return fail(http.StatusInternalServerError, "external source registration failed")
	}
	src := registered.Source
	return re.JSON(http.StatusOK, map[string]any{
		"paper_id": registered.PaperID, "created": registered.Created, "external_id": registered.ExternalID, "source_url": normalized,
		"source": map[string]any{
			"source_id": src.SourceID, "origin": src.Origin, "sha256": src.Sha256, "size_bytes": src.SizeBytes,
			"created_at": src.CreatedAt.UTC().Format(time.RFC3339), "source_url": src.SourceURL,
			"retrieved_url": src.RetrievedURL, "retrieved_at": src.RetrievedAt.UTC().Format(time.RFC3339),
			"pdf_endpoint": "/api/papers/" + registered.PaperID + "/sources/" + src.SourceID + "/pdf",
		},
	})
}
