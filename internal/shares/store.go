package shares

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// URLPrefix is the public route prefix for share content.
const URLPrefix = "/share"

// PermanentSharePaths are the path roots exposed by the configured
// permanent share token (when set). Mirrors atlas/server/routers/shares.py:
// PERMANENT_SHARE_PATHS.
var PermanentSharePaths = []string{
	"papers/pdf",
	"papers/markdown",
	"papers/json",
	"papers/images",
}

// PermanentRecord returns a synthesized record representing the
// configured non-expiring share token, or nil if none is configured.
func PermanentRecord(cfg *config.Config) *Record {
	if cfg.ShareAccessToken == "" {
		return nil
	}
	return &Record{
		Token:     cfg.ShareAccessToken,
		Paths:     append([]string{}, PermanentSharePaths...),
		CreatedBy: "config",
		CreatedAt: "config",
		Label:     "configured permanent paper asset share",
	}
}

// BuildURL constructs a share URL fragment "/share/<token>" or
// "/share/<token>/<relPath>". If baseURL is non-empty, prepends it
// (with trailing slash stripped) to produce an absolute URL.
func BuildURL(token, relPath, baseURL string) string {
	prefix := URLPrefix
	if baseURL != "" {
		prefix = strings.TrimRight(baseURL, "/") + URLPrefix
	}
	if relPath == "" {
		return fmt.Sprintf("%s/%s", prefix, token)
	}
	return fmt.Sprintf("%s/%s/%s", prefix, token, strings.Trim(relPath, "/"))
}

// BuildExternalURL is BuildURL with cfg.PublicBaseURL as the absolute
// base. Returns an error when PublicBaseURL is empty — share URLs only
// make sense once the operator has told us where the server lives
// externally.
func BuildExternalURL(cfg *config.Config, token, relPath string) (string, error) {
	if cfg.PublicBaseURL == "" {
		return "", errors.New("QATLAS_SERVER_URL / PUBLIC_BASE_URL must be set to build external share URLs")
	}
	return BuildURL(token, relPath, cfg.PublicBaseURL), nil
}

// NewToken returns a fresh 32-char hex token.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateOptions configures CreateRecord.
type CreateOptions struct {
	Paths     []string
	Label     string
	ExpiresIn int // seconds; 0 means use the configured default (or no expiry)
	CreatedBy string
}

// ValidatedPathError signals an invalid relative path supplied to
// CreateRecord. Carries the offending path so the route handler can
// surface it in the 400 response.
type ValidatedPathError struct {
	Path   string
	Reason string
}

func (e *ValidatedPathError) Error() string {
	return fmt.Sprintf("invalid share path %q: %s", e.Path, e.Reason)
}

// validateFragment enforces "no abs path / no backslashes / no ..".
func validateFragment(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return &ValidatedPathError{Path: p, Reason: "must be non-empty"}
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "..") {
		return &ValidatedPathError{Path: p, Reason: "must be relative without '..' or backslashes"}
	}
	return nil
}

// IsUnderShare reports whether path lives under any of the share's
// permitted roots. Caller should canonicalize path first (no leading /).
func IsUnderShare(path string, allowedRoots []string) bool {
	p := strings.Trim(path, "/")
	for _, r := range allowedRoots {
		r = strings.Trim(r, "/")
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// ResolveKey maps a share-relative path to its corresponding object key
// in the raw objstore. Returns ("", err) when the path escapes its
// allowed root.
//
// Share path scheme:
//
//	papers/{pdf|markdown|json|images}/<rest>   ->   {pdf|...}/<rest>
//	<other>                                    ->   <other>          (verbatim)
//
// All paths are validated against ".." traversal regardless of which
// branch they fall into; the per-kind prefix is just a namespace marker
// the share API expects, the underlying object key drops the "papers/"
// for compatibility with the objstore layout.
func ResolveKey(relPath string) (string, error) {
	rel := strings.Trim(relPath, "/")
	if rel == "" {
		return "", errors.New("share path must be non-empty")
	}
	if strings.Contains(rel, "..") || strings.Contains(rel, "\\") {
		return "", errors.New("share path must not contain '..' or backslashes")
	}
	for _, kind := range []string{"pdf", "markdown", "json", "images"} {
		prefix := "papers/" + kind
		if rel == prefix {
			return kind, nil
		}
		if strings.HasPrefix(rel, prefix+"/") {
			suffix := strings.TrimPrefix(rel[len(prefix):], "/")
			return kind + "/" + suffix, nil
		}
	}
	// Out-of-scheme paths (the historical "RAW_DIR catch-all" branch)
	// map directly to the same key string. Caller should treat this
	// path as a key into the raw store.
	return rel, nil
}

// ResolveTarget maps a share-relative path to its real on-disk location
// under cfg.RawDir. This is retained for paths that genuinely need a
// filesystem path (e.g. backup/scrub tooling); HTTP handlers should use
// ResolveKey + objstore.Store instead so the same code path serves both
// local and S3 backends.
func ResolveTarget(cfg *config.Config, relPath string) (string, error) {
	key, err := ResolveKey(relPath)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(cfg.RawDir, filepath.FromSlash(key))
	cleanRoot := filepath.Clean(cfg.RawDir)
	cleanAbs := filepath.Clean(abs)
	rrel, err := filepath.Rel(cleanRoot, cleanAbs)
	if err != nil || strings.HasPrefix(rrel, "..") {
		return "", errors.New("path escapes RAW_DIR")
	}
	return cleanAbs, nil
}

// CreateRecord persists a new share record and returns the inserted row.
// Validates every path fragment up front and verifies (via store.Stat)
// that the underlying object actually exists — matches the historical
// "400 on missing path" behaviour from the FastAPI implementation.
//
// store can be nil when the caller doesn't want the existence check
// (e.g. permanent / wildcard shares created from config). When non-nil,
// every cleaned path is Stat'd as an object key derived via ResolveKey.
func CreateRecord(s *Store, cfg *config.Config, opts CreateOptions, store objstore.Store) (*Record, error) {
	cleaned := make([]string, 0, len(opts.Paths))
	for _, p := range opts.Paths {
		if err := validateFragment(p); err != nil {
			return nil, err
		}
		cleaned = append(cleaned, strings.TrimSpace(p))
	}
	if store != nil {
		ctx := context.Background()
		for _, p := range cleaned {
			key, err := ResolveKey(p)
			if err != nil {
				return nil, fmt.Errorf("path does not exist: %s", p)
			}
			// File-shaped key: try Stat directly. If that misses, fall
			// back to a 1-result ListPrefix(key+"/") so images
			// "directory" keys still pass the existence check on both
			// local and S3 backends.
			if _, exists, _ := store.Stat(ctx, key); exists {
				continue
			}
			listed, _ := store.ListPrefix(ctx, key+"/", 1)
			if len(listed) > 0 {
				continue
			}
			return nil, fmt.Errorf("path does not exist: %s", p)
		}
	}

	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ttl := opts.ExpiresIn
	if ttl == 0 {
		ttl = cfg.DefaultShareExpiresIn
	}

	rec := &Record{
		Token:     token,
		Paths:     cleaned,
		CreatedBy: opts.CreatedBy,
		CreatedAt: now.Format(time.RFC3339),
		Label:     opts.Label,
	}
	if ttl > 0 {
		rec.ExpiresAt = now.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339)
	}
	if err := s.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}
