package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// appInfoName / appInfoVersion are folded into the minio-go client
// User-Agent (e.g. "qatlasd/0.9.0/edge-a minio-go/v7…") so the
// RustFS audit trail can distinguish legitimate qatlasd writes
// from direct-to-bucket access (mc/…, boto3/…). Set once at process
// start via SetClientAppInfo, before any store is constructed; the
// values are read by buildMinioClient for every client it builds.
//
// UA is a *forgeable* signal — never use it for authz. It only makes the
// audit stream readable at a glance; the load-bearing forensic key is
// the SigV4 accessKey recorded by the RustFS-side audit trail (T10).
var (
	appInfoName    = "qatlasd"
	appInfoVersion = "dev"
)

// SetClientAppInfo overrides the User-Agent app name/version stamped on
// every subsequently-built minio-go client. Call it once at startup
// (before initRawStore) with version+edge so direct-bucket access is
// visually separable from server writes in the audit log. Empty
// arguments are ignored (keep the existing default).
func SetClientAppInfo(name, version string) {
	if name != "" {
		appInfoName = name
	}
	if version != "" {
		appInfoVersion = version
	}
}

// S3Store implements Store against any S3-compatible backend
// (Amazon S3, RustFS, MinIO). We use minio-go because:
//
//   - the binary is small (~3 MiB added to the server) vs aws-sdk-go-v2
//     (~30 MiB) — every MB matters on memory-tight edges (~1 GB class);
//   - RustFS and MinIO share the same wire dialect so the SDK keeps
//     server-specific quirks contained in one place;
//   - the API surface mirrors S3 verbs 1:1 so the Store glue stays thin.
//
// All methods are safe for concurrent use; minio.Client maintains its
// own connection pool internally.
//
// presignClient (optional) is a second minio.Client bound to the same
// bucket + credentials but a different endpoint host/scheme. It is
// used ONLY by PresignGet, so the public-facing URLs handed to
// end-user clients can point at a public ingress (e.g. an LE-fronted
// subdomain or a direct IP) while server↔RustFS traffic stays on the
// internal mesh endpoint. When nil, PresignGet falls back to client.
// See NewS3StoreDual for the wiring rationale.
type S3Store struct {
	client        *minio.Client
	presignClient *minio.Client
	bucket        string
}

// NewS3Store is the single-endpoint convenience constructor: it uses
// the same endpoint for both server↔RustFS traffic AND for signing
// presigned URLs handed to clients. Equivalent to
// NewS3StoreDual(endpoint, "", bucket, key, secret).
//
// Suitable for single-network deployments (everything on one LAN) and
// for the storage prune subcommand which never presigns. Production
// edge servers that split internal / public networks should call
// NewS3StoreDual directly.
func NewS3Store(endpoint, bucket, accessKeyID, secretAccessKey string) (*S3Store, error) {
	return NewS3StoreDual(endpoint, "", bucket, accessKeyID, secretAccessKey)
}

// NewS3StoreDual builds an S3Store with separate internal + public
// endpoints. internalEndpoint is used for all server-initiated S3
// operations (Put/Get/Stat/List/versioning/prune); publicEndpoint is
// used ONLY when computing presigned URLs via PresignGet.
//
// Both endpoints must include scheme (http:// or https://). The scheme
// picks TLS-or-not for the underlying minio-go client; we don't
// second-guess what the operator wrote. credentials and bucket are
// shared — the two endpoints must front the same RustFS / S3 backend.
//
// When publicEndpoint is "" or string-equal to internalEndpoint, the
// dual-client setup collapses to the single-client behaviour (no
// presignClient, PresignGet uses client). This keeps the zero-config
// path identical to NewS3Store.
//
// Wire model:
//
//	server ──internalEndpoint──► RustFS  (Put / Get / Stat / List)
//	browser ──publicEndpoint──► RustFS  (presigned GET only)
//
// SigV4 requires the Host header at request time to match the Host
// used to compute the signature. minio-go signs each presigned URL
// with the endpoint configured on the client that issues it; the
// downstream reverse proxy (Caddy on edge VPS) MUST preserve the
// inbound Host header when forwarding to the internal RustFS port,
// otherwise RustFS will reject with SignatureDoesNotMatch.
func NewS3StoreDual(internalEndpoint, publicEndpoint, bucket, accessKeyID, secretAccessKey string) (*S3Store, error) {
	if internalEndpoint == "" || bucket == "" || accessKeyID == "" || secretAccessKey == "" {
		return nil, errors.New("objstore: S3Store endpoint, bucket and credentials required")
	}
	client, err := buildMinioClient(internalEndpoint, accessKeyID, secretAccessKey)
	if err != nil {
		return nil, err
	}
	s := &S3Store{client: client, bucket: bucket}
	if publicEndpoint != "" && publicEndpoint != internalEndpoint {
		pub, err := buildMinioClient(publicEndpoint, accessKeyID, secretAccessKey)
		if err != nil {
			return nil, fmt.Errorf("objstore: build public-endpoint client: %w", err)
		}
		s.presignClient = pub
	}
	return s, nil
}

// buildMinioClient is the shared minio.New wrapper used by both the
// internal and the optional public-endpoint constructors. It centralises
// scheme parsing, host validation, and the fixed "us-east-1" region —
// see NewS3Store doc for why we hardcode the region.
func buildMinioClient(endpoint, accessKeyID, secretAccessKey string) (*minio.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("objstore: parse endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("objstore: endpoint scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Host
	if host == "" {
		return nil, fmt.Errorf("objstore: endpoint %q missing host", endpoint)
	}
	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: u.Scheme == "https",
		// Region is required by minio-go's signature calculation; the
		// actual value is only checked against the bucket on AWS S3
		// proper. RustFS / MinIO accept anything.
		Region: "us-east-1",
	})
	if err != nil {
		return nil, fmt.Errorf("objstore: minio.New: %w", err)
	}
	// Stamp the app name/version into the request User-Agent so the
	// RustFS audit trail visibly separates qatlasd writes from
	// direct mc/boto3 access. Forgeable — readability only, never authz.
	client.SetAppInfo(appInfoName, appInfoVersion)
	return client, nil
}

// validateKey enforces the same traversal rejection as LocalStore so the
// two backends fail the same way on malformed input.
func validateKey(key string) error {
	if key == "" {
		return errors.New("objstore: key required")
	}
	if strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.Contains(key, "\\") {
		return fmt.Errorf("objstore: invalid key %q", key)
	}
	return nil
}

// Ping verifies the configured bucket is reachable and accessible by
// the current credentials. Uses BucketExists which translates to a
// cheap HEAD-bucket call (no listing, no read of any object). Used by
// the /api/health endpoint to surface RustFS reachability without
// paying per-object I/O. The boolean signals "bucket exists"
// specifically — false with err=nil means credentials are valid but
// the bucket is missing (still an unhealthy state for callers
// expecting it).
func (s *S3Store) Ping(ctx context.Context) (exists bool, err error) {
	return s.client.BucketExists(ctx, s.bucket)
}

// Bucket returns the configured bucket name. Used by /api/health to
// include the bucket identity in the response without exposing
// credentials.
func (s *S3Store) Bucket() string { return s.bucket }

// EndpointURL returns the internal endpoint URL the client was built
// with (host[:port], no scheme). Used by /api/health for surfacing
// what the server is actually talking to. We reconstruct it from the
// minio.Client's EndpointURL() to avoid duplicating the field.
func (s *S3Store) EndpointURL() string {
	if s == nil || s.client == nil {
		return ""
	}
	u := s.client.EndpointURL()
	if u == nil {
		return ""
	}
	return u.String()
}

// Put streams r into the bucket at key. When size >= 0 minio-go uses a
// single PutObject; size < 0 triggers a multipart upload with default
// part size. contentType is stored in object metadata and surfaces in
// Content-Type on subsequent GETs / presigned URLs.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	return s.PutWithMeta(ctx, key, r, size, contentType, nil)
}

// PutWithMeta is Put plus user-defined metadata. Each entry becomes an
// x-amz-meta-<lowercase-k> header. S3 reserves the headers themselves
// as 2 KiB total per object; the upload-pdf handler uses ~80 bytes
// (sha256 hex), well clear of the cap.
//
// We accept arbitrary keys and trust the caller to keep them
// lowercase — minio-go does NOT auto-lower for us, and roundtripping
// CamelCase keys back via Stat returns lowercase, which has burned
// us once before.
func (s *S3Store) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, contentType string, metadata map[string]string) (int64, error) {
	return s.PutWithOptions(ctx, key, r, size, PutOptions{
		ContentType: contentType,
		Metadata:    metadata,
	})
}

// PutWithOptions is the conditional-write capable PUT. It honours
// PutOptions.IfNoneMatch and PutOptions.IfMatch via minio-go's
// SetMatchETagExcept / SetMatchETag setters, which translate to the
// S3 If-None-Match / If-Match request headers verbatim. RustFS
// enforces both ends of the spec (see e2e_test/src/reliant/
// conditional_writes.rs in the rustfs repo for the matrix it verifies),
// so:
//
//   - IfNoneMatch="*" + object absent → 200/201, write happens.
//   - IfNoneMatch="*" + object exists → 412, returns ErrPreconditionFailed,
//     S3 does NOT write.
//   - IfMatch="<etag>" + etag matches  → 200/201, write replaces object.
//   - IfMatch="<etag>" + etag stale    → 412, returns ErrPreconditionFailed.
//   - IfMatch="<etag>" + object absent → "NoSuchKey", also normalised to
//     ErrPreconditionFailed because for the caller the distinction
//     between "didn't exist" and "had a different etag" doesn't matter
//     — both mean "your CAS lost, re-Stat and decide".
//
// On AWS, minio-go also rewrites the request to send Content-MD5 for
// the conditional path. We don't compute one client-side — RustFS
// accepts the request without it.
func (s *S3Store) PutWithOptions(ctx context.Context, key string, r io.Reader, size int64, po PutOptions) (int64, error) {
	if err := validateKey(key); err != nil {
		return 0, err
	}
	opts := minio.PutObjectOptions{}
	if po.ContentType != "" {
		opts.ContentType = po.ContentType
	}
	if len(po.Metadata) > 0 {
		// minio-go mutates the map; copy so callers can reuse theirs.
		md := make(map[string]string, len(po.Metadata))
		for k, v := range po.Metadata {
			md[k] = v
		}
		opts.UserMetadata = md
	}
	if po.IfNoneMatch != "" {
		opts.SetMatchETagExcept(po.IfNoneMatch)
	}
	if po.IfMatch != "" {
		opts.SetMatchETag(po.IfMatch)
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, r, size, opts)
	if err != nil {
		if isPreconditionFailed(err) {
			return 0, fmt.Errorf("objstore: put %s: %w", key, ErrPreconditionFailed)
		}
		// IfMatch against a missing key is semantically a CAS loss
		// (someone deleted between our Stat and our Put). RustFS
		// surfaces this as NoSuchKey + 404, not 412 — normalise so
		// the caller only branches on ErrPreconditionFailed.
		if po.IfMatch != "" && isNoSuchKey(err) {
			return 0, fmt.Errorf("objstore: put %s: %w", key, ErrPreconditionFailed)
		}
		return 0, fmt.Errorf("objstore: put %s: %w", key, err)
	}
	return info.Size, nil
}

// Get opens key for reading. Returns ErrNotFound when key doesn't
// exist; minio-go returns this as an ErrorResponse with code
// "NoSuchKey", so we re-wrap so callers can use IsNotFound consistently
// across backends.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("objstore: get %s: %w", key, err)
	}
	// minio-go's GetObject is lazy — it doesn't HEAD until you Read or
	// Stat. Stat() here so we can return ErrNotFound up-front instead
	// of after the caller already wrapped the reader in io.Copy.
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if isNoSuchKey(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objstore: stat %s after get: %w", key, err)
	}
	return obj, ObjectInfo{
		Key:         key,
		Size:        stat.Size,
		UpdatedAt:   stat.LastModified.UTC(),
		ContentType: stat.ContentType,
		ETag:        normaliseETag(stat.ETag),
		Metadata:    copyUserMeta(stat.UserMetadata),
	}, nil
}

// Stat does a HEAD against the object. Distinguishes absent (exists=
// false, err=nil) from "lookup failed" (err non-nil).
func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, bool, error) {
	if err := validateKey(key); err != nil {
		return ObjectInfo{}, false, err
	}
	stat, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return ObjectInfo{}, false, nil
		}
		return ObjectInfo{}, false, fmt.Errorf("objstore: stat %s: %w", key, err)
	}
	return ObjectInfo{
		Key:         key,
		Size:        stat.Size,
		UpdatedAt:   stat.LastModified.UTC(),
		ContentType: stat.ContentType,
		ETag:        normaliseETag(stat.ETag),
		Metadata:    copyUserMeta(stat.UserMetadata),
	}, true, nil
}

// Delete removes key. S3 DeleteObject is idempotent: deleting a
// non-existent key returns success, no special casing needed.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("objstore: delete %s: %w", key, err)
	}
	return nil
}

// ListPrefix issues a recursive ListObjectsV2. We deliberately don't
// expose pagination tokens — keep the interface uniform with the local
// backend's eager walk. limit caps the result set client-side; passing
// 0 lists everything (use sparingly on large buckets).
func (s *S3Store) ListPrefix(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	if prefix != "" {
		// Same validation rule as keys, except empty prefix is OK
		// (listing everything in the bucket).
		if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") || strings.Contains(prefix, "\\") {
			return nil, fmt.Errorf("objstore: invalid prefix %q", prefix)
		}
	}
	opts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}
	var out []ObjectInfo
	for obj := range s.client.ListObjects(ctx, s.bucket, opts) {
		if obj.Err != nil {
			return nil, fmt.Errorf("objstore: list %s: %w", prefix, obj.Err)
		}
		out = append(out, ObjectInfo{
			Key:         obj.Key,
			Size:        obj.Size,
			UpdatedAt:   obj.LastModified.UTC(),
			ContentType: obj.ContentType,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// PresignGet returns a time-limited GET URL valid for ttl. The URL
// signs the entire request (host + path + query), so the caller can
// hand it to a browser, curl, or downstream client without re-auth.
//
// When the store was constructed via NewS3StoreDual with a distinct
// publicEndpoint, the URL is signed against that public host so
// browsers can fetch directly without traversing the internal mesh
// (server↔RustFS keeps using the internal endpoint). Otherwise the
// URL points at the same endpoint used for server-side operations.
//
// ttl is clamped to [1s, 7d] — minio-go's PresignedGetObject errors
// out below 1s, and 7 days is the AWS S3 maximum for v4 sig.
func (s *S3Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	if err := validateKey(key); err != nil {
		return "", false, err
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	c := s.client
	if s.presignClient != nil {
		c = s.presignClient
	}
	u, err := c.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		return "", false, fmt.Errorf("objstore: presign %s: %w", key, err)
	}
	return u.String(), true, nil
}

// EnsureVersioning makes sure bucket versioning is "Enabled" so a later
// PutObject-with-same-key keeps the old version reachable via
// ListObjectVersions / GetObject?versionId=. Called by cmd/qatlasd/main.go
// at boot.
//
// Idempotent: if status is already "Enabled" we skip the Set call to
// avoid noisy "config change" audit events. We **never** transition
// out of Enabled — even if an operator manually Suspended versioning,
// qatlas reverts on next restart. This is intentional: losing the
// ability to recover an over-written PDF is a much bigger correctness
// hazard than the (small) extra storage cost.
//
// Returns the prior status as a string ("" when never set, "Enabled",
// "Suspended") plus whether we changed it. Errors are descriptive so
// the caller can decide whether to fail-fast or warn-and-continue
// (qatlas chooses the latter — the server still works without
// versioning, the user just loses overwrite-rollback).
func (s *S3Store) EnsureVersioning(ctx context.Context) (priorStatus string, changed bool, err error) {
	cfg, err := s.client.GetBucketVersioning(ctx, s.bucket)
	if err != nil {
		return "", false, fmt.Errorf("objstore: get bucket versioning %s: %w", s.bucket, err)
	}
	if cfg.Status == "Enabled" {
		return cfg.Status, false, nil
	}
	if err := s.client.EnableVersioning(ctx, s.bucket); err != nil {
		return cfg.Status, false, fmt.Errorf("objstore: enable bucket versioning %s: %w", s.bucket, err)
	}
	return cfg.Status, true, nil
}

// ObjectVersion is one entry in a versioned ListObjects result —
// includes everything the prune command needs to decide whether to
// delete. We don't put this in store.go's ObjectInfo because version
// concepts only make sense on S3; LocalStore has no version notion.
type ObjectVersion struct {
	Key            string
	VersionID      string
	IsLatest       bool // true for the current version; false for noncurrent
	IsDeleteMarker bool // soft-deleted entry (versioning artefact); usually want to prune too
	Size           int64
	LastModified   time.Time
}

// ListAllVersions enumerates every version (current + noncurrent +
// delete markers) under prefix. Pass "" prefix to walk the whole
// bucket. Returns objects in S3 list order — *not* sorted by date,
// so callers that want "most recent first per key" must sort.
//
// We pull the full result into memory rather than expose a channel
// because the prune command needs to group by key (decide "keep N
// per key" semantics) which requires the whole list anyway. For
// buckets with hundreds of millions of objects this would need
// pagination + streaming aggregation; current bucket sizes (< 1M
// objects) make it a non-issue.
func (s *S3Store) ListAllVersions(ctx context.Context, prefix string) ([]ObjectVersion, error) {
	if prefix != "" {
		if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") || strings.Contains(prefix, "\\") {
			return nil, fmt.Errorf("objstore: invalid prefix %q", prefix)
		}
	}
	opts := minio.ListObjectsOptions{
		Prefix:       prefix,
		Recursive:    true,
		WithVersions: true,
	}
	var out []ObjectVersion
	for obj := range s.client.ListObjects(ctx, s.bucket, opts) {
		if obj.Err != nil {
			return nil, fmt.Errorf("objstore: list versions %s: %w", prefix, obj.Err)
		}
		out = append(out, ObjectVersion{
			Key:            obj.Key,
			VersionID:      obj.VersionID,
			IsLatest:       obj.IsLatest,
			IsDeleteMarker: obj.IsDeleteMarker,
			Size:           obj.Size,
			LastModified:   obj.LastModified.UTC(),
		})
	}
	return out, nil
}

// DeleteVersion removes one specific version of an object. Idempotent
// at the S3 level — deleting an already-gone version returns success.
// versionID MUST be non-empty; passing "" would delete the current
// version (or add a delete marker on a versioned bucket), which is
// almost never what `storage prune` wants. We guard against that
// to make accidental "prune everything" impossible.
func (s *S3Store) DeleteVersion(ctx context.Context, key, versionID string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if versionID == "" {
		return fmt.Errorf("objstore: DeleteVersion requires non-empty versionID; use Delete for current-version removal")
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{
		VersionID: versionID,
	}); err != nil {
		return fmt.Errorf("objstore: delete version %s@%s: %w", key, versionID, err)
	}
	return nil
}

// copyUserMeta normalises minio-go's metadata map into the lowercase
// form Store contract requires, and returns nil for empty input so
// callers can do plain `if info.Metadata == nil`.
//
// Why copy at all: minio-go reuses the map it returned to us across
// subsequent calls in some code paths; mutating it would race.
func copyUserMeta(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[strings.ToLower(k)] = v
	}
	return out
}

// isNoSuchKey checks whether err is the minio-go "object missing"
// error. We match on the S3 error code rather than the Go error type
// so the check works against any S3-compatible backend that mirrors
// the AWS error scheme.
func isNoSuchKey(err error) bool {
	if err == nil {
		return false
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		switch er.Code {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
		// Some SDK paths surface 404 without a structured Code.
		if er.StatusCode == 404 {
			return true
		}
	}
	return false
}

// isPreconditionFailed detects S3 412 PreconditionFailed responses
// (both IfMatch-stale and IfNoneMatch-collision) as well as the
// special-case "IfMatch on a key that doesn't exist" reply which
// RustFS surfaces as NoSuchKey + 404 but is semantically a precondition
// rejection for our handlers — both mean "your CAS lost, re-Stat and
// decide". We collapse them so callers only branch on one sentinel.
func isPreconditionFailed(err error) bool {
	if err == nil {
		return false
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		if er.Code == "PreconditionFailed" || er.StatusCode == 412 {
			return true
		}
	}
	return false
}

// normaliseETag strips the surrounding double quotes S3 wire-format
// puts around ETag values. minio-go usually does this for us in
// StatObject, but we run it again to be defensive — passing a quoted
// ETag back into SetMatchETag would double-quote it on the next request.
func normaliseETag(etag string) string {
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		return etag[1 : len(etag)-1]
	}
	return etag
}

// Compile-time guard: S3Store implements Store.
var _ Store = (*S3Store)(nil)
