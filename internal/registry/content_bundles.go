package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ParseBundle is publication metadata for a complete immutable parser bundle.
// Legacy parse_revisions without a parse_bundles row are never ready bundles.
type ParseBundle struct {
	ParseRevision
	ManifestKey, SourcePDFSHA256, MiddlePath, MarkdownPath, ManifestSHA256 string
}

// A small transaction surface keeps the source/publication logic testable with
// an in-memory catalog. The real implementation always uses a PostgreSQL txn.
type contentTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

const sourceColumns = `source_id, paper_id, origin, sha256, objstore_key, size_bytes, created_at`
const bundleColumns = `r.revision_id, r.paper_id, r.source_id, r.schema, r.schema_version,
 r.artifact_sha256, r.objstore_key, r.tier, r.created_at, r.is_current,
 b.manifest_key, b.source_pdf_sha256, b.middle_path, b.markdown_path, b.manifest_sha256`

// GetImportedPaperSource resolves a pinned legacy location without accessing
// that object. Callers must check this BEFORE legacy Stat/download fallback, then
// FreezePaperSource verifies the frozen bytes and fails closed if missing.
func (s *Store) GetImportedPaperSource(ctx context.Context, paperID, legacyPDFKey string) (PaperSource, bool, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, false, ErrCatalogUnavailable
	}
	src, err := scanPaperSource(s.pool.QueryRow(ctx, `SELECT s.source_id, s.paper_id, s.origin, s.sha256, s.objstore_key, s.size_bytes, s.created_at
 FROM paper_source_imports i JOIN paper_sources s ON s.source_id=i.source_id AND s.paper_id=i.paper_id
 WHERE i.paper_id=$1 AND i.legacy_key=$2`, paperID, legacyPDFKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, false, nil
	}
	if err != nil {
		return PaperSource{}, false, catalogUnavailable("registry: get imported source", err)
	}
	return src, true, nil
}

// BindPaperSourceImport pins a canonical/legacy alias for a freshly registered
// source without accessing or writing that legacy location. Existing aliases
// remain authoritative: a different SHA cannot replace their source identity.
func (s *Store) BindPaperSourceImport(ctx context.Context, objects objstore.Store, paperID, sourceID, legacyPDFKey string) (PaperSource, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, ErrCatalogUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: begin source alias binding", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	src, err := bindPaperSourceImport(ctx, tx, objects, paperID, sourceID, legacyPDFKey)
	if err != nil {
		return PaperSource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperSource{}, catalogUnavailable("registry: commit source alias binding", err)
	}
	return src, nil
}

func bindPaperSourceImport(ctx context.Context, tx contentTx, objects objstore.Store, paperID, sourceID, legacyPDFKey string) (PaperSource, error) {
	if paperID == "" || sourceID == "" || legacyPDFKey == "" {
		return PaperSource{}, paperbundle.ErrInvalid
	}
	if err := lockContentPaper(ctx, tx, paperID); err != nil {
		return PaperSource{}, err
	}
	src, err := scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources WHERE paper_id=$1 AND source_id=$2`, paperID, sourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, objstore.ErrNotFound
	}
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: source for alias binding", err)
	}
	if src.ObjstoreKey != paperbundle.PDFKey(paperID, sourceID) {
		return PaperSource{}, paperbundle.ErrIntegrity
	}
	src, err = freezePaperSource(ctx, tx, objects, src)
	if err != nil {
		return PaperSource{}, err
	}
	prior, err := scanPaperSource(tx.QueryRow(ctx, `SELECT s.source_id, s.paper_id, s.origin, s.sha256, s.objstore_key, s.size_bytes, s.created_at
 FROM paper_source_imports i JOIN paper_sources s ON s.source_id=i.source_id AND s.paper_id=i.paper_id
 WHERE i.paper_id=$1 AND i.legacy_key=$2`, paperID, legacyPDFKey))
	if err == nil {
		if prior.Sha256 != src.Sha256 {
			return PaperSource{}, paperbundle.ErrIntegrity
		}
		return freezePaperSource(ctx, tx, objects, prior)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, catalogUnavailable("registry: check bound alias", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO paper_source_imports (paper_id,legacy_key,source_id) VALUES ($1,$2,$3) ON CONFLICT (paper_id,legacy_key) DO NOTHING`, paperID, legacyPDFKey, sourceID)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: bind source alias", err)
	}
	return src, nil
}

// RegisterFrozenPaperSource lazily imports only a legacy PDF, reusing an existing
// identity for identical SHA when possible. A paper-scoped transaction lock
// serializes registration so concurrent requests do not mint duplicate sources.
// The legacy location is pinned on first success; subsequent access reads ONLY
// the frozen source, even if the legacy object has changed or disappeared.
func (s *Store) RegisterFrozenPaperSource(ctx context.Context, objects objstore.Store, paperID, origin, legacyPDFKey string) (PaperSource, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, ErrCatalogUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: begin source import", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	src, err := registerFrozenPaperSource(ctx, tx, objects, paperID, origin, legacyPDFKey)
	if err != nil {
		return PaperSource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperSource{}, catalogUnavailable("registry: commit source import", err)
	}
	return src, nil
}

// RegisterFrozenPDF registers freshly fetched/uploaded bytes directly into
// immutable content storage. It never writes to any legacy PDF location.
func (s *Store) RegisterFrozenPDF(ctx context.Context, objects objstore.Store, paperID, origin string, pdf []byte) (PaperSource, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, ErrCatalogUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: begin fresh source registration", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	src, err := registerFrozenPDF(ctx, tx, objects, paperID, origin, pdf)
	if err != nil {
		return PaperSource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperSource{}, catalogUnavailable("registry: commit fresh source registration", err)
	}
	return src, nil
}

func registerFrozenPDF(ctx context.Context, tx contentTx, objects objstore.Store, paperID, origin string, pdf []byte) (PaperSource, error) {
	if paperID == "" || origin == "" || len(pdf) == 0 {
		return PaperSource{}, paperbundle.ErrInvalid
	}
	if err := lockContentPaper(ctx, tx, paperID); err != nil {
		return PaperSource{}, err
	}
	return registerPDFBytes(ctx, tx, objects, paperID, origin, pdf)
}

// Caller must already hold the paper's content transaction lock.
func registerPDFBytes(ctx context.Context, tx contentTx, objects objstore.Store, paperID, origin string, pdf []byte) (PaperSource, error) {
	sha := paperbundle.SHA256(pdf)
	src, err := scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources
 WHERE paper_id = $1 AND sha256 = $2 ORDER BY created_at, source_id LIMIT 1`, paperID, sha))
	if err == nil {
		// Existing IDs (and comments) remain stable when their storage moves.
		return freezeSourceBytes(ctx, tx, objects, src, pdf)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, catalogUnavailable("registry: reuse source SHA", err)
	}
	src = PaperSource{SourceID: NewSourceID(), PaperID: paperID, Origin: origin, Sha256: sha, SizeBytes: int64(len(pdf))}
	frozen, err := paperbundle.New(objects).FreezePDF(ctx, paperID, src.SourceID, pdf, sha)
	if err != nil {
		return PaperSource{}, err
	}
	src.ObjstoreKey = frozen.Key
	_, err = tx.Exec(ctx, `INSERT INTO paper_sources
 (source_id, paper_id, origin, sha256, objstore_key, size_bytes) VALUES ($1,$2,$3,$4,$5,$6)`,
		src.SourceID, src.PaperID, src.Origin, src.Sha256, src.ObjstoreKey, src.SizeBytes)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: register frozen source", err)
	}
	src, err = scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources WHERE paper_id=$1 AND source_id=$2`, paperID, src.SourceID))
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: read registered source", err)
	}
	return src, nil
}

func lockContentPaper(ctx context.Context, tx contentTx, paperID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "content:"+paperID)
	if err != nil {
		return catalogUnavailable("registry: lock paper content", err)
	}
	return nil
}

func registerFrozenPaperSource(ctx context.Context, tx contentTx, objects objstore.Store, paperID, origin, legacyPDFKey string) (PaperSource, error) {
	if paperID == "" || origin == "" || legacyPDFKey == "" {
		return PaperSource{}, paperbundle.ErrInvalid
	}
	if err := lockContentPaper(ctx, tx, paperID); err != nil {
		return PaperSource{}, err
	}
	row := tx.QueryRow(ctx, `SELECT s.source_id, s.paper_id, s.origin, s.sha256, s.objstore_key, s.size_bytes, s.created_at
 FROM paper_source_imports i JOIN paper_sources s ON s.source_id = i.source_id AND s.paper_id = i.paper_id
 WHERE i.paper_id = $1 AND i.legacy_key = $2`, paperID, legacyPDFKey)
	src, err := scanPaperSource(row)
	if err == nil {
		return freezePaperSource(ctx, tx, objects, src)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, catalogUnavailable("registry: lookup imported source", err)
	}

	// Preserve a pre-existing source ID whose old location is being moved.
	src, err = scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources
 WHERE paper_id = $1 AND objstore_key = $2 ORDER BY created_at, source_id LIMIT 1`, paperID, legacyPDFKey))
	if errors.Is(err, pgx.ErrNoRows) {
		pdf, readErr := readContentObject(ctx, objects, legacyPDFKey)
		if readErr != nil {
			return PaperSource{}, readErr
		}
		if len(pdf) == 0 {
			return PaperSource{}, paperbundle.ErrInvalid
		}
		src, err = registerPDFBytes(ctx, tx, objects, paperID, origin, pdf)
		if err != nil {
			return PaperSource{}, err
		}
	}
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: lookup source identity", err)
	}
	// Existing location hits and already frozen imports both go through exact
	// byte verification before a mapping can become visible.
	src, err = freezePaperSource(ctx, tx, objects, src)
	if err != nil {
		return PaperSource{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO paper_source_imports (paper_id, legacy_key, source_id)
 VALUES ($1,$2,$3) ON CONFLICT (paper_id, legacy_key) DO NOTHING`, paperID, legacyPDFKey, src.SourceID)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: pin imported source", err)
	}
	return src, nil
}

// FreezePaperSource preserves SourceID and all identity fields. It reloads the
// authoritative row under the same paper lock used by lazy registration; only
// the storage location may change, and only after same-SHA persisted verification.
func (s *Store) FreezePaperSource(ctx context.Context, objects objstore.Store, src PaperSource) (PaperSource, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, ErrCatalogUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: begin source freeze", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockContentPaper(ctx, tx, src.PaperID); err != nil {
		return PaperSource{}, err
	}
	actual, err := scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources WHERE paper_id=$1 AND source_id=$2`, src.PaperID, src.SourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, objstore.ErrNotFound
	}
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: reload source to freeze", err)
	}
	if src.Sha256 != "" && src.Sha256 != actual.Sha256 {
		return PaperSource{}, paperbundle.ErrIntegrity
	}
	actual, err = freezePaperSource(ctx, tx, objects, actual)
	if err != nil {
		return PaperSource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperSource{}, catalogUnavailable("registry: commit source freeze", err)
	}
	return actual, nil
}

func freezePaperSource(ctx context.Context, tx contentTx, objects objstore.Store, src PaperSource) (PaperSource, error) {
	key := paperbundle.PDFKey(src.PaperID, src.SourceID)
	if strings.HasPrefix(src.ObjstoreKey, "content/") {
		if src.ObjstoreKey != key {
			return PaperSource{}, paperbundle.ErrIntegrity
		}
		_, err := paperbundle.New(objects).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
		return src, err // Fail closed: NEVER read a legacy key after freezing.
	}
	pdf, err := readContentObject(ctx, objects, src.ObjstoreKey)
	if err != nil {
		return PaperSource{}, err
	}
	return freezeSourceBytes(ctx, tx, objects, src, pdf)
}

func freezeSourceBytes(ctx context.Context, tx contentTx, objects objstore.Store, src PaperSource, pdf []byte) (PaperSource, error) {
	// A frozen identity cannot be repaired from mutable legacy bytes, even when
	// another legacy alias happened to have the originally recorded SHA.
	if strings.HasPrefix(src.ObjstoreKey, "content/") {
		return freezePaperSource(ctx, tx, objects, src)
	}
	if int64(len(pdf)) != src.SizeBytes || paperbundle.SHA256(pdf) != src.Sha256 {
		return PaperSource{}, paperbundle.ErrIntegrity
	}
	frozen, err := paperbundle.New(objects).FreezePDF(ctx, src.PaperID, src.SourceID, pdf, src.Sha256)
	if err != nil {
		return PaperSource{}, err
	}
	oldKey := src.ObjstoreKey
	tag, err := tx.Exec(ctx, `UPDATE paper_sources SET objstore_key=$1
 WHERE paper_id=$2 AND source_id=$3 AND sha256=$4 AND size_bytes=$5 AND objstore_key=$6`,
		frozen.Key, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes, oldKey)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: move same-SHA source location", err)
	}
	if tag.RowsAffected() != 1 {
		return PaperSource{}, paperbundle.ErrIntegrity
	}
	_, err = tx.Exec(ctx, `INSERT INTO paper_source_imports (paper_id, legacy_key, source_id)
 VALUES ($1,$2,$3) ON CONFLICT (paper_id, legacy_key) DO NOTHING`, src.PaperID, oldKey, src.SourceID)
	if err != nil {
		return PaperSource{}, catalogUnavailable("registry: pin prior source location", err)
	}
	src.ObjstoreKey = frozen.Key
	return src, nil
}

// PublishBundle verifies all persisted objects before atomically registering the
// revision, its complete bundle, and (optionally) the paper's current pointer.
// Supplied Middle key/hash must agree; blank fields are filled from the manifest.
func (s *Store) PublishBundle(ctx context.Context, objects objstore.Store, rev ParseRevision, setCurrent bool) (ParseBundle, error) {
	if !s.ensure(ctx) {
		return ParseBundle{}, ErrCatalogUnavailable
	}
	bundle, err := prepareBundlePublication(ctx, objects, rev)
	if err != nil {
		return ParseBundle{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ParseBundle{}, catalogUnavailable("registry: begin bundle publication", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := publishBundle(ctx, tx, objects, bundle, setCurrent); err != nil {
		return ParseBundle{}, err
	}
	bundle, err = scanParseBundle(tx.QueryRow(ctx, `SELECT `+bundleColumns+`
 FROM parse_revisions r JOIN parse_bundles b ON b.revision_id=r.revision_id
 WHERE r.paper_id=$1 AND r.revision_id=$2`, rev.PaperID, rev.RevisionID))
	if err != nil {
		return ParseBundle{}, catalogUnavailable("registry: read published bundle", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ParseBundle{}, catalogUnavailable("registry: commit bundle publication", err)
	}
	return bundle, nil
}

func prepareBundlePublication(ctx context.Context, objects objstore.Store, rev ParseRevision) (ParseBundle, error) {
	m, err := paperbundle.New(objects).VerifyBundle(ctx, rev.PaperID, rev.SourceID, rev.RevisionID)
	if err != nil {
		return ParseBundle{}, err
	}
	middle, _ := m.Member(m.MiddlePath)
	key := paperbundle.FileKey(rev.PaperID, rev.SourceID, rev.RevisionID, m.MiddlePath)
	if rev.Schema == "" || (rev.ObjstoreKey != "" && rev.ObjstoreKey != key) ||
		(rev.ArtifactSha256 != "" && rev.ArtifactSha256 != middle.SHA256) {
		return ParseBundle{}, paperbundle.ErrIntegrity
	}
	rev.ObjstoreKey, rev.ArtifactSha256, rev.Tier = key, middle.SHA256, NormalizeParseTier(rev.Tier)
	manifestKey := paperbundle.ManifestKey(rev.PaperID, rev.SourceID, rev.RevisionID)
	body, err := readContentObject(ctx, objects, manifestKey)
	if err != nil {
		return ParseBundle{}, err
	}
	return ParseBundle{ParseRevision: rev, ManifestKey: manifestKey, ManifestSHA256: paperbundle.SHA256(body),
		SourcePDFSHA256: m.SourcePDFSHA256, MiddlePath: m.MiddlePath, MarkdownPath: m.MarkdownPath}, nil
}

func publishBundle(ctx context.Context, tx contentTx, objects objstore.Store, b ParseBundle, setCurrent bool) error {
	if err := lockContentPaper(ctx, tx, b.PaperID); err != nil {
		return err
	}
	// Recheck after waiting for the publication lock, before any DB writes.
	if err := verifyPublishedBundle(ctx, objects, b); err != nil {
		return err
	}
	src, err := scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources WHERE paper_id=$1 AND source_id=$2`, b.PaperID, b.SourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return objstore.ErrNotFound
	}
	if err != nil {
		return catalogUnavailable("registry: bundle source identity", err)
	}
	if src.Sha256 != b.SourcePDFSHA256 || src.ObjstoreKey != paperbundle.PDFKey(b.PaperID, b.SourceID) {
		return paperbundle.ErrIntegrity
	}
	if _, err := paperbundle.New(objects).ReadPDF(ctx, b.PaperID, b.SourceID, src.Sha256, src.SizeBytes); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO parse_revisions
 (revision_id,paper_id,source_id,schema,schema_version,artifact_sha256,objstore_key,tier,is_current)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,FALSE) ON CONFLICT (revision_id) DO NOTHING`,
		b.RevisionID, b.PaperID, b.SourceID, b.Schema, b.SchemaVersion, b.ArtifactSha256, b.ObjstoreKey, b.Tier)
	if err != nil {
		return catalogUnavailable("registry: insert bundle revision", err)
	}
	actual, err := scanParseRevision(tx.QueryRow(ctx, `SELECT revision_id,paper_id,source_id,schema,schema_version,
 artifact_sha256,objstore_key,tier,created_at,is_current FROM parse_revisions WHERE revision_id=$1`, b.RevisionID))
	if err != nil {
		return catalogUnavailable("registry: verify bundle revision identity", err)
	}
	if !sameRevision(actual, b.ParseRevision) {
		return paperbundle.ErrIntegrity
	}
	_, err = tx.Exec(ctx, `INSERT INTO parse_bundles
 (revision_id,paper_id,source_id,source_pdf_sha256,manifest_key,manifest_sha256,middle_path,markdown_path)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (revision_id) DO NOTHING`,
		b.RevisionID, b.PaperID, b.SourceID, b.SourcePDFSHA256, b.ManifestKey, b.ManifestSHA256, b.MiddlePath, b.MarkdownPath)
	if err != nil {
		return catalogUnavailable("registry: insert complete bundle", err)
	}
	published, err := scanParseBundle(tx.QueryRow(ctx, `SELECT `+bundleColumns+`
 FROM parse_revisions r JOIN parse_bundles b ON b.revision_id=r.revision_id WHERE r.revision_id=$1`, b.RevisionID))
	if err != nil {
		return catalogUnavailable("registry: verify complete bundle identity", err)
	}
	if !sameBundle(published, b) {
		return paperbundle.ErrIntegrity
	}
	if setCurrent {
		if _, err := tx.Exec(ctx, `UPDATE parse_revisions SET is_current=FALSE WHERE paper_id=$1 AND is_current AND revision_id<>$2`, b.PaperID, b.RevisionID); err != nil {
			return catalogUnavailable("registry: clear current bundle", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE parse_revisions SET is_current=TRUE WHERE paper_id=$1 AND revision_id=$2`, b.PaperID, b.RevisionID); err != nil {
			return catalogUnavailable("registry: publish current bundle", err)
		}
	}
	return nil
}

func sameRevision(a, b ParseRevision) bool {
	return a.RevisionID == b.RevisionID && a.PaperID == b.PaperID && a.SourceID == b.SourceID && a.Schema == b.Schema &&
		a.SchemaVersion == b.SchemaVersion && a.ArtifactSha256 == b.ArtifactSha256 && a.ObjstoreKey == b.ObjstoreKey && a.Tier == b.Tier
}
func sameBundle(a, b ParseBundle) bool {
	return sameRevision(a.ParseRevision, b.ParseRevision) && a.ManifestKey == b.ManifestKey && a.ManifestSHA256 == b.ManifestSHA256 &&
		a.SourcePDFSHA256 == b.SourcePDFSHA256 && a.MiddlePath == b.MiddlePath && a.MarkdownPath == b.MarkdownPath
}

func (s *Store) GetParseBundle(ctx context.Context, paperID, revisionID string) (ParseBundle, bool, error) {
	if !s.ensure(ctx) {
		return ParseBundle{}, false, ErrCatalogUnavailable
	}
	b, err := scanParseBundle(s.pool.QueryRow(ctx, `SELECT `+bundleColumns+`
 FROM parse_revisions r JOIN parse_bundles b ON b.revision_id=r.revision_id AND b.paper_id=r.paper_id AND b.source_id=r.source_id
 WHERE r.paper_id=$1 AND r.revision_id=$2`, paperID, revisionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ParseBundle{}, false, nil
	}
	if err != nil {
		return ParseBundle{}, false, catalogUnavailable("registry: get parse bundle", err)
	}
	return b, true, nil
}

// GetReadyParseBundle checks the latest complete revision for this paper/source.
// Source-scoped lookups work even if another source is the paper-wide current.
// Missing/corrupt members are a cache miss (new revision can repair the parse),
// but backend outages are errors and must not trigger replacement acquisition.
func (s *Store) GetReadyParseBundle(ctx context.Context, objects objstore.Store, paperID, sourceID string) (ParseBundle, bool, error) {
	if !s.ensure(ctx) {
		return ParseBundle{}, false, ErrCatalogUnavailable
	}
	b, err := scanParseBundle(s.pool.QueryRow(ctx, `SELECT `+bundleColumns+`
 FROM parse_revisions r JOIN parse_bundles b ON b.revision_id=r.revision_id AND b.paper_id=r.paper_id AND b.source_id=r.source_id
 JOIN paper_sources s ON s.paper_id=r.paper_id AND s.source_id=r.source_id AND s.sha256=b.source_pdf_sha256
 WHERE r.paper_id=$1 AND r.source_id=$2 ORDER BY r.is_current DESC,r.created_at DESC,r.revision_id DESC LIMIT 1`, paperID, sourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ParseBundle{}, false, nil
	}
	if err != nil {
		return ParseBundle{}, false, catalogUnavailable("registry: get ready parse bundle", err)
	}
	if err := verifyPublishedBundle(ctx, objects, b); err != nil {
		if errors.Is(err, objstore.ErrNotFound) || errors.Is(err, paperbundle.ErrIntegrity) || errors.Is(err, paperbundle.ErrInvalid) {
			return ParseBundle{}, false, nil
		}
		return ParseBundle{}, false, err
	}
	return b, true, nil
}

func verifyPublishedBundle(ctx context.Context, objects objstore.Store, b ParseBundle) error {
	m, err := paperbundle.New(objects).VerifyBundle(ctx, b.PaperID, b.SourceID, b.RevisionID)
	if err != nil {
		return err
	}
	middle, _ := m.Member(m.MiddlePath)
	if m.SourcePDFSHA256 != b.SourcePDFSHA256 || m.MiddlePath != b.MiddlePath || m.MarkdownPath != b.MarkdownPath ||
		middle.SHA256 != b.ArtifactSha256 || b.ObjstoreKey != paperbundle.FileKey(b.PaperID, b.SourceID, b.RevisionID, m.MiddlePath) ||
		b.ManifestKey != paperbundle.ManifestKey(b.PaperID, b.SourceID, b.RevisionID) {
		return paperbundle.ErrIntegrity
	}
	body, err := readContentObject(ctx, objects, b.ManifestKey)
	if err != nil {
		return err
	}
	if paperbundle.SHA256(body) != b.ManifestSHA256 {
		return paperbundle.ErrIntegrity
	}
	return nil
}

func scanParseBundle(row scanner) (ParseBundle, error) {
	var b ParseBundle
	if err := row.Scan(&b.RevisionID, &b.PaperID, &b.SourceID, &b.Schema, &b.SchemaVersion, &b.ArtifactSha256, &b.ObjstoreKey, &b.Tier,
		&b.CreatedAt, &b.IsCurrent, &b.ManifestKey, &b.SourcePDFSHA256, &b.MiddlePath, &b.MarkdownPath, &b.ManifestSHA256); err != nil {
		return b, fmt.Errorf("registry: scan parse bundle: %w", err)
	}
	return b, nil
}

func readContentObject(ctx context.Context, objects objstore.Store, key string) ([]byte, error) {
	if objects == nil {
		return nil, errors.New("registry: content storage unavailable")
	}
	r, _, err := objects.Get(ctx, key)
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
