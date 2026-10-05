package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func contentObjects(t *testing.T) objstore.Store {
	t.Helper()
	s, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func contentPut(t *testing.T, s objstore.Store, key string, b []byte) {
	t.Helper()
	if _, err := s.Put(context.Background(), key, bytes.NewReader(b), int64(len(b)), ""); err != nil {
		t.Fatal(err)
	}
}
func contentSource(paperID, sourceID, key string, pdf []byte) PaperSource {
	return PaperSource{PaperID: paperID, SourceID: sourceID, Origin: "upload", Sha256: paperbundle.SHA256(pdf), ObjstoreKey: key, SizeBytes: int64(len(pdf)), CreatedAt: time.Now()}
}

func TestFrozenImportPreservesSourceAndIgnoresChangedLegacy(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	pdf := []byte("%PDF-1.7\nexisting comment source\n")
	key := "pdf/legacy.pdf"
	src := contentSource("qa_test", "src_existing", key, pdf)
	db.sources[src.SourceID] = src
	contentPut(t, objects, key, pdf)
	tx := db.begin()
	got, err := registerFrozenPaperSource(ctx, tx, objects, src.PaperID, "arxiv:v1", key)
	tx.finish()
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceID != src.SourceID || got.Sha256 != src.Sha256 || got.Origin != src.Origin || got.ObjstoreKey != paperbundle.PDFKey(src.PaperID, src.SourceID) {
		t.Fatalf("identity changed: %+v", got)
	}
	if len(db.sources) != 1 {
		t.Fatalf("source re-minted: %v", db.sources)
	}
	// A previous location is never used again, even when it has other bytes.
	contentPut(t, objects, key, []byte("%PDF-tampered"))
	tx = db.begin()
	again, err := registerFrozenPaperSource(ctx, tx, objects, src.PaperID, "arxiv:v1", key)
	tx.finish()
	if err != nil || again.SourceID != src.SourceID {
		t.Fatalf("changed legacy was consulted: %+v %v", again, err)
	}
	if err := objects.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	tx = db.begin()
	_, err = registerFrozenPaperSource(ctx, tx, objects, src.PaperID, "arxiv:v1", key)
	tx.finish()
	if err != nil {
		t.Fatalf("missing legacy affected frozen source: %v", err)
	}
	// Restore a good legacy PDF, remove frozen bytes: still MUST fail closed.
	contentPut(t, objects, key, pdf)
	if err := objects.Delete(ctx, got.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	tx = db.begin()
	_, err = registerFrozenPaperSource(ctx, tx, objects, src.PaperID, "arxiv:v1", key)
	tx.finish()
	if !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("missing frozen fell back: %v", err)
	}
	if _, exists, _ := objects.Stat(ctx, got.ObjstoreKey); exists {
		t.Fatal("legacy repaired missing frozen object")
	}
}

func TestConcurrentLazyRegistrationReusesSHAAndAlias(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	pdf := []byte("%PDF-1.7\nconcurrent PDF\n")
	for _, key := range []string{"pdf/a.pdf", "pdf/b.pdf"} {
		contentPut(t, objects, key, pdf)
	}
	const n = 20
	var wg sync.WaitGroup
	results := make(chan PaperSource, n)
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx := db.begin()
			defer tx.finish()
			key := "pdf/a.pdf"
			if i%2 == 1 {
				key = "pdf/b.pdf"
			}
			src, err := registerFrozenPaperSource(ctx, tx, objects, "qa_test", "arxiv:v1", key)
			results <- src
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for src := range results {
		if id == "" {
			id = src.SourceID
		}
		if src.SourceID != id {
			t.Fatalf("concurrent IDs %s != %s", src.SourceID, id)
		}
	}
	if len(db.sources) != 1 || len(db.imports) != 2 {
		t.Fatalf("sources=%d aliases=%d", len(db.sources), len(db.imports))
	}
	if _, exists, _ := objects.Stat(ctx, "markdown/old.md"); exists {
		t.Fatal("old parser output copied")
	}
}

func TestFreshPDFReuseAndAliasBindingNeverWritesLegacy(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	pdf := []byte("%PDF-1.7\nfresh fetched bytes\n")
	// Same-SHA source with an unavailable legacy location can move safely
	// from verified supplied bytes without changing comment source IDs.
	existing := contentSource("qa_test", "src_existing", "pdf/missing.pdf", pdf)
	db.sources[existing.SourceID] = existing
	tx := db.begin()
	src, err := registerFrozenPDF(ctx, tx, objects, existing.PaperID, "arxiv:v3", pdf)
	tx.finish()
	if err != nil || src.SourceID != existing.SourceID {
		t.Fatalf("reuse=%+v %v", src, err)
	}
	tx = db.begin()
	bound, err := bindPaperSourceImport(ctx, tx, objects, src.PaperID, src.SourceID, "pdf/arxiv-v3.pdf")
	tx.finish()
	if err != nil || bound.SourceID != src.SourceID {
		t.Fatalf("bind=%+v %v", bound, err)
	}
	if _, exists, _ := objects.Stat(ctx, "pdf/arxiv-v3.pdf"); exists {
		t.Fatal("fresh path wrote legacy PDF")
	}
	// Once frozen, even a same-SHA upload cannot silently reconstitute missing
	// immutable content from mutable input. Explicit operator repair required.
	if err := objects.Delete(ctx, src.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	tx = db.begin()
	_, err = registerFrozenPDF(ctx, tx, objects, existing.PaperID, "upload", pdf)
	tx.finish()
	if !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("fresh bytes repaired frozen identity: %v", err)
	}
}

func TestFreezeMismatchNeverMovesRegistryLocation(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	pdf := []byte("%PDF-original")
	src := contentSource("qa_test", "src_existing", "pdf/original.pdf", pdf)
	db.sources[src.SourceID] = src
	contentPut(t, objects, src.ObjstoreKey, []byte("%PDF-different"))
	tx := db.begin()
	_, err := registerFrozenPaperSource(ctx, tx, objects, src.PaperID, "upload", src.ObjstoreKey)
	tx.finish()
	if !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("mismatch=%v", err)
	}
	if db.sources[src.SourceID].ObjstoreKey != src.ObjstoreKey || len(db.imports) != 0 {
		t.Fatal("failed freeze modified registry identity")
	}
	if _, exists, _ := objects.Stat(ctx, paperbundle.PDFKey(src.PaperID, src.SourceID)); exists {
		t.Fatal("bad source was frozen")
	}
}

func readyContentFixture(t *testing.T, objects objstore.Store, db *memoryContentDB, revID string) (ParseBundle, paperbundle.Input) {
	t.Helper()
	ctx := context.Background()
	pdf := []byte("%PDF-1.7\nbundle source\n")
	src := contentSource("qa_test", "src_test", paperbundle.PDFKey("qa_test", "src_test"), pdf)
	db.sources[src.SourceID] = src
	if _, err := paperbundle.New(objects).FreezePDF(ctx, src.PaperID, src.SourceID, pdf, src.Sha256); err != nil {
		t.Fatal(err)
	}
	in := paperbundle.Input{PaperID: src.PaperID, SourceID: src.SourceID, RevisionID: revID, SourcePDFSHA256: src.Sha256,
		MiddlePath: "paper/a_middle.json", MarkdownPath: "paper/a.md", Files: map[string][]byte{
			"paper/a_middle.json": []byte(" {\"schema\":\"docvortex.middle\",\"schema_version\":\"2.0\",\"pdf_info\":{\"pages\":1},\"blocks\":[]}\r\n"), "paper/a.md": []byte("# Original\n"), "unknown/vendor.bin": {1, 2, 3},
		}}
	if _, err := paperbundle.New(objects).WriteBundle(ctx, in); err != nil {
		t.Fatal(err)
	}
	b, err := prepareBundlePublication(ctx, objects, ParseRevision{PaperID: src.PaperID, SourceID: src.SourceID, RevisionID: revID, Schema: DocVortexMiddleSchema, SchemaVersion: DocVortexMiddleSchemaVersion, Tier: "STANDARD"})
	if err != nil {
		t.Fatal(err)
	}
	return b, in
}

func TestBundlePublicationVerifiesBeforeCurrentAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	b, _ := readyContentFixture(t, objects, db, "pr_ready")
	old := ParseRevision{RevisionID: "pr_old", PaperID: b.PaperID, SourceID: b.SourceID, Schema: "legacy", ObjstoreKey: "json/old.json", IsCurrent: true}
	db.revs[old.RevisionID] = old
	for range 2 {
		tx := db.begin()
		err := publishBundle(ctx, tx, objects, b, true)
		tx.finish()
		if err != nil {
			t.Fatal(err)
		}
	}
	if !db.revs[b.RevisionID].IsCurrent || db.revs[old.RevisionID].IsCurrent {
		t.Fatalf("current flip failed: %v", db.revs)
	}
	if db.revs[old.RevisionID].ObjstoreKey != old.ObjstoreKey {
		t.Fatal("old comment revision mutated")
	}
	if len(db.bundles) != 1 {
		t.Fatal("idempotent bundle publication duplicated row")
	}
	bundleEvent, currentEvent := -1, -1
	for i, e := range db.events {
		if e == "bundle-insert" && bundleEvent < 0 {
			bundleEvent = i
		}
		if e == "current-true" && currentEvent < 0 {
			currentEvent = i
		}
	}
	if bundleEvent < 0 || currentEvent <= bundleEvent {
		t.Fatalf("pointer published before complete bundle: %v", db.events)
	}
	if err := verifyPublishedBundle(ctx, objects, b); err != nil {
		t.Fatal(err)
	}
	b.ManifestSHA256 = strings.Repeat("f", 64)
	if err := verifyPublishedBundle(ctx, objects, b); !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("PG manifest hash not enforced: %v", err)
	}
}

func TestPublicationRejectsMissingMembersAndConflictingIdentity(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	b, in := readyContentFixture(t, objects, db, "pr_ready")
	if err := objects.Delete(ctx, paperbundle.FileKey(in.PaperID, in.SourceID, in.RevisionID, "unknown/vendor.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBundlePublication(ctx, objects, b.ParseRevision); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("partial persisted bundle accepted %v", err)
	}
	if len(db.revs) != 0 || len(db.bundles) != 0 {
		t.Fatal("partial publication reached DB")
	}
	// Restore original full bytes under a NEW revision (not copy of old output).
	objects = contentObjects(t)
	db = newMemoryContentDB()
	b, _ = readyContentFixture(t, objects, db, "pr_ready")
	conflict := b.ParseRevision
	conflict.PaperID = "qa_other"
	db.revs[b.RevisionID] = conflict
	tx := db.begin()
	err := publishBundle(ctx, tx, objects, b, true)
	tx.finish()
	if !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("cross-paper id conflict accepted %v", err)
	}
	if len(db.bundles) != 0 {
		t.Fatal("conflicting revision published bundle")
	}
	wrong := b.ParseRevision
	wrong.ArtifactSha256 = strings.Repeat("a", 64)
	if _, err := prepareBundlePublication(ctx, objects, wrong); !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("wrong middle SHA accepted %v", err)
	}
}

func TestFrozenSourceRootRelocationPreservesProvenanceAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	pdf := []byte("%PDF-same immutable source")
	src := contentSource("qa_survivor", "src_stable", paperbundle.PDFKey("qa_old", "src_stable"), pdf)
	src.SourceURL = "https://source.example/paper.pdf"
	src.RetrievedURL = "https://cdn.example/paper.pdf"
	src.RetrievedAt = time.Now().UTC()
	db.sources[src.SourceID] = src
	if _, err := paperbundle.New(objects).FreezePDF(ctx, "qa_old", src.SourceID, pdf, src.Sha256); err != nil {
		t.Fatal(err)
	}
	tx := db.begin()
	got, err := freezePaperSource(ctx, tx, objects, src)
	tx.finish()
	if err != nil || got.ObjstoreKey != paperbundle.PDFKey(src.PaperID, src.SourceID) || got.SourceID != src.SourceID || got.SourceURL != src.SourceURL || got.RetrievedURL != src.RetrievedURL || !got.RetrievedAt.Equal(src.RetrievedAt) {
		t.Fatalf("relocation rewrote identity/provenance: %+v %v", got, err)
	}
	// A row still referring to a missing old frozen root cannot be restored
	// from newly supplied or mutable legacy bytes, even with the right SHA.
	db.sources[src.SourceID] = src
	if err := objects.Delete(ctx, src.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	tx = db.begin()
	_, err = freezeSourceBytes(ctx, tx, objects, src, pdf)
	tx.finish()
	if !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("missing old frozen root repaired from input: %v", err)
	}
}

func TestContentCatalogUnavailable(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)
	if _, err := s.RegisterFrozenPDF(ctx, nil, "qa", "upload", []byte("pdf")); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatal(err)
	}
	if _, _, err := s.GetImportedPaperSource(ctx, "qa", "pdf/a.pdf"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatal(err)
	}
	if _, _, err := s.GetReadyParseBundle(ctx, nil, "qa", "src"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatal(err)
	}
	if _, err := s.PublishBundle(ctx, nil, ParseRevision{}, true); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatal(err)
	}
}

// Fake catalog with transaction-duration locks. It deliberately has no real
// PostgreSQL/network dependency and exercises the same helper used in production.
type memoryContentDB struct {
	mu      sync.Mutex
	sources map[string]PaperSource
	imports map[string]string
	revs    map[string]ParseRevision
	bundles map[string]ParseBundle
	events  []string
}

func newMemoryContentDB() *memoryContentDB {
	return &memoryContentDB{sources: map[string]PaperSource{}, imports: map[string]string{}, revs: map[string]ParseRevision{}, bundles: map[string]ParseBundle{}}
}

type memoryContentTx struct {
	db     *memoryContentDB
	locked bool
}

func (db *memoryContentDB) begin() *memoryContentTx { return &memoryContentTx{db: db} }
func (tx *memoryContentTx) finish() {
	if tx.locked {
		tx.locked = false
		tx.db.mu.Unlock()
	}
}
func (tx *memoryContentTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	db := tx.db
	switch {
	case strings.Contains(sql, "pg_advisory_xact_lock"):
		if !tx.locked {
			db.mu.Lock()
			tx.locked = true
		}
		return pgconn.NewCommandTag("SELECT 1"), nil
	case strings.Contains(sql, "INSERT INTO paper_sources"):
		s := PaperSource{SourceID: args[0].(string), PaperID: args[1].(string), Origin: args[2].(string), Sha256: args[3].(string), ObjstoreKey: args[4].(string), SizeBytes: args[5].(int64), CreatedAt: time.Now()}
		if len(args) >= 9 {
			s.SourceURL = args[6].(string)
			s.RetrievedURL = args[7].(string)
			s.RetrievedAt = args[8].(time.Time)
		}
		if _, ok := db.sources[s.SourceID]; ok {
			return pgconn.CommandTag{}, errors.New("duplicate source ID")
		}
		db.sources[s.SourceID] = s
	case strings.Contains(sql, "UPDATE paper_sources"):
		s, ok := db.sources[args[2].(string)]
		if !ok || s.PaperID != args[1] || s.Sha256 != args[3] || s.SizeBytes != args[4] || s.ObjstoreKey != args[5] {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		s.ObjstoreKey = args[0].(string)
		db.sources[s.SourceID] = s
	case strings.Contains(sql, "INSERT INTO paper_source_imports"):
		key := args[0].(string) + "\n" + args[1].(string)
		if _, ok := db.imports[key]; !ok {
			db.imports[key] = args[2].(string)
		}
	case strings.Contains(sql, "INSERT INTO parse_revisions"):
		id := args[0].(string)
		if _, ok := db.revs[id]; !ok {
			db.revs[id] = ParseRevision{RevisionID: id, PaperID: args[1].(string), SourceID: args[2].(string), Schema: args[3].(string), SchemaVersion: args[4].(string), ArtifactSha256: args[5].(string), ObjstoreKey: args[6].(string), Tier: args[7].(string), CreatedAt: time.Now()}
		}
	case strings.Contains(sql, "INSERT INTO parse_bundles"):
		id := args[0].(string)
		db.events = append(db.events, "bundle-insert")
		if _, ok := db.bundles[id]; !ok {
			db.bundles[id] = ParseBundle{ParseRevision: db.revs[id], SourcePDFSHA256: args[3].(string), ManifestKey: args[4].(string), ManifestSHA256: args[5].(string), MiddlePath: args[6].(string), MarkdownPath: args[7].(string)}
		}
	case strings.Contains(sql, "UPDATE parse_revisions SET is_current=FALSE"):
		for id, r := range db.revs {
			if r.PaperID == args[0] && id != args[1] {
				r.IsCurrent = false
				db.revs[id] = r
			}
		}
		db.events = append(db.events, "current-false")
	case strings.Contains(sql, "UPDATE parse_revisions SET is_current=TRUE"):
		r := db.revs[args[1].(string)]
		r.IsCurrent = true
		db.revs[r.RevisionID] = r
		db.events = append(db.events, "current-true")
	default:
		return pgconn.CommandTag{}, fmt.Errorf("unexpected SQL Exec %s", sql)
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (tx *memoryContentTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	db := tx.db
	compact := strings.Join(strings.Fields(sql), "")
	switch {
	case strings.Contains(sql, "FROM paper_source_imports"):
		id, ok := db.imports[args[0].(string)+"\n"+args[1].(string)]
		if !ok {
			return memoryRow{err: pgx.ErrNoRows}
		}
		return sourceMemoryRow(db.sources[id])
	case strings.Contains(sql, "FROM paper_sources"):
		var matches []PaperSource
		for _, s := range db.sources {
			if s.PaperID != args[0] {
				continue
			}
			if strings.Contains(compact, "sha256=$2") && s.Sha256 == args[1] || strings.Contains(compact, "objstore_key=$2") && s.ObjstoreKey == args[1] || strings.Contains(compact, "source_id=$2") && s.SourceID == args[1] {
				matches = append(matches, s)
			}
		}
		if len(matches) == 0 {
			return memoryRow{err: pgx.ErrNoRows}
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
				return matches[i].SourceID < matches[j].SourceID
			}
			return matches[i].CreatedAt.Before(matches[j].CreatedAt)
		})
		return sourceMemoryRow(matches[0])
	case strings.Contains(sql, "JOIN parse_bundles"):
		id := args[0].(string)
		if len(args) == 2 {
			id = args[1].(string)
		}
		b, ok := db.bundles[id]
		if !ok {
			return memoryRow{err: pgx.ErrNoRows}
		}
		b.ParseRevision = db.revs[id]
		return bundleMemoryRow(b)
	case strings.Contains(sql, "FROM parse_revisions"):
		r, ok := db.revs[args[0].(string)]
		if !ok {
			return memoryRow{err: pgx.ErrNoRows}
		}
		return revisionMemoryRow(r)
	default:
		return memoryRow{err: fmt.Errorf("unexpected SQL QueryRow %s", sql)}
	}
}

type memoryRow struct {
	values []any
	err    error
}

func (r memoryRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan %d destinations != %d", len(dest), len(r.values))
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(r.values[i]))
	}
	return nil
}
func sourceMemoryRow(s PaperSource) memoryRow {
	var retrievedAt *time.Time
	if !s.RetrievedAt.IsZero() {
		at := s.RetrievedAt
		retrievedAt = &at
	}
	return memoryRow{values: []any{s.SourceID, s.PaperID, s.Origin, s.Sha256, s.ObjstoreKey, s.SizeBytes, s.CreatedAt, s.SourceURL, s.RetrievedURL, retrievedAt}}
}
func revisionValues(r ParseRevision) []any {
	return []any{r.RevisionID, r.PaperID, r.SourceID, r.Schema, r.SchemaVersion, r.ArtifactSha256, r.ObjstoreKey, r.Tier, r.CreatedAt, r.IsCurrent}
}
func revisionMemoryRow(r ParseRevision) memoryRow { return memoryRow{values: revisionValues(r)} }
func bundleMemoryRow(b ParseBundle) memoryRow {
	return memoryRow{values: append(revisionValues(b.ParseRevision), b.ManifestKey, b.SourcePDFSHA256, b.MiddlePath, b.MarkdownPath, b.ManifestSHA256)}
}
