package objstore

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Router is a multi-bucket Store that dispatches by the leading "kind"
// segment of each key. v0.7.0 splits the single qatlas-raw bucket into
// per-kind buckets (qatlas-pdf / qatlas-md / qatlas-images); Router
// keeps the rest of the codebase keying objects as "<kind>/<shard>/..."
// (via paperassets.AssetKey) while transparently mapping each kind to
// its own backend with the "<kind>/" prefix stripped.
//
// Example: a handler writes key "pdf/9508/9508027.pdf". Router strips
// "pdf/", routes the remainder "9508/9508027.pdf" to the pdf backend
// (bucket qatlas-pdf). Listing re-prepends the kind so callers still
// see whole "pdf/..." keys.
//
// Unknown kinds (notably "json", which v0.7.0 drops) route to a nil
// backend: writes error, reads report not-found. This lets legacy
// resolve/list code that still probes json degrade to "absent" instead
// of crashing.
type Router struct {
	backends map[string]Store
}

// NewRouter builds a Router from a kind→Store map. Keys are the bare
// kind names ("pdf", "markdown", "images"). A nil or missing backend
// for a kind means that kind is unavailable (writes error, reads 404).
func NewRouter(backends map[string]Store) *Router {
	return &Router{backends: backends}
}

// split peels the leading "<kind>/" segment off key and returns the
// kind, the remainder, and the backend for that kind (nil if unknown).
func (r *Router) split(key string) (kind, rest string, backend Store) {
	i := strings.IndexByte(key, '/')
	if i < 0 {
		// No kind segment — treat the whole thing as kind with empty rest.
		return key, "", r.backends[key]
	}
	kind = key[:i]
	rest = key[i+1:]
	return kind, rest, r.backends[kind]
}

func (r *Router) Put(ctx context.Context, key string, rd io.Reader, size int64, contentType string) (int64, error) {
	kind, rest, b := r.split(key)
	if b == nil {
		return 0, unknownKind(kind)
	}
	return b.Put(ctx, rest, rd, size, contentType)
}

func (r *Router) PutWithMeta(ctx context.Context, key string, rd io.Reader, size int64, contentType string, metadata map[string]string) (int64, error) {
	kind, rest, b := r.split(key)
	if b == nil {
		return 0, unknownKind(kind)
	}
	return b.PutWithMeta(ctx, rest, rd, size, contentType, metadata)
}

func (r *Router) PutWithOptions(ctx context.Context, key string, rd io.Reader, size int64, opts PutOptions) (int64, error) {
	kind, rest, b := r.split(key)
	if b == nil {
		return 0, unknownKind(kind)
	}
	return b.PutWithOptions(ctx, rest, rd, size, opts)
}

func (r *Router) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	kind, rest, b := r.split(key)
	if b == nil {
		return nil, ObjectInfo{}, ErrNotFound
	}
	rc, info, err := b.Get(ctx, rest)
	if err == nil {
		info.Key = kind + "/" + info.Key
	}
	return rc, info, err
}

func (r *Router) Stat(ctx context.Context, key string) (ObjectInfo, bool, error) {
	kind, rest, b := r.split(key)
	if b == nil {
		return ObjectInfo{}, false, nil
	}
	info, exists, err := b.Stat(ctx, rest)
	if exists && err == nil {
		info.Key = kind + "/" + info.Key
	}
	return info, exists, err
}

func (r *Router) Delete(ctx context.Context, key string) error {
	kind, rest, b := r.split(key)
	if b == nil {
		return nil // deleting an unknown kind is a no-op (idempotent)
	}
	_ = kind
	return b.Delete(ctx, rest)
}

func (r *Router) ListPrefix(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	kind, rest, b := r.split(prefix)
	if b == nil {
		return nil, nil // unknown kind → empty listing
	}
	infos, err := b.ListPrefix(ctx, rest, limit)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		infos[i].Key = kind + "/" + infos[i].Key
	}
	return infos, nil
}

func (r *Router) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	_, rest, b := r.split(key)
	if b == nil {
		return "", false, ErrNotFound
	}
	return b.PresignGet(ctx, rest, ttl)
}

func unknownKind(kind string) error {
	return fmt.Errorf("objstore: no backend for kind %q (json is dropped in v0.7.0; pdf/markdown/images only)", kind)
}

// S3Backends returns every distinct *S3Store backing this Router, so
// boot-time maintenance (e.g. EnsureVersioning) can iterate per-bucket.
// Non-S3 backends (LocalStore in dev) are skipped. Order is
// unspecified.
func (r *Router) S3Backends() []*S3Store {
	out := make([]*S3Store, 0, len(r.backends))
	for _, b := range r.backends {
		if s3, ok := b.(*S3Store); ok {
			out = append(out, s3)
		}
	}
	return out
}
