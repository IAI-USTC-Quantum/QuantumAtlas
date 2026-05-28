package objstore

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// jsonMarshal/jsonUnmarshal aliases keep the sidecar JSON wire format
// in one place — easy to swap to a compact / canonical encoder later.
var (
	jsonMarshal   = json.Marshal
	jsonUnmarshal = json.Unmarshal
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

// PutWithMeta delegates to PutWithOptions. LocalStore persists metadata
// as a sidecar JSON file (<key>.meta.json) alongside the object so the
// content-aware idempotency path (Stat → compare sha256 metadata) works
// identically to the S3 backend in dev. This keeps dev-vs-prod fidelity
// for the upload handler's race-safe flow at the cost of one extra small
// write per Put.
func (s *LocalStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, contentType string, metadata map[string]string) (int64, error) {
	return s.PutWithOptions(ctx, key, r, size, PutOptions{
		ContentType: contentType,
		Metadata:    metadata,
	})
}

// PutWithOptions writes to BaseDir/key. Supports a constrained subset
// of preconditions:
//
//   - IfNoneMatch == "*"  → atomic create-if-not-exists via os.Link
//     (POSIX guarantees EEXIST when the target already exists). Returns
//     ErrPreconditionFailed when the file is already there.
//   - IfMatch != "" or IfNoneMatch != "" (non-"*") → returns
//     ErrPreconditionUnsupported. LocalStore has no ETag and is dev-
//     only/single-process, so emulating CAS here would either lie about
//     concurrency safety or require a sidecar lock file. The handler is
//     expected to fall back to an unconditional write (matching legacy
//     behaviour) when it sees ErrPreconditionUnsupported.
//   - Empty preconditions → unconditional write, identical to old
//     PutWithMeta semantics (write via .part + rename).
//
// Metadata is persisted as a sidecar JSON file (<dest>.meta.json) only
// after the primary object is successfully in place — so a 412
// (precondition failed) leaves no orphan sidecars. The sidecar write
// uses the same .part + rename atomicity dance to avoid partial files.
func (s *LocalStore) PutWithOptions(_ context.Context, key string, r io.Reader, _ int64, po PutOptions) (int64, error) {
	if po.IfMatch != "" {
		return 0, fmt.Errorf("objstore: LocalStore IfMatch %q: %w", po.IfMatch, ErrPreconditionUnsupported)
	}
	if po.IfNoneMatch != "" && po.IfNoneMatch != "*" {
		return 0, fmt.Errorf("objstore: LocalStore IfNoneMatch %q: %w (only \"*\" is supported)", po.IfNoneMatch, ErrPreconditionUnsupported)
	}

	dest, err := s.resolve(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	// Per-call unique tmp suffix so two concurrent writers don't trample
	// each other's staged bytes under the same dest+".part" name. Random
	// hex is cheap and avoids needing a process-wide counter.
	var nonce [8]byte
	if _, err := crand.Read(nonce[:]); err != nil {
		return 0, fmt.Errorf("objstore: tmp nonce: %w", err)
	}
	tmp := fmt.Sprintf("%s.%s.part", dest, hex.EncodeToString(nonce[:]))
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	written, copyErr := io.Copy(out, r)
	if cerr := out.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		cleanup()
		return 0, copyErr
	}

	if po.IfNoneMatch == "*" {
		// Atomic create-if-not-exists: os.Link returns EEXIST when dest
		// already exists. On success we still own tmp and must remove it.
		if err := os.Link(tmp, dest); err != nil {
			cleanup()
			if errors.Is(err, os.ErrExist) {
				return 0, fmt.Errorf("objstore: put %s: %w", key, ErrPreconditionFailed)
			}
			return 0, err
		}
		cleanup()
		if err := s.writeSidecar(dest, po.Metadata); err != nil {
			return 0, fmt.Errorf("objstore: write sidecar for %s: %w", key, err)
		}
		return written, nil
	}

	// Unconditional path: rename overwrites any existing file atomically.
	if err := os.Rename(tmp, dest); err != nil {
		cleanup()
		return 0, err
	}
	if err := s.writeSidecar(dest, po.Metadata); err != nil {
		return 0, fmt.Errorf("objstore: write sidecar for %s: %w", key, err)
	}
	return written, nil
}

// writeSidecar atomically persists metadata as <dest>.meta.json. When
// metadata is empty/nil we remove any pre-existing sidecar so a Put
// without metadata behaves as "clear metadata", matching how an S3
// PutObject without x-amz-meta-* headers replaces the prior metadata.
func (s *LocalStore) writeSidecar(dest string, metadata map[string]string) error {
	sidecar := dest + sidecarExt
	if len(metadata) == 0 {
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	body, err := jsonMarshal(metadata)
	if err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := crand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%s.part", sidecar, hex.EncodeToString(nonce[:]))
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, sidecar); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readSidecar returns the metadata persisted alongside dest, or nil
// when no sidecar exists. Corrupt sidecars are treated as "no metadata"
// (with a one-shot warning) rather than failing reads — the object
// itself is still usable and we'd rather degrade to legacy semantics
// than 500 the caller.
func (s *LocalStore) readSidecar(dest string) map[string]string {
	body, err := os.ReadFile(dest + sidecarExt)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := jsonUnmarshal(body, &m); err != nil {
		localSidecarCorruptOnce.Do(func() {
			slog.Warn("LocalStore sidecar corrupt; ignoring metadata", "path", dest+sidecarExt, "err", err)
		})
		return nil
	}
	return m
}

// sidecarExt is the suffix LocalStore appends to per-object metadata
// sidecar files. It must not appear in any real storage key (see
// ListPrefix filter) — ".meta.json" satisfies that because our keys are
// driven by paperassets.AssetKey which uses ".pdf"/".json"/".md".
const sidecarExt = ".meta.json"

// localSidecarCorruptOnce throttles the warn for corrupt sidecars. A
// single bad upload shouldn't flood the log; we tolerate the situation
// by returning empty metadata (legacy behaviour).
var localSidecarCorruptOnce sync.Once

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
		Metadata:  s.readSidecar(path),
	}, nil
}

// Stat reports whether BaseDir/key exists and, when it does, its size,
// modtime, and any persisted sidecar metadata. Distinguishes "absent"
// (exists=false, err=nil) from "lookup failed" (err non-nil).
//
// Because the sidecar is published AFTER the primary file (see
// PutWithOptions), there is a microsecond-scale window where the data
// file is visible but its sidecar hasn't been renamed into place yet.
// When that happens we briefly poll for the sidecar to land before
// returning — bounded to a handful of millisecond-scale tries — so the
// upload handler's content-aware idempotency (Stat → compare sha256
// metadata) doesn't return false-positive 409s under concurrent same-
// content uploads. After the budget is exhausted we return whatever
// metadata is there (nil for legacy objects with no sidecar at all),
// matching the legacy semantics.
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
	meta := s.readSidecar(path)
	if meta == nil {
		// Close the publish-window race: see if the sidecar arrives
		// shortly. The total budget is ~25ms which is huge compared to
		// the typical ~10µs gap between os.Link(data) and os.Rename
		// (sidecar); legacy objects without sidecars at all will fully
		// exhaust this and still return nil — same as before.
		const tries = 5
		const sleep = 5 * time.Millisecond
		for i := 0; i < tries; i++ {
			time.Sleep(sleep)
			meta = s.readSidecar(path)
			if meta != nil {
				break
			}
		}
	}
	return ObjectInfo{
		Key:       key,
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UTC(),
		Metadata:  meta,
	}, true, nil
}

// Delete removes BaseDir/key and any sidecar metadata file. Missing
// key = no error; missing sidecar = no error.
func (s *LocalStore) Delete(_ context.Context, key string) error {
	path, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(path + sidecarExt); err != nil && !errors.Is(err, os.ErrNotExist) {
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
		// Don't surface our own sidecar metadata files as objects.
		if strings.HasSuffix(filepath.Base(path), sidecarExt) {
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
