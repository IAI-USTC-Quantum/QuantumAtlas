package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Genuine self-contained PDF fixture; no browser, publisher, PostgreSQL or S3.
func externalPDF() []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>"}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, o := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, n := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", n)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
func externalRef(url string, pdf []byte) ExternalSourceRef {
	return ExternalSourceRef{SourceURL: url, Title: " A verified external work ", Authors: []string{"Alice", "Bob"}, Year: 2024, Source: PaperSource{Sha256: paperbundle.SHA256(pdf), SizeBytes: int64(len(pdf)), RetrievedURL: "https://cdn.example/first.pdf", RetrievedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
}
func registerExternalFixture(t *testing.T, db *externalContentDB, store objstore.Store, ref ExternalSourceRef, pdf []byte) (ExternalSourceRegistration, error) {
	t.Helper()
	ref, url, id, kind, err := validateFrozenExternalSource(ref, pdf)
	if err != nil {
		return ExternalSourceRegistration{}, err
	}
	tx := db.begin()
	defer tx.finish()
	return registerFrozenExternalSource(context.Background(), tx, store, ref, pdf, url, id, kind)
}
func TestFrozenExternalRegistrationPreservesBytesIdentityAndFirstProvenance(t *testing.T) {
	pdf := externalPDF()
	store := contentObjects(t)
	db := newExternalContentDB()
	ref := externalRef("https://eprint.iacr.org/2024/123", pdf)
	first, err := registerExternalFixture(t, db, store, ref, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.ExternalID != "eprint:2024/123" || first.Source.ObjstoreKey != paperbundle.PDFKey(first.PaperID, first.Source.SourceID) {
		t.Fatalf("registration=%+v", first)
	}
	if first.Source.SourceURL != ref.SourceURL || first.Source.RetrievedURL != ref.Source.RetrievedURL || !first.Source.RetrievedAt.Equal(ref.Source.RetrievedAt) {
		t.Fatalf("provenance=%+v", first.Source)
	}
	r, _, err := store.Get(context.Background(), first.Source.ObjstoreKey)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(body, pdf) {
		t.Fatal("original PDF bytes changed")
	}
	if files, _ := store.ListPrefix(context.Background(), "pdf/", 0); len(files) != 0 {
		t.Fatalf("legacy staging written %v", files)
	}
	retry := ref
	retry.SourceURL = "https://eprint.iacr.org/2024/123.pdf"
	retry.Source.RetrievedURL = "https://cdn.example/later.pdf"
	retry.Source.RetrievedAt = ref.Source.RetrievedAt.Add(time.Hour)
	again, err := registerExternalFixture(t, db, store, retry, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.PaperID != first.PaperID || again.Source.SourceID != first.Source.SourceID || again.Source.SourceURL != first.Source.SourceURL || again.Source.RetrievedURL != first.Source.RetrievedURL || !again.Source.RetrievedAt.Equal(first.Source.RetrievedAt) {
		t.Fatalf("retry rewrote identity/provenance %+v", again)
	}
	// Same title/bytes on an unrelated URL must not merge works.
	other, err := registerExternalFixture(t, db, store, externalRef("https://other.example/article.pdf", pdf), pdf)
	if err != nil {
		t.Fatal(err)
	}
	if !other.Created || other.PaperID == first.PaperID {
		t.Fatal("title/bytes used to deduplicate distinct external works")
	}
	if p := db.papers[first.PaperID]; p.Title != "A verified external work" || p.Year != 2024 || len(p.Authors) != 2 {
		t.Fatalf("bibliographic metadata changed %+v", p)
	}
}
func TestFrozenExternalReuseMovesOnlyLocationAndNeverRestoresFrozenMissing(t *testing.T) {
	pdf := externalPDF()
	store := contentObjects(t)
	db := newExternalContentDB()
	ref := externalRef("https://eprint.iacr.org/2024/321", pdf)
	id := "eprint:2024/321"
	paper := "qa_historical"
	db.identities[id] = paper
	old := contentSource(paper, "src_historical", "pdf/external-sources/"+paperbundle.SHA256(pdf)+".pdf", pdf)
	old.SourceURL = "https://eprint.iacr.org/2024/321.pdf"
	old.RetrievedURL = "https://archive.example/original.pdf"
	old.RetrievedAt = ref.Source.RetrievedAt.Add(-time.Hour)
	db.memory.sources[old.SourceID] = old
	got, err := registerExternalFixture(t, db, store, ref, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source.SourceID != old.SourceID || got.Source.Origin != old.Origin || got.Source.SourceURL != old.SourceURL || got.Source.RetrievedURL != old.RetrievedURL || !got.Source.RetrievedAt.Equal(old.RetrievedAt) {
		t.Fatalf("legacy history changed %+v", got.Source)
	}
	if err := store.Delete(context.Background(), got.Source.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	contentPut(t, store, old.ObjstoreKey, pdf)
	if _, err := registerExternalFixture(t, db, store, ref, pdf); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("frozen missing repaired from fresh/legacy input: %v", err)
	}
	if _, exists, _ := store.Stat(context.Background(), got.Source.ObjstoreKey); exists {
		t.Fatal("frozen source restored")
	}
}
func TestFrozenExternalValidationRejectsUnownedOrStagedMetadata(t *testing.T) {
	pdf := externalPDF()
	base := externalRef("https://example.com/paper.pdf", pdf)
	cases := []ExternalSourceRef{base, base, base, base, base, base, base}
	cases[0].Source.ObjstoreKey = "pdf/external-sources/" + base.Source.Sha256 + ".pdf"
	cases[1].Source.Sha256 = strings.Repeat("0", 64)
	cases[2].Source.SizeBytes++
	cases[3].Source.SourceID = "src_client"
	cases[4].Title = " "
	cases[5].Year = 0
	cases[6].Source.RetrievedURL = "http://unsafe.example/pdf"
	for i, ref := range cases {
		if _, _, _, _, err := validateFrozenExternalSource(ref, pdf); err == nil {
			t.Fatalf("invalid declared metadata %d accepted", i)
		}
	}
	nonPDF := []byte("not a PDF")
	ref := externalRef("https://example.com/paper.pdf", nonPDF)
	if _, _, _, _, err := validateFrozenExternalSource(ref, nonPDF); !errors.Is(err, ErrExternalSourceInvalid) {
		t.Fatal(err)
	}
}

type externalPaperFixture struct {
	Title   string
	Authors []string
	Year    int
}
type externalContentDB struct {
	memory     *memoryContentDB
	identities map[string]string
	external   map[string]string
	papers     map[string]externalPaperFixture
}

func newExternalContentDB() *externalContentDB {
	return &externalContentDB{memory: newMemoryContentDB(), identities: map[string]string{}, external: map[string]string{}, papers: map[string]externalPaperFixture{}}
}

type externalContentTx struct {
	*memoryContentTx
	external *externalContentDB
}

func (db *externalContentDB) begin() *externalContentTx {
	return &externalContentTx{memoryContentTx: db.memory.begin(), external: db}
}
func (tx *externalContentTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	compact := strings.Join(strings.Fields(sql), "")
	switch {
	case strings.Contains(sql, "INSERT INTO papers"):
		tx.external.external[args[1].(string)] = args[0].(string)
		tx.external.papers[args[0].(string)] = externalPaperFixture{Title: args[2].(string), Authors: args[3].([]string), Year: args[4].(int)}
		return pgconn.NewCommandTag("INSERT 1"), nil
	case strings.Contains(sql, "INSERT INTO paper_identities"):
		if _, found := tx.external.identities[args[0].(string)]; !found {
			tx.external.identities[args[0].(string)] = args[1].(string)
		}
		return pgconn.NewCommandTag("INSERT 1"), nil
	case strings.Contains(compact, "UPDATEpaper_sourcesSETsource_url="):
		src, found := tx.db.sources[args[4].(string)]
		if !found || src.PaperID != args[3] || src.Sha256 != args[5] {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		if src.SourceURL == "" {
			src.SourceURL = args[0].(string)
		}
		if src.RetrievedURL == "" {
			src.RetrievedURL = args[1].(string)
		}
		if src.RetrievedAt.IsZero() {
			src.RetrievedAt = args[2].(time.Time)
		}
		tx.db.sources[src.SourceID] = src
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return tx.memoryContentTx.Exec(ctx, sql, args...)
}
func (tx *externalContentTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "FROM paper_identities") {
		if id, ok := tx.external.identities[args[0].(string)]; ok {
			return memoryRow{values: []any{id}}
		}
		return memoryRow{err: pgx.ErrNoRows}
	}
	if strings.Contains(sql, "FROM papers WHERE external_id") {
		if id, ok := tx.external.external[args[0].(string)]; ok {
			return memoryRow{values: []any{id}}
		}
		return memoryRow{err: pgx.ErrNoRows}
	}
	return tx.memoryContentTx.QueryRow(ctx, sql, args...)
}
