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
//
// Metadata holds user-defined key/value pairs (S3 x-amz-meta-* headers
// without the prefix; lower-case keys). It is populated by Stat and
// Get on backends that support it (S3); LocalStore returns nil because
// it has no native sidecar metadata store. Callers MUST treat a nil
// or missing key the same way — "metadata unknown" rather than "known
// to be empty" — and fall back to the legacy path (e.g. force a re-PUT
// on upload conflicts).
//
// ETag is the backend-assigned strong identity for the current object
// version, as returned by the underlying store. S3Store fills it with
// minio-go's StatObject ETag (unquoted MD5-hex for single-part PUTs,
// composite for multipart). LocalStore leaves it empty — the local
// fallback has no native ETag and synthesising one (e.g. mtime+size)
// would lie about CAS semantics. Callers that need ETag for an
// If-Match CAS write MUST treat ETag == "" as "CAS unavailable on
// this backend" and either skip the precondition or refuse the write
// path that depends on it.
type ObjectInfo struct {
	Key         string
	Size        int64
	UpdatedAt   time.Time
	ContentType string
	ETag        string
	Metadata    map[string]string
}

// PutOptions bundles every per-write knob handlers need so we don't
// have to grow the Store interface every time another option shows up.
// Zero values mean "no opinion" — the backend uses its defaults.
//
// IfNoneMatch and IfMatch implement S3's conditional write semantics
// (RFC 7232 preconditions, as implemented by RustFS and AWS S3):
//
//   - IfNoneMatch: "*"      → write only if the key does NOT exist
//     (create-only / write-once). 412 PreconditionFailed when it
//     already exists.
//   - IfNoneMatch: "<etag>" → write only if current etag is NOT this
//     value. Rarely useful; included for symmetry.
//   - IfMatch:     "<etag>" → write only if current etag IS this value
//     (compare-and-swap). 412 PreconditionFailed when someone else
//     overwrote between the caller's Stat and Put.
//   - IfMatch:     "*"      → write only if the key DOES exist.
//
// Empty IfNoneMatch / IfMatch sends no precondition header — the Put
// is unconditional (legacy behaviour). Both fields can be set at once
// in principle, but real handlers should pick one path.
//
// On a precondition failure, PutWithOptions returns
// ErrPreconditionFailed (errors.Is-compatible), giving the caller a
// stable sentinel to branch on instead of poking at S3 error codes.
//
// LocalStore implements IfNoneMatch="*" via os.Link with EEXIST detection;
// every other precondition value returns ErrPreconditionUnsupported.
// LocalStore is dev-only and single-process, so CAS via IfMatch is moot
// there — the handler is expected to either accept last-write-wins
// (current LocalStore behaviour) or refuse to run the CAS path when
// the backend reports ETag == "".
type PutOptions struct {
	ContentType string
	Metadata    map[string]string

	IfNoneMatch string
	IfMatch     string
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

	// PutWithMeta is Put plus user-defined metadata. On S3 each map
	// entry becomes an x-amz-meta-<k> header (S3 lowercases keys on
	// the wire — pass lowercase to avoid round-trip surprises). On
	// LocalStore metadata is silently dropped: the dev-only local
	// fallback has no native sidecar store and surfacing a "metadata
	// unsupported" error would force every caller to special-case it.
	// Callers that depend on metadata for correctness (e.g. content
	// dedup via sha256) must already tolerate missing metadata on
	// reads — see ObjectInfo.Metadata.
	//
	// Behaves identically to Put when metadata is nil or empty.
	PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, contentType string, metadata map[string]string) (int64, error)

	// PutWithOptions is the general-purpose write that bundles every
	// per-write option (content type, metadata, conditional headers).
	// Put and PutWithMeta are kept as thin wrappers so legacy callers
	// don't have to change, but new code that needs conditional writes
	// (If-None-Match, If-Match) must use this method.
	//
	// On a conditional-write failure (S3 412 PreconditionFailed)
	// returns ErrPreconditionFailed. On a backend that cannot honour
	// the requested precondition (e.g. LocalStore + IfMatch) returns
	// ErrPreconditionUnsupported. Both sentinels are errors.Is-
	// compatible so callers can branch without poking at S3 wire codes.
	PutWithOptions(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (int64, error)

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

// ErrPreconditionFailed is returned by PutWithOptions when an
// If-None-Match / If-Match precondition was rejected by the backend
// (HTTP 412 on S3, EEXIST on LocalStore). The caller is expected to
// re-Stat the key and decide whether to short-circuit (sha matched
// after all), surface a 409 to the client, or retry once.
var ErrPreconditionFailed = errors.New("objstore: precondition failed")

// ErrPreconditionUnsupported is returned by PutWithOptions when the
// backend cannot honour the requested precondition (e.g. LocalStore
// asked for IfMatch). Distinct sentinel so callers can fall back to
// the unconditional write path without confusing a real precondition
// rejection (ErrPreconditionFailed) with "backend doesn't speak this".
var ErrPreconditionUnsupported = errors.New("objstore: precondition unsupported by backend")

// IsNotFound reports whether err is or wraps ErrNotFound. Wraps
// errors.Is for callsite readability.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsPreconditionFailed reports whether err is or wraps
// ErrPreconditionFailed.
func IsPreconditionFailed(err error) bool {
	return errors.Is(err, ErrPreconditionFailed)
}

// IsPreconditionUnsupported reports whether err is or wraps
// ErrPreconditionUnsupported.
func IsPreconditionUnsupported(err error) bool {
	return errors.Is(err, ErrPreconditionUnsupported)
}
