package web

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const releaseBase = "https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases/download"

// Resolve prefers built-in UI, then a verified version-specific cache, then an
// HTTPS download from the exact GitHub Release. No caller configuration or
// resource selection is required. Called only for serve, never --version,
// config init, service installation or other non-UI commands.
func Resolve(ctx context.Context, version string) (fs.FS, error) {
	tree, err := embeddedFS()
	if err != nil {
		return nil, err
	}
	if tree != nil {
		if err := Validate(tree); err != nil {
			return nil, fmt.Errorf("embedded UI: %w", err)
		}
		return tree, nil
	}
	canonical, err := ReleaseVersion(version)
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locate UI cache: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second}
	defer client.CloseIdleConnections()
	return (resolver{cache: filepath.Join(cache, "qatlas", "ui"), base: releaseBase, client: client}).resolve(ctx, canonical)
}

// Test seams are private: production cannot be pointed at a different release
// repo or disable TLS through environment variables or application config.
type resolver struct {
	cache, base string
	client      *http.Client
}

func (r resolver) resolve(ctx context.Context, version string) (fs.FS, error) {
	version, err := ReleaseVersion(version)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir := filepath.Join(r.cache, "v"+version)
	if tree, err := readCache(dir, version); err == nil {
		return tree, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(r.cache, 0700); err != nil {
		return nil, fmt.Errorf("create UI cache: %w", err)
	}
	lock := flock.New(dir + ".lock")
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("lock UI cache: %w", err)
	}
	if !locked {
		return nil, errors.New("timed out waiting for UI cache lock")
	}
	// Another process may have completed this version while we waited.
	if tree, err := readCache(dir, version); err == nil {
		return tree, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	asset := BundleName(version)
	checksums, err := r.download(ctx, version, checksumName(version), 1<<20)
	if err != nil {
		return nil, err
	}
	digest, err := checksumFor(checksums, asset)
	if err != nil {
		return nil, fmt.Errorf("verify UI %s: %w", version, err)
	}
	data, err := r.download(ctx, version, asset, maxBundleSize)
	if err != nil {
		return nil, err
	}
	tree, err := openBundle(data, digest, version)
	if err != nil {
		return nil, fmt.Errorf("verify UI %s: %w", version, err)
	}
	// Publish a complete cache directory atomically on the same filesystem.
	// Archive + trusted-at-download checksum are never observed half-written.
	tmp, err := os.MkdirTemp(r.cache, ".ui-")
	if err != nil {
		return nil, fmt.Errorf("stage UI cache: %w", err)
	}
	defer os.RemoveAll(tmp)
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"bundle.zip", data}, {"sha256", []byte(hex.EncodeToString(digest))},
	} {
		if err := writeCacheFile(filepath.Join(tmp, file.name), file.data); err != nil {
			return nil, fmt.Errorf("write UI cache: %w", err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, fmt.Errorf("publish UI cache: %w", err)
	}
	return tree, nil
}

func writeCacheFile(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func readLimitedFile(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("not a regular file within the size limit")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

func readCache(dir, version string) (fs.FS, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	bad := func(err error) (fs.FS, error) {
		// Do not wrap ErrNotExist here: an existing incomplete/corrupt cache
		// must fail closed, not be silently ignored and replaced.
		return nil, fmt.Errorf("invalid UI cache %s: %v; remove this version's cache directory and retry", dir, err)
	}
	if err != nil {
		return bad(err)
	}
	if !info.IsDir() {
		return bad(errors.New("not a cache directory"))
	}
	raw, err := readLimitedFile(filepath.Join(dir, "sha256"), 64)
	if err != nil {
		return bad(err)
	}
	digest, err := hex.DecodeString(string(raw))
	if err != nil || len(digest) != 32 {
		return bad(errors.New("invalid cached checksum"))
	}
	data, err := readLimitedFile(filepath.Join(dir, "bundle.zip"), maxBundleSize)
	if err != nil {
		return bad(err)
	}
	tree, err := openBundle(data, digest, version)
	if err != nil {
		return bad(err)
	}
	return tree, nil
}

func (r resolver) download(ctx context.Context, version, asset string, limit int64) ([]byte, error) {
	target := strings.TrimRight(r.base, "/") + "/v" + version + "/" + asset
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("UI download requires HTTPS")
	}
	client := *r.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("unsafe UI download redirect")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "qatlasd/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download UI %s (%s): %w", version, asset, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download UI %s (%s): HTTP %d; the exact release and its UI assets must be published (no latest fallback)", version, asset, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("UI download exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read UI download: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("UI download exceeds size limit")
	}
	return data, nil
}
