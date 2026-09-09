package downloadworker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

type ipResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type publicTransport struct{ base *http.Transport }

func (t *publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !publicHTTPURL(r.URL) {
		return nil, errors.New("download URL must use HTTP(S) without credentials")
	}
	return t.base.RoundTrip(r)
}
func (t *publicTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
func publicHTTPURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

// In addition to Go's private/loopback/link-local predicates, reject reserved,
// documentation, benchmarking and IPv4-translation ranges. This is a defense
// for ordinary PDF/landing HTTP only, NOT a replacement for browser egress ACLs.
var nonPublicRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range nonPublicRanges {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func publicDial(resolver ipResolver, dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid download destination")
		}
		var ips []netip.Addr
		if ip, e := netip.ParseAddr(host); e == nil {
			ips = []netip.Addr{ip}
		} else {
			ips, err = resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, errors.New("download DNS resolution failed")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("download DNS returned no addresses")
		}
		// Reject the whole answer if it mixes public and private destinations.
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("non-public download destination blocked")
			}
		}
		for _, ip := range ips {
			// Dial the vetted numeric address, never the hostname: no DNS rebind window.
			conn, e := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if e == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, errors.New("public download connection failed")
	}
}

// NewPublicHTTPClient protects ordinary downloader/arXiv fetches. The master
// client is separate: private masters remain valid. HTTP proxies are disabled
// here because a proxy could perform unvalidated destination resolution.
func NewPublicHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = publicDial(net.DefaultResolver, dialer.DialContext)
	return &http.Client{Timeout: 60 * time.Second, Transport: &publicTransport{base: transport}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many download redirects")
		}
		if !publicHTTPURL(req.URL) {
			return errors.New("unsafe download redirect")
		}
		return nil
	}}
}
