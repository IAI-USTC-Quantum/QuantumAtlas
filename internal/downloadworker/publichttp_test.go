package downloadworker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type fakeIPResolver struct {
	ips   []netip.Addr
	calls int
}

func (r *fakeIPResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.ips, nil
}
func TestPublicIPDeniesLocalPrivateAndReserved(t *testing.T) {
	for _, text := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "0.0.0.0", "100.64.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255", "::1", "::", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::a00:1", "2002:a00:1::"} {
		if publicIP(netip.MustParseAddr(text)) {
			t.Errorf("non-public address accepted: %s", text)
		}
	}
	for _, text := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(text)) {
			t.Errorf("public address rejected: %s", text)
		}
	}
}
func TestPublicDialPinsResolvedIPAndRejectsMixedDNS(t *testing.T) {
	resolver := &fakeIPResolver{ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	var dialed string
	dial := publicDial(resolver, func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = address
		return nil, errors.New("synthetic dial stop")
	})
	_, _ = dial(context.Background(), "tcp", "publisher.invalid:443")
	if dialed != "8.8.8.8:443" || resolver.calls != 1 {
		t.Fatalf("destination was not resolved once and pinned: %s calls=%d", dialed, resolver.calls)
	}
	resolver.ips = append(resolver.ips, netip.MustParseAddr("127.0.0.1"))
	dialed = ""
	if _, err := dial(context.Background(), "tcp", "publisher.invalid:443"); err == nil || dialed != "" {
		t.Fatal("mixed DNS was allowed to dial")
	}
	resolver.calls = 0
	if _, err := dial(context.Background(), "tcp", "[::ffff:127.0.0.1]:80"); err == nil || resolver.calls != 0 {
		t.Fatal("literal private destination escaped validation")
	}
}
func TestPublicHTTPBlocksPrivateRedirectAndProxyBypass(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9999")
	client := NewPublicHTTPClient()
	defer client.CloseIdleConnections()
	transport := client.Transport.(*publicTransport)
	if transport.base.Proxy != nil {
		t.Fatal("proxy could bypass destination validation")
	}
	resolver := &fakeIPResolver{ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	dials := 0
	transport.base.DialContext = publicDial(resolver, func(_ context.Context, _ string, address string) (net.Conn, error) {
		dials++
		if address != "8.8.8.8:80" {
			t.Errorf("unsafe dial %s", address)
		}
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			buf := make([]byte, 4096)
			_, _ = serverConn.Read(buf)
			_, _ = io.WriteString(serverConn, "HTTP/1.1 302 Found\r\nLocation: http://169.254.169.254/latest/meta-data/\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		}()
		return clientConn, nil
	})
	response, err := client.Get("http://publisher.invalid/pdf")
	if response != nil {
		response.Body.Close()
	}
	if err == nil || dials != 1 {
		t.Fatal("redirect reached private destination", err, dials)
	}
	for _, raw := range []string{"ftp://publisher.invalid/file", "http://name:credential@publisher.invalid/file"} {
		req, e := http.NewRequest(http.MethodGet, raw, nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = transport.RoundTrip(req); e == nil || strings.Contains(e.Error(), "credential@") {
			t.Fatal("unsafe URL admitted or leaked credentials", e)
		}
	}
}
