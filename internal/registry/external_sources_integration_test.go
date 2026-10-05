package registry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func externalFixtureRef(url, hash string) ExternalSourceRef {
	return ExternalSourceRef{SourceURL: url, Title: "Externally Hosted Original", Authors: []string{"Fixture Author"}, Year: 2026,
		Source: PaperSource{Sha256: hash, ObjstoreKey: "pdf/external-sources/" + hash + ".pdf", SizeBytes: 123,
			RetrievedURL: url, RetrievedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)}}
}

func TestIntegrationExternalSourceRegistration(t *testing.T) {
	pool, ctx := testPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool)
	url := "https://example.org/" + newPaperID() + ".pdf"
	ref := externalFixtureRef(url, strings.Repeat("ab", 32))
	registered, err := s.RegisterExternalSource(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE paper_id=$1`, registered.PaperID)
	})
	if !registered.Created || !strings.HasPrefix(registered.PaperID, "qa_") {
		t.Fatalf("first=%+v", registered)
	}
	paper, found, err := s.Get(ctx, registered.PaperID)
	if err != nil || !found || paper.PaperRef != "source_url:"+url || paper.ExternalID != paper.PaperRef || paper.DOI != "" || paper.ArxivID != "" || paper.OpenAlexID != "" || paper.TitleHash != "" {
		t.Fatalf("paper=%+v found=%v err=%v", paper, found, err)
	}
	if id, found, err := s.GetPaperIDByIdentity(ctx, KindSourceURL, url); err != nil || !found || id != registered.PaperID {
		t.Fatalf("lookup=%s %v %v", id, found, err)
	}
	if _, found, err := s.LookupByIdentity(ctx, TitleKey(TitleHash(ref.Title, ref.Authors, ref.Year))); err != nil || found {
		t.Fatalf("weak title identity inserted: %v %v", found, err)
	}
	retry, err := s.RegisterExternalSource(ctx, ref)
	if err != nil || retry.Created || retry.PaperID != registered.PaperID || retry.Source.SourceID != registered.Source.SourceID || !retry.Source.RetrievedAt.Equal(registered.Source.RetrievedAt) {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	ref.Source.Sha256 = strings.Repeat("cd", 32)
	ref.Source.ObjstoreKey = "pdf/external-sources/" + ref.Source.Sha256 + ".pdf"
	revision, err := s.RegisterExternalSource(ctx, ref)
	if err != nil || revision.Created || revision.PaperID != registered.PaperID || revision.Source.SourceID == registered.Source.SourceID {
		t.Fatalf("revision=%+v err=%v", revision, err)
	}
	sources, err := s.ListPaperSources(ctx, registered.PaperID)
	if err != nil || len(sources) != 2 || sources[0].Sha256 != strings.Repeat("ab", 32) || sources[0].SourceURL != url || sources[0].RetrievedURL != url {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	pinned, found, err := s.GetPaperSource(ctx, registered.PaperID, registered.Source.SourceID)
	if err != nil || !found || pinned.Sha256 != registered.Source.Sha256 || pinned.ObjstoreKey != registered.Source.ObjstoreKey {
		t.Fatalf("original overwritten: %+v %v %v", pinned, found, err)
	}
	ref.SourceURL += "?another-work=1"
	other, err := s.RegisterExternalSource(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE paper_id=$1`, other.PaperID) })
	if !other.Created || other.PaperID == registered.PaperID {
		t.Fatalf("equal titles merged different URLs: %+v", other)
	}
	if _, _, err := s.ResolveOrMint(ctx, PaperRef{Title: ref.Title, Authors: ref.Authors, Year: ref.Year}); !errors.Is(err, ErrTitleOnlyRef) {
		t.Fatalf("title-only changed: %v", err)
	}
	// Existing authoritative DOI/title behavior must not start matching this
	// external paper via a weak title key.
	doiPaper, created, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.9999/" + registered.PaperID, Title: ref.Title, Authors: ref.Authors, Year: ref.Year})
	if err != nil || !created || doiPaper == registered.PaperID || doiPaper == other.PaperID {
		t.Fatalf("DOI merged external by title: %s %v %v", doiPaper, created, err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE paper_id=$1`, doiPaper) })
}

func TestIntegrationExternalSourceConcurrentEprint(t *testing.T) {
	pool, ctx := testPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool)
	// Test-only work number is kept out of ordinary production registration.
	ref := externalFixtureRef("https://eprint.iacr.org/2099/910261", strings.Repeat("ef", 32))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE external_id='eprint:2099/910261'`)
	})
	var wg sync.WaitGroup
	results := make(chan ExternalSourceRegistration, 12)
	errorsCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local := ref
			if i%2 == 1 {
				local.SourceURL += ".pdf"
			}
			r, err := s.RegisterExternalSource(ctx, local)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- r
		}(i)
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	var paperID, sourceID string
	minted := 0
	for r := range results {
		if paperID == "" {
			paperID, sourceID = r.PaperID, r.Source.SourceID
		}
		if r.PaperID != paperID || r.Source.SourceID != sourceID || r.ExternalID != "eprint:2099/910261" {
			t.Fatalf("race forked identities: %+v", r)
		}
		if r.Created {
			minted++
		}
	}
	if minted != 1 {
		t.Fatalf("minted=%d want1", minted)
	}
	if id, found, err := s.GetPaperIDByIdentity(ctx, KindEprint, "2099/910261"); err != nil || !found || id != paperID {
		t.Fatalf("eprint lookup=%s %v %v", id, found, err)
	}
}

// Apply 00010 over actual 00009 rows in a private schema; old source SHA/id and
// DOI/arXiv/OpenAlex priority must survive the generated-column replacement.
func TestIntegrationExternalSourceMigrationUpgrade(t *testing.T) {
	pool, ctx := testPool(t)
	schema := "external_upgrade_" + strings.TrimPrefix(newPaperID(), "qa_")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	cfg := pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	provider, err := newProvider(isolated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.Exec(ctx, `INSERT INTO papers(paper_id,doi,arxiv_id,openalex_id,title) VALUES('qa_upgrade','10.9999/upgrade','2601.91026','W91026','Old title');
		INSERT INTO paper_sources(source_id,paper_id,origin,sha256,objstore_key,size_bytes)
		VALUES('src_upgrade','qa_upgrade','upload',repeat('a',64),'pdf/legacy.pdf',123)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	store := NewStore(isolated)
	paper, found, err := store.Get(ctx, "qa_upgrade")
	if err != nil || !found || paper.PaperRef != "openalex:W91026" {
		t.Fatalf("old priority changed: %+v %v %v", paper, found, err)
	}
	src, found, err := store.GetPaperSource(ctx, "qa_upgrade", "src_upgrade")
	if err != nil || !found || src.Sha256 != strings.Repeat("a", 64) || src.ObjstoreKey != "pdf/legacy.pdf" || !src.RetrievedAt.IsZero() || src.SourceURL != "" {
		t.Fatalf("old immutable source changed: %+v %v %v", src, found, err)
	}
	if _, err := store.RegisterExternalSource(ctx, externalFixtureRef("https://example.org/new.pdf", strings.Repeat("cd", 32))); err != nil {
		t.Fatal(err)
	}
	// Force the source insert (after paper + identity) to fail, and prove
	// the whole registration is rolled back rather than leaving a paper.
	if _, err := isolated.Exec(ctx, `ALTER TABLE paper_sources ADD CONSTRAINT external_fixture_size CHECK(size_bytes > 2)`); err != nil {
		t.Fatal(err)
	}
	failed := externalFixtureRef("https://example.org/transaction-failure.pdf", strings.Repeat("ef", 32))
	failed.Source.SizeBytes = 1
	if _, err := store.RegisterExternalSource(ctx, failed); err == nil {
		t.Fatal("source constraint failure unexpectedly accepted")
	}
	var rows int
	if err := isolated.QueryRow(ctx, `SELECT count(*) FROM papers WHERE external_id=$1`, "source_url:"+failed.SourceURL).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("transaction left paper: rows=%d err=%v", rows, err)
	}
	if _, found, err := store.LookupByIdentity(ctx, "source_url:"+failed.SourceURL); err != nil || found {
		t.Fatalf("transaction left identity: found=%v err=%v", found, err)
	}
	if _, err := provider.DownTo(ctx, 9); err == nil {
		t.Fatal("destructive downgrade accepted existing external-only work")
	}
	// A failed down migration must roll back every DDL change.
	if paper, found, err := store.Get(ctx, "qa_upgrade"); err != nil || !found || paper.PaperRef != "openalex:W91026" {
		t.Fatalf("failed downgrade left partial schema: %+v %v %v", paper, found, err)
	}
}
