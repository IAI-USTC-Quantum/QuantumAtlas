package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalStore is the filesystem-backed Store implementation. Keys are
// translated to "<BaseDir>/<key>" with both Unix forward-slashes and the
// platform separator handled correctly.
//
// PresignGet on a LocalStore always returns supported=false — there's
// nothing to presign, so the caller must serve bytes itself.
type LocalStore struct {
	BaseDir string
}

// NewLocalStore returns a LocalStore rooted at baseDir, ensuring the
// directory exists. baseDir must be absolute; the constructor refuses
// relative paths so a downstream MkdirAll can't accidentally land in
// the current working directory.
func NewLocalStore(baseDir string) (*LocalStore, error) {
	if baseDir == "" {
		return nil, errors.New("objstore: LocalStore baseDir required")
	}
	if !filepath.IsAbs(baseDir) {
		return nil, fmt.Errorf("objstore: LocalStore baseDir must be absolute, got %q", baseDir)
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("objstore: mkdir %s: %w", baseDir, err)
	}
	return &LocalStore{BaseDir: baseDir}, nil
}

// resolve converts a forward-slash key to a platform-specific absolute
// path under BaseDir, rejecting traversal and absolute keys.
func (s *LocalStore) resolve(key string) (string, error) {
	if key == "" {
		return "", errors.New("objstore: key required")
	}
	if strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.Contains(key, "\\") {
		return "", fmt.Errorf("objstore: invalid key %q", key)
	}
	// filepath.FromSlash handles Windows separators correctly; on Unix
	// it's a no-op. We then re-validate that the final cleaned path
	// stays under BaseDir, guarding against e.g. embedded ".." that
	// slipped past the cheap prefix check.
	abs := filepath.Join(s.BaseDir, filepath.FromSlash(key))
	cleanRoot := filepath.Clean(s.BaseDir)
	cleanAbs := filepath.Clean(abs)
	rel, err := filepath.Rel(cleanRoot, cleanAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("objstore: key %q escapes base dir", key)
	}
	return cleanAbs, nil
}

// Put writes r to BaseDir/key via a `.part` sidecar + atomic rename.
// _contentType is ignored — the local fs has no first-class content
// type, callers downstream rely on the extension instead.
func (s *LocalStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	return s.PutWithMeta(ctx, key, r, size, contentType, nil)
}

// PutWithMeta delegates to Put and silently discards metadata. The
// local backend has no native sidecar metadata store (xattr is Linux-
// only and breaks on macOS/Windows without setup); writing a parallel
// `.meta.json` would invite races and bloat the dev workflow. Callers
// that need metadata for correctness (e.g. sha256 dedup) MUST tolerate
// nil ObjectInfo.Metadata on reads.
//
// We log once-per-process via sync.Once below so devs notice when
// they're running in a degraded mode, without spamming.
func (s *LocalStore) PutWithMeta(_ context.Context, key string, r io.Reader, _ int64, _ string, metadata map[string]string) (int64, error) {
	if len(metadata) > 0 {
		localMetadataDropOnce.Do(func() {
			slog.Warn("LocalStore drops user metadata; dedup features that rely on it are disabled. Switch to S3Store for full functionality.",
				"first_dropped_keys", maps.Keys(metadata))
		})
	}
	dest, err := s.resolve(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp := dest + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	// Defer order matters: the rename happens before the cleanup runs.
	// We only nuke the .part file if it still exists (i.e. rename failed).
	cleanup := func() { _ = os.Remove(tmp) }
	written, copyErr := io.Copy(out, r)
	if cerr := out.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		cleanup()
		return 0, copyErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		cleanup()
		return 0, err
	}
	return written, nil
}

// localMetadataDropOnce gates the one-time warn when PutWithMeta is
// called with non-empty metadata on a LocalStore. We do this here
// (file-scope) rather than inside the struct so multiple LocalStore
// instances in tests don't spam the log.
var localMetadataDropOnce sync.Once

// Get opens BaseDir/key for reading. Returns ErrNotFound if absent.
func (s *LocalStore) Get(_ context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	path, err := s.resolve(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ObjectInfo{}, err
	}
	return f, ObjectInfo{
		Key:       key,
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UTC(),
	}, nil
}

// Stat reports whether BaseDir/key exists and, when it does, its size
// and modtime. Distinguishes "absent" (exists=false, err=nil) from
// "lookup failed" (err non-nil).
func (s *LocalStore) Stat(_ context.Context, key string) (ObjectInfo, bool, error) {
	path, err := s.resolve(key)
	if err != nil {
		return ObjectInfo{}, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ObjectInfo{}, false, nil
		}
		return ObjectInfo{}, false, err
	}
	// Treat directories as "not an object" — the fs has dirs as
	// first-class entries, but the Store contract is object-only.
	// ListPrefix is the right way to discover children.
	if !info.Mode().IsRegular() {
		return ObjectInfo{}, false, nil
	}
	return ObjectInfo{
		Key:       key,
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UTC(),
	}, true, nil
}

// Delete removes BaseDir/key. Missing key = no error.
func (s *LocalStore) Delete(_ context.Context, key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ListPrefix walks BaseDir/<prefix> recursively, returning every
// regular file. Prefix may be empty (list everything) or a partial
// path; trailing slashes are ignored. limit=0 means no cap.
//
// The walk is rooted at the deepest directory that fully contains the
// prefix — when prefix is "pdf/24/2401", the walk starts at
// BaseDir/pdf/24/ and we filter children by the trailing "2401" stem.
// This avoids re-walking BaseDir on every prefix probe in
// ResolveAssets, which is hot path for the paper resources endpoint.
func (s *LocalStore) ListPrefix(_ context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	prefix = strings.TrimRight(prefix, "/")
	if prefix != "" {
		// Same traversal-rejection rules as resolve().
		if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") || strings.Contains(prefix, "\\") {
			return nil, fmt.Errorf("objstore: invalid prefix %q", prefix)
		}
	}

	walkRoot := s.BaseDir
	stemFilter := ""
	if prefix != "" {
		full := filepath.Join(s.BaseDir, filepath.FromSlash(prefix))
		if info, err := os.Stat(full); err == nil && info.IsDir() {
			walkRoot = full
		} else {
			// Either a file (return single match) or a partial filename
			// prefix inside the parent dir.
			parent := filepath.Dir(full)
			if info, err := os.Stat(parent); err != nil || !info.IsDir() {
				return nil, nil
			}
			walkRoot = parent
			stemFilter = filepath.Base(full)
		}
	}

	var out []ObjectInfo
	err := filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Skip unreadable subdirs but keep walking; matches the
			// S3 behaviour where ListObjects can't fail on a single
			// "missing" prefix.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if stemFilter != "" && !strings.HasPrefix(filepath.Base(path), stemFilter) {
			return nil
		}
		rel, err := filepath.Rel(s.BaseDir, path)
		if err != nil {
			return nil
		}
		out = append(out, ObjectInfo{
			Key:       filepath.ToSlash(rel),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC(),
		})
		if limit > 0 && len(out) >= limit {
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

// errStopWalk is a sentinel used to abort filepath.Walk from inside
// the visitor without surfacing an error to the caller.
var errStopWalk = errors.New("objstore: stop walk")

// PresignGet is unsupported on the local backend.
func (s *LocalStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, nil
}
