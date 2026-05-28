// Package objstore is a minimal, backend-agnostic blob storage abstraction
// for QuantumAtlas raw paper assets (PDF / markdown / JSON / image dirs)
// and any future S3-backed metadata stores.
//
// The interface deliberately exposes the smallest surface needed by the
// HTTP route handlers; if a method would be useful in only one backend
// (e.g. local-fs symlinks, S3 bucket policies), it belongs in a
// backend-specific helper, not here.
//
// # Keys
//
// All keys are forward-slash-delimited, never start with "/" or contain
// ".." or "\\". The local backend maps each key to "<baseDir>/<key>";
// the S3 backend uses keys verbatim as object names. Callers are
// responsible for sanitising keys before passing them in — the store
// implementations refuse leading "/" or ".." to make traversal bugs
// fail-fast, but they do not lower-case, percent-encode, or otherwise
// "fix" keys.
//
// # Why this interface (vs raw os.* + minio.Client)
//
// The Go server straddles two storage backends: a local-fs RawDir for
// dev / first-boot, and a remote RustFS / S3 bucket for production.
// Without an interface, every route handler ends up branching on
// cfg.S3Enabled and the test surface multiplies. This package hides that
// branch behind a single Store value the handlers receive at boot.
package objstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ObjectInfo is a thin, uniform view of an object's metadata.
//
// Size may be -1 when the backend can't cheaply report it (e.g. range
// listings that didn't fetch the full Stat). Callers that need an
// authoritative size should Stat() the key explicitly.
type ObjectInfo struct {
	Key         string
	Size        int64
	UpdatedAt   time.Time
	ContentType string
}

// Store abstracts the backing store for raw paper assets. Implementations
// are safe for concurrent use by multiple goroutines; individual
// io.ReadClosers returned by Get are NOT — close one before opening the
// next on the same Store from the same goroutine.
type Store interface {
	// Put writes r to key. size is a hint: pass the exact byte count
	// when known so the S3 backend can use a single PutObject instead
	// of multi-part; pass -1 when streaming an unknown-length reader.
	// contentType is stored alongside the object on backends that
	// support it (S3); local backend ignores it (extension wins).
	// Returns the number of bytes actually written.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)

	// Get opens key for reading. The caller MUST close the reader.
	// Returns ErrNotFound when key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)

	// Stat returns metadata for key. The exists flag is false (with
	// nil err) when key does not exist; a non-nil err means the lookup
	// itself failed (network, permission, etc.) and the caller cannot
	// distinguish present-but-unreadable from absent.
	Stat(ctx context.Context, key string) (info ObjectInfo, exists bool, err error)

	// Delete removes key. Deleting a non-existent key is not an error
	// (idempotent, matches both POSIX unlink semantics under O_EXCL and
	// the S3 DeleteObject contract).
	Delete(ctx context.Context, key string) error

	// ListPrefix returns every object whose key starts with prefix.
	// The returned slice is NOT sorted by the backend; callers that
	// need deterministic order must sort it themselves. Limit is the
	// max objects to return (0 = no limit, but use sparingly — S3
	// pagination can be slow over WAN).
	//
	// Recursive: an S3 ListObjectsV2 without a delimiter and a local
	// filepath.Walk both surface every nested object.
	ListPrefix(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error)

	// PresignGet returns a short-lived public URL the client can hit
	// directly without re-authenticating against the server. When the
	// backend doesn't support presigning (local fs), returns
	// supported=false and the caller must fall back to streaming via
	// Get + io.Copy. Returning supported=false with err=nil is the
	// happy-path signal for "use the fallback".
	PresignGet(ctx context.Context, key string, ttl time.Duration) (url string, supported bool, err error)
}

// ErrNotFound is returned by Get when the key does not exist. Stat
// signals absence via its exists=false return — only Get uses this
// sentinel, because Get's contract returns a reader that the caller
// must close, and there's no useful "zero reader" we could return for
// the missing case.
var ErrNotFound = errors.New("objstore: not found")

// IsNotFound reports whether err is or wraps ErrNotFound. Wraps
// errors.Is for callsite readability.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
