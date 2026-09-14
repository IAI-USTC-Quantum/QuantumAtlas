package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureResolver(t *testing.T, handler http.HandlerFunc) (resolver, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Method != "GET" || req.Header.Get("Authorization") != "" {
			t.Error("unexpected download method/auth")
		}
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	transport := client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != server.Listener.Addr().String() {
			return nil, errors.New("fixture blocked non-loopback-server dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return resolver{cache: t.TempDir(), base: server.URL, client: client}, &calls
}

func fixtureRelease(t *testing.T, versions ...string) http.HandlerFunc {
	t.Helper()
	files := map[string][]byte{}
	for _, version := range versions {
		data, digest := fixtureBundle(t, version)
		files["/v"+version+"/"+BundleName(version)] = data
		files["/v"+version+"/"+checksumName(version)] = []byte(fmt.Sprintf("%x  %s\n", digest, BundleName(version)))
	}
	return func(w http.ResponseWriter, req *http.Request) {
		data, ok := files[req.URL.Path]
		if !ok {
			t.Errorf("unexpected unpinned URL: %s", req.URL.Path)
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(data)
	}
}

func assertFixtureUI(t *testing.T, tree fs.FS, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	for name, file := range fixtureTree() {
		got, err := fs.ReadFile(tree, name)
		if err != nil || string(got) != string(file.Data) {
			t.Fatalf("resource mismatch: %s: %v", name, err)
		}
	}
}

func TestResolveDownloadCacheUpgradeAndOffline(t *testing.T) {
	r, calls := fixtureResolver(t, fixtureRelease(t, "0.35.0", "0.36.0-rc.1"))
	tree, err := r.resolve(context.Background(), "0.35.0")
	assertFixtureUI(t, tree, err)
	if calls.Load() != 2 {
		t.Fatal("expected one checksum and one bundle request")
	}
	tree, err = r.resolve(context.Background(), "0.35.0")
	assertFixtureUI(t, tree, err)
	if calls.Load() != 2 {
		t.Fatal("valid cache downloaded again")
	}
	tree, err = r.resolve(context.Background(), "0.36.0-rc.1")
	assertFixtureUI(t, tree, err)
	if calls.Load() != 4 {
		t.Fatal("upgrade failed to fetch new version")
	}
	// Cached bytes remain usable with no network at all, including the old
	// version after upgrade. Every cache read still checks SHA256 + ZIP CRC.
	r.base = "https://never-contact.invalid"
	for _, version := range []string{"0.35.0", "0.36.0-rc.1"} {
		tree, err = r.resolve(context.Background(), version)
		assertFixtureUI(t, tree, err)
	}
	if calls.Load() != 4 {
		t.Fatal("offline cache made a request")
	}
}

func TestResolveConcurrentFirstStart(t *testing.T) {
	r, calls := fixtureResolver(t, fixtureRelease(t, "0.35.0"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := r.resolve(context.Background(), "0.35.0")
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("concurrent starts made %d requests, want 2", calls.Load())
	}
}

func TestResolveCorruptCacheFailsClosed(t *testing.T) {
	for _, damage := range []string{"archive", "checksum", "missing", "symlink"} {
		t.Run(damage, func(t *testing.T) {
			r, calls := fixtureResolver(t, fixtureRelease(t, "0.35.0"))
			if _, err := r.resolve(context.Background(), "0.35.0"); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(r.cache, "v0.35.0")
			var err error
			switch damage {
			case "archive":
				err = os.WriteFile(filepath.Join(dir, "bundle.zip"), []byte("damaged"), 0600)
			case "checksum":
				err = os.WriteFile(filepath.Join(dir, "sha256"), []byte(strings.Repeat("0", 64)), 0600)
			case "missing":
				err = os.Remove(filepath.Join(dir, "sha256"))
			case "symlink":
				if err := os.Remove(filepath.Join(dir, "sha256")); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink("bundle.zip", filepath.Join(dir, "sha256"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.resolve(context.Background(), "0.35.0"); err == nil || !strings.Contains(err.Error(), "invalid UI cache") {
				t.Fatalf("corrupt cache accepted: %v", err)
			}
			if calls.Load() != 2 {
				t.Fatal("silently replaced corrupt cache over network")
			}
		})
	}
}

func TestResolveDownloadFailuresLeaveNoCache(t *testing.T) {
	data, digest := fixtureBundle(t, "0.35.0")
	checksum := fmt.Sprintf("%x  %s\n", digest, BundleName("0.35.0"))
	for _, kind := range []string{"404", "500", "hash", "missing_checksum", "duplicate_checksum", "truncated", "oversized", "downgrade", "version"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := fixtureResolver(t, func(w http.ResponseWriter, req *http.Request) {
				if kind == "404" || kind == "500" {
					w.WriteHeader(map[string]int{"404": 404, "500": 500}[kind])
					return
				}
				if kind == "downgrade" {
					w.Header().Set("Location", "http://never-contact.invalid/ui.zip")
					w.WriteHeader(302)
					return
				}
				if strings.HasSuffix(req.URL.Path, "_checksums.txt") {
					value := checksum
					switch kind {
					case "hash":
						value = strings.Repeat("0", 64) + "  " + BundleName("0.35.0")
					case "missing_checksum":
						value = ""
					case "duplicate_checksum":
						value += value
					case "version":
						_, otherDigest := fixtureBundle(t, "0.36.0")
						value = fmt.Sprintf("%x  %s\n", otherDigest, BundleName("0.35.0"))
					}
					fmt.Fprint(w, value)
					return
				}
				if kind == "oversized" {
					w.Header().Set("Content-Length", fmt.Sprint(maxBundleSize+1))
					return
				}
				if kind == "truncated" {
					w.Header().Set("Content-Length", fmt.Sprint(len(data)+10))
				}
				if kind == "version" {
					other, _ := fixtureBundle(t, "0.36.0")
					w.Write(other)
					return
				}
				w.Write(data)
			})
			if _, err := r.resolve(context.Background(), "0.35.0"); err == nil {
				t.Fatal("download failure was ignored")
			}
			if _, err := os.Stat(filepath.Join(r.cache, "v0.35.0")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("failure published cache")
			}
			entries, err := os.ReadDir(r.cache)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".ui-") {
					t.Fatal("partial stage leaked")
				}
			}
		})
	}
}

func TestResolveBoundedRequestAndLock(t *testing.T) {
	r, _ := fixtureResolver(t, func(w http.ResponseWriter, req *http.Request) { <-req.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.resolve(ctx, "0.35.0"); err == nil {
		t.Fatal("timeout ignored")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("unbounded request")
	}
}

func TestResolveInvalidVersionNeverDownloads(t *testing.T) {
	r, calls := fixtureResolver(t, func(w http.ResponseWriter, req *http.Request) { t.Error("unexpected network request") })
	for _, value := range []string{"dev", "latest", "../escape", "0.0.0-20260401000000-abcdefabcdef"} {
		if _, err := r.resolve(context.Background(), value); err == nil {
			t.Fatal("invalid version accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid version made requests")
	}
}
