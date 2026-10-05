package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

func TestSupportedParseProfilesAreHonestAndVersioned(t *testing.T) {
	for _, p := range [][2]string{{DocVortexMiddleSchema, DocVortexMiddleSchemaVersion}, {NativeMiddleSchema, NativeMiddleSchemaVersion}} {
		if !SupportedParseProfile(p[0], p[1]) {
			t.Fatal(p)
		}
	}
	for _, p := range [][2]string{{"", ""}, {"docvortex.middle", "3.4.4"}, {NativeMiddleSchema, "3.4.4"}, {NativeMiddleSchema, "2.0"}, {"mineru.middle", "1"}, {"content_list", "1"}} {
		if SupportedParseProfile(p[0], p[1]) {
			t.Fatalf("unsupported/producer version accepted: %v", p)
		}
		if err := requireParseProfile(p[0], p[1]); !errors.Is(err, ErrUnsupportedParseProfile) || !errors.Is(err, paperbundle.ErrIntegrity) {
			t.Fatal(err)
		}
	}
}

func TestNativeBundlePublicationPreservesAllOriginalMembersAndProfile(t *testing.T) {
	ctx := context.Background()
	objects := contentObjects(t)
	db := newMemoryContentDB()
	pdf := []byte("%PDF-native source exact bytes")
	src := contentSource("qa_native", "src_native", paperbundle.PDFKey("qa_native", "src_native"), pdf)
	db.sources[src.SourceID] = src
	if _, err := paperbundle.New(objects).FreezePDF(ctx, src.PaperID, src.SourceID, pdf, src.Sha256); err != nil {
		t.Fatal(err)
	}
	// Producer-native original: explicit indexes stay 1/2, page boxes stay
	// pixel/page units and profile fields are NOT injected into these bytes.
	original := []byte(" {\r\n \"_backend\":\"hybrid\",\"_effort\":\"medium\",\"_version_name\":\"3.4.4\",\"pdf_info\":[{\"page_idx\":0,\"page_size\":[595,842],\"para_blocks\":[{\"type\":\"text\",\"index\":1,\"bbox\":[40,50,550,80],\"lines\":[{\"spans\":[{\"type\":\"text\",\"content\":\"Original native text α\"}]}]},{\"type\":\"image\",\"index\":2,\"bbox\":[40,100,300,400],\"blocks\":[]}]}]}\r\n")
	files := map[string][]byte{"layout.json": original, "markdown.md": []byte("# Original native result\r\n")}
	for i := 0; i < 18; i++ {
		files[fmt.Sprintf("nested/unknown-%02d.meta.json", i)] = []byte(fmt.Sprintf(" original unknown bytes %d\x00", i))
	}
	in := paperbundle.Input{PaperID: src.PaperID, SourceID: src.SourceID, RevisionID: "pr_native", SourcePDFSHA256: src.Sha256, MiddlePath: "layout.json", MarkdownPath: "markdown.md", Files: files}
	if _, err := paperbundle.New(objects).WriteBundle(ctx, in); err != nil {
		t.Fatal(err)
	}
	old := ParseRevision{RevisionID: "pr_legacy_current", PaperID: src.PaperID, SourceID: src.SourceID, Schema: DocVortexMiddleSchema, SchemaVersion: DocVortexMiddleSchemaVersion, ObjstoreKey: "json/legacy.json", IsCurrent: true}
	db.revs[old.RevisionID] = old
	// Mislabeling genuine native output as DocVortex must never publish or flip.
	mislabel := ParseRevision{PaperID: src.PaperID, SourceID: src.SourceID, RevisionID: in.RevisionID, Schema: DocVortexMiddleSchema, SchemaVersion: DocVortexMiddleSchemaVersion}
	if _, err := prepareBundlePublication(ctx, objects, mislabel); !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("native mislabeled as DocVortex: %v", err)
	}
	if !db.revs[old.RevisionID].IsCurrent || len(db.revs) != 1 || len(db.bundles) != 0 {
		t.Fatal("failed profile validation published/flipped current")
	}
	actual := mislabel
	actual.Schema, actual.SchemaVersion = NativeMiddleSchema, NativeMiddleSchemaVersion
	b, err := prepareBundlePublication(ctx, objects, actual)
	if err != nil {
		t.Fatal(err)
	}
	if b.Schema != NativeMiddleSchema || b.SchemaVersion != NativeMiddleSchemaVersion || b.ArtifactSha256 != paperbundle.SHA256(original) || b.SourcePDFSHA256 != src.Sha256 {
		t.Fatalf("profile/source/hash changed %+v", b)
	}
	tx := db.begin()
	err = publishBundle(ctx, tx, objects, b, true)
	tx.finish()
	if err != nil {
		t.Fatal(err)
	}
	if !db.revs[b.RevisionID].IsCurrent || db.revs[old.RevisionID].IsCurrent || db.revs[old.RevisionID].ObjstoreKey != old.ObjstoreKey {
		t.Fatal("atomic current/old revision preservation failed")
	}
	if err := verifyPublishedBundle(ctx, objects, b); err != nil {
		t.Fatal(err)
	}
	if len(db.sources) != 1 || db.sources[src.SourceID].Sha256 != src.Sha256 {
		t.Fatal("source identity changed during native publication")
	}
	for name, want := range files {
		r, _, err := objects.Get(ctx, paperbundle.FileKey(in.PaperID, in.SourceID, in.RevisionID, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("member %q renamed/normalized: %v", name, err)
		}
	}
	wrong := b
	wrong.Schema, wrong.SchemaVersion = DocVortexMiddleSchema, DocVortexMiddleSchemaVersion
	if err := verifyPublishedBundle(ctx, objects, wrong); !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("ready native bundle mislabeled %v", err)
	}
	unknown := b
	unknown.SchemaVersion = "3.4.4"
	if err := verifyPublishedBundle(ctx, objects, unknown); !errors.Is(err, ErrUnsupportedParseProfile) {
		t.Fatalf("producer release treated as profile %v", err)
	}
}

func TestMiddleProfileEnvelopeRejectsContentListAndMixedSchema(t *testing.T) {
	validDoc := []byte(`{"schema":"docvortex.middle","schema_version":"2.0","pdf_info":{"pages":1},"blocks":[]}`)
	if err := verifyMiddleProfile(validDoc, DocVortexMiddleSchema, DocVortexMiddleSchemaVersion); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`[]`, `{"pdf_info":{}}`, `{"schema":"mineru.native.middle","pdf_info":[]}`, `{"schema":null,"pdf_info":[]}`, `{"pdf_info":[],"blocks":[]}`, `{"content_list":[]}`} {
		if err := verifyMiddleProfile([]byte(raw), NativeMiddleSchema, NativeMiddleSchemaVersion); !errors.Is(err, paperbundle.ErrIntegrity) {
			t.Fatalf("mixed/non-Middle accepted: %s %v", raw, err)
		}
	}
}
