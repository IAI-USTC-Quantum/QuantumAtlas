package shares

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
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

// ResolveTarget maps a share-relative path to its real on-disk location.
// Returns ("", err) when the path escapes its allowed root.
//
// The share path scheme is:
//
//	papers/{pdf|markdown|json|images}/<asset rel>  ->  paperAssetDir(kind)/<asset rel>
//	<other>                                        ->  rawRoot/<other>
//
// Both layers reject ".." traversal.
func ResolveTarget(cfg *config.Config, relPath string) (string, error) {
	rel := strings.Trim(relPath, "/")
	for _, kind := range []string{"pdf", "markdown", "json", "images"} {
		prefix := "papers/" + kind
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			suffix := strings.TrimPrefix(rel[len(prefix):], "/")
			return safeJoin(filepath.Join(cfg.RawDir, kind), suffix,
				"path not under paper "+kind+" directory")
		}
	}
	return safeJoin(cfg.RawDir, rel, "path not under RAW_DIR")
}

// safeJoin joins root + rel, then verifies the result still lives
// under root after symlink/.. resolution. Returns errMsg as the error
// message on escape.
func safeJoin(root, rel, errMsg string) (string, error) {
	abs := filepath.Join(root, rel)
	cleanRoot := filepath.Clean(root)
	cleanAbs := filepath.Clean(abs)
	rrel, err := filepath.Rel(cleanRoot, cleanAbs)
	if err != nil || strings.HasPrefix(rrel, "..") {
		return "", errors.New(errMsg)
	}
	return cleanAbs, nil
}

// CreateRecord persists a new share record and returns the inserted row.
// Validates every path fragment up front and refuses to persist a record
// pointing at a non-existent file (matches the Python 400 behavior).
func CreateRecord(store *Store, cfg *config.Config, opts CreateOptions) (*Record, error) {
	cleaned := make([]string, 0, len(opts.Paths))
	for _, p := range opts.Paths {
		if err := validateFragment(p); err != nil {
			return nil, err
		}
		cleaned = append(cleaned, strings.TrimSpace(p))
	}
	for _, p := range cleaned {
		fs, err := ResolveTarget(cfg, p)
		if err != nil {
			return nil, fmt.Errorf("path does not exist: %s", p)
		}
		if !pathExists(fs) {
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
	if err := store.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
