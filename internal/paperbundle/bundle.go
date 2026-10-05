// Package paperbundle stores immutable source PDFs and complete parser bundles.
// Original parser member names and bytes are never rewritten. A generated
// manifest is the final write and the only object that marks a bundle complete.
package paperbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

var (
	ErrIntegrity = errors.New("paperbundle: immutable object integrity mismatch")
	ErrInvalid   = errors.New("paperbundle: invalid identity or member path")
)

const ManifestVersion = 1

// Member names are the original ZIP relative paths (without a files/ prefix).
type Member struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type Manifest struct {
	Version         int      `json:"version"`
	PaperID         string   `json:"paper_id"`
	SourceID        string   `json:"source_id"`
	RevisionID      string   `json:"revision_id"`
	SourcePDFSHA256 string   `json:"source_pdf_sha256"`
	MiddlePath      string   `json:"middle_path"`
	MarkdownPath    string   `json:"markdown_path"`
	Files           []Member `json:"files"`
}

type Input struct {
	PaperID, SourceID, RevisionID, SourcePDFSHA256 string
	Files                                          map[string][]byte
	MiddlePath, MarkdownPath                       string
}

type FrozenPDF struct {
	PaperID, SourceID, Key, SHA256 string
	SizeBytes                      int64
}

// Store requires conditional writes. Unsupported preconditions fail closed;
// there is deliberately no unconditional-PUT or legacy-content fallback.
type Store struct{ objects objstore.Store }

func New(objects objstore.Store) *Store { return &Store{objects: objects} }

func PDFKey(paperID, sourceID string) string {
	return "content/" + paperID + "/" + sourceID + "/source.pdf"
}
func ManifestKey(paperID, sourceID, revisionID string) string {
	return "content/" + paperID + "/" + sourceID + "/parses/" + revisionID + "/manifest.json"
}
func FileKey(paperID, sourceID, revisionID, memberPath string) string {
	return "content/" + paperID + "/" + sourceID + "/parses/" + revisionID + "/files/" + memberPath
}

func SHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ValidateMemberPath rejects ambiguous/traversing paths without cleaning or
// renaming valid names. Dots within a component and unknown extensions are OK.
func ValidateMemberPath(name string) error {
	if name == "" || !utf8.ValidString(name) || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("%w: %q", ErrInvalid, name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: %q", ErrInvalid, name)
		}
	}
	return nil
}

func validateIDs(ids ...string) error {
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			return ErrInvalid
		}
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return fmt.Errorf("%w: id %q", ErrInvalid, id)
			}
		}
	}
	return nil
}

func validSHA(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// FreezePDF atomically creates the immutable copy, then hashes the actual
// stored bytes, even when another writer won the create. A colliding key with
// different bytes is an integrity error, never a reason to overwrite it.
func (s *Store) FreezePDF(ctx context.Context, paperID, sourceID string, pdf []byte, expectedSHA string) (FrozenPDF, error) {
	if err := validateIDs(paperID, sourceID); err != nil {
		return FrozenPDF{}, err
	}
	if len(pdf) == 0 {
		return FrozenPDF{}, fmt.Errorf("%w: empty PDF", ErrInvalid)
	}
	sha := SHA256(pdf)
	if expectedSHA != "" && (!validSHA(expectedSHA) || expectedSHA != sha) {
		return FrozenPDF{}, ErrIntegrity
	}
	key := PDFKey(paperID, sourceID)
	if err := s.createVerified(ctx, key, pdf, "application/pdf"); err != nil {
		return FrozenPDF{}, err
	}
	return FrozenPDF{PaperID: paperID, SourceID: sourceID, Key: key, SHA256: sha, SizeBytes: int64(len(pdf))}, nil
}

// ReadPDF reads only the frozen key and verifies its pinned hash and size.
// A frozen identity must never be served from a mutable legacy PDF location.
func (s *Store) ReadPDF(ctx context.Context, paperID, sourceID, expectedSHA string, expectedSize int64) ([]byte, error) {
	if err := validateIDs(paperID, sourceID); err != nil {
		return nil, err
	}
	if !validSHA(expectedSHA) || expectedSize <= 0 {
		return nil, ErrInvalid
	}
	body, err := s.read(ctx, PDFKey(paperID, sourceID))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != expectedSize || SHA256(body) != expectedSHA {
		return nil, ErrIntegrity
	}
	return body, nil
}

// WriteBundle persists every valid member (including unknown files) unchanged,
// verifies persisted bytes, and finally creates/verifies the frozen manifest.
// Failures may leave orphan members but can never create a complete marker.
func (s *Store) WriteBundle(ctx context.Context, in Input) (Manifest, error) {
	m := Manifest{Version: ManifestVersion, PaperID: in.PaperID, SourceID: in.SourceID,
		RevisionID: in.RevisionID, SourcePDFSHA256: in.SourcePDFSHA256,
		MiddlePath: in.MiddlePath, MarkdownPath: in.MarkdownPath}
	if err := validateIDs(in.PaperID, in.SourceID, in.RevisionID); err != nil {
		return Manifest{}, err
	}
	if !validSHA(in.SourcePDFSHA256) {
		return Manifest{}, ErrInvalid
	}
	for name, data := range in.Files {
		if err := ValidateMemberPath(name); err != nil {
			return Manifest{}, err
		}
		m.Files = append(m.Files, Member{Path: name, SizeBytes: int64(len(data)), SHA256: SHA256(data)})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	pdf, err := s.read(ctx, PDFKey(in.PaperID, in.SourceID))
	if err != nil {
		return Manifest{}, err
	}
	if len(pdf) == 0 || SHA256(pdf) != in.SourcePDFSHA256 {
		return Manifest{}, ErrIntegrity
	}
	for _, f := range m.Files {
		ct := mime.TypeByExtension(path.Ext(f.Path))
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := s.createVerified(ctx, FileKey(in.PaperID, in.SourceID, in.RevisionID, f.Path), in.Files[f.Path], ct); err != nil {
			return Manifest{}, err
		}
	}
	body, err := json.Marshal(m)
	if err != nil {
		return Manifest{}, err
	}
	if err := s.createVerified(ctx, ManifestKey(in.PaperID, in.SourceID, in.RevisionID), body, "application/json"); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func ValidateManifest(m Manifest) error {
	if err := validateIDs(m.PaperID, m.SourceID, m.RevisionID); err != nil {
		return err
	}
	if m.Version != ManifestVersion || !validSHA(m.SourcePDFSHA256) || len(m.Files) == 0 {
		return ErrInvalid
	}
	if err := ValidateMemberPath(m.MiddlePath); err != nil {
		return err
	}
	if err := ValidateMemberPath(m.MarkdownPath); err != nil {
		return err
	}
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if err := ValidateMemberPath(f.Path); err != nil {
			return err
		}
		if seen[f.Path] || f.SizeBytes < 0 || !validSHA(f.SHA256) {
			return ErrInvalid
		}
		seen[f.Path] = true
	}
	if !seen[m.MiddlePath] || !seen[m.MarkdownPath] || m.MiddlePath == m.MarkdownPath {
		return ErrInvalid
	}
	// File/directory collisions cannot be represented faithfully by local
	// storage and are not valid portable ZIP member sets.
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("%w: file/directory collision %q", ErrInvalid, parent)
			}
		}
	}
	return nil
}

func (m Manifest) Member(name string) (Member, bool) {
	for _, f := range m.Files {
		if f.Path == name {
			return f, true
		}
	}
	return Member{}, false
}

// GetManifest reads the marker, validates its complete member inventory and
// scoping, but does not read members. VerifyBundle is required before publishing.
func (s *Store) GetManifest(ctx context.Context, paperID, sourceID, revisionID string) (Manifest, error) {
	if err := validateIDs(paperID, sourceID, revisionID); err != nil {
		return Manifest{}, err
	}
	body, err := s.read(ctx, ManifestKey(paperID, sourceID, revisionID))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest JSON: %v", ErrIntegrity, err)
	}
	if err := ValidateManifest(m); err != nil {
		return Manifest{}, err
	}
	if m.PaperID != paperID || m.SourceID != sourceID || m.RevisionID != revisionID {
		return Manifest{}, ErrIntegrity
	}
	return m, nil
}

// VerifyBundle re-reads and hashes the source PDF and every listed member. It
// never treats metadata, object existence, or a partial upload as proof of ready.
func (s *Store) VerifyBundle(ctx context.Context, paperID, sourceID, revisionID string) (Manifest, error) {
	m, err := s.GetManifest(ctx, paperID, sourceID, revisionID)
	if err != nil {
		return Manifest{}, err
	}
	pdf, err := s.read(ctx, PDFKey(paperID, sourceID))
	if err != nil {
		return Manifest{}, err
	}
	if len(pdf) == 0 || SHA256(pdf) != m.SourcePDFSHA256 {
		return Manifest{}, ErrIntegrity
	}
	for _, f := range m.Files {
		if err := s.verify(ctx, FileKey(paperID, sourceID, revisionID, f.Path), f.SizeBytes, f.SHA256); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

func (s *Store) createVerified(ctx context.Context, key string, body []byte, contentType string) error {
	if s == nil || s.objects == nil {
		return errors.New("paperbundle: storage unavailable")
	}
	n, err := s.objects.PutWithOptions(ctx, key, bytes.NewReader(body), int64(len(body)), objstore.PutOptions{ContentType: contentType, IfNoneMatch: "*"})
	if err != nil && !errors.Is(err, objstore.ErrPreconditionFailed) {
		return err
	}
	if err == nil && n != int64(len(body)) {
		return fmt.Errorf("%w: short write %s", ErrIntegrity, key)
	}
	return s.verify(ctx, key, int64(len(body)), SHA256(body))
}

func (s *Store) verify(ctx context.Context, key string, size int64, sha string) error {
	if s == nil || s.objects == nil {
		return errors.New("paperbundle: storage unavailable")
	}
	r, _, err := s.objects.Get(ctx, key)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, readErr := io.Copy(h, io.LimitReader(r, size+1))
	closeErr := r.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sha {
		return fmt.Errorf("%w: %s", ErrIntegrity, key)
	}
	return nil
}

func (s *Store) read(ctx context.Context, key string) ([]byte, error) {
	if s == nil || s.objects == nil {
		return nil, errors.New("paperbundle: storage unavailable")
	}
	r, _, err := s.objects.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil {
		return nil, readErr
	}
	return body, closeErr
}
