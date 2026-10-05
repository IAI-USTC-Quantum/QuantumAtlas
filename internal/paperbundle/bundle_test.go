package paperbundle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

func localObjects(t *testing.T) objstore.Store {
	t.Helper()
	s, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func inputFixture(t *testing.T, objects objstore.Store) Input {
	t.Helper()
	pdf := []byte("%PDF-1.7\noriginal source bytes\n")
	if _, err := New(objects).FreezePDF(context.Background(), "qa_test", "src_test", pdf, SHA256(pdf)); err != nil {
		t.Fatal(err)
	}
	return Input{PaperID: "qa_test", SourceID: "src_test", RevisionID: "pr_test", SourcePDFSHA256: SHA256(pdf),
		MiddlePath: "nested/paper_middle.json", MarkdownPath: "nested/full.md", Files: map[string][]byte{
			"nested/paper_middle.json":    []byte(" {\n \"pdf_info\" : [] }\r\n"),
			"nested/full.md":              []byte("# Original\n![image](images/figure.png)\n"),
			"nested/images/figure.png":    {0x89, 'P', 'N', 'G', 0, 0xff},
			"nested/content_list.json":    []byte("[ {\"unknown\":true} ]\n"),
			"unknown..original.meta.json": []byte("{\"unchanged\": 1}\r\n"),
			"unknown..original":           []byte("sibling must not delete metadata-suffixed file"),
			"vendor/custom binary.data":   {0, 1, 2, 0xff},
			"empty.bin":                   {},
		}}
}

func TestWriteBundlePreservesFullOriginalInventory(t *testing.T) {
	ctx := context.Background()
	objects := localObjects(t)
	trace := &tracingStore{Store: objects}
	in := inputFixture(t, trace)
	trace.writes = nil
	m, err := New(trace).WriteBundle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != len(in.Files) {
		t.Fatalf("inventory %d != %d", len(m.Files), len(in.Files))
	}
	if got := trace.writes[len(trace.writes)-1]; got != ManifestKey(in.PaperID, in.SourceID, in.RevisionID) {
		t.Fatalf("last write = %q", got)
	}
	for name, want := range in.Files {
		r, _, err := objects.Get(ctx, FileKey(in.PaperID, in.SourceID, in.RevisionID, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("member %q changed: %q / %v", name, got, err)
		}
		member, ok := m.Member(name)
		if !ok || member.SHA256 != SHA256(want) || member.SizeBytes != int64(len(want)) {
			t.Fatalf("member metadata = %+v", member)
		}
	}
	infos, err := objects.ListPrefix(ctx, "content/qa_test/src_test/parses/pr_test/files/", 0)
	if err != nil || len(infos) != len(in.Files) {
		t.Fatalf("listing omitted original member: %d %v", len(infos), err)
	}
	if _, err := New(objects).VerifyBundle(ctx, in.PaperID, in.SourceID, in.RevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := New(objects).WriteBundle(ctx, in); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
}

func TestFreezePDFConcurrentAndWriteOnce(t *testing.T) {
	objects := localObjects(t)
	pdf := []byte("%PDF-1.7\nfrozen\n")
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := New(objects).FreezePDF(context.Background(), "qa_test", "src_test", pdf, SHA256(pdf))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(objects).FreezePDF(context.Background(), "qa_test", "src_test", []byte("%PDF-new"), ""); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("overwrite err=%v", err)
	}
	got, err := New(objects).ReadPDF(context.Background(), "qa_test", "src_test", SHA256(pdf), int64(len(pdf)))
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatalf("frozen bytes changed %q %v", got, err)
	}
	if _, err := New(objects).FreezePDF(context.Background(), "qa_other", "src_test", pdf, strings.Repeat("a", 64)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong hash %v", err)
	}
}

func TestWriteBundleNeverMarksPartialOrCorruptReady(t *testing.T) {
	for _, mode := range []string{"put-fail", "persist-corrupt", "short-write", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			base := localObjects(t)
			in := inputFixture(t, base)
			fault := &tracingStore{Store: base}
			key := FileKey(in.PaperID, in.SourceID, in.RevisionID, in.MiddlePath)
			switch mode {
			case "put-fail":
				fault.failKey = key
			case "persist-corrupt":
				fault.corruptKey = key
			case "short-write":
				fault.shortKey = key
			case "unsupported":
				fault.unsupported = true
			}
			if _, err := New(fault).WriteBundle(context.Background(), in); err == nil {
				t.Fatal("fault succeeded")
			}
			if _, exists, err := base.Stat(context.Background(), ManifestKey(in.PaperID, in.SourceID, in.RevisionID)); err != nil || exists {
				t.Fatalf("partial marker exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestBundleCollisionAndIntegrity(t *testing.T) {
	ctx := context.Background()
	objects := localObjects(t)
	in := inputFixture(t, objects)
	if _, err := New(objects).WriteBundle(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Files[in.MiddlePath] = []byte("replacement JSON")
	if _, err := New(objects).WriteBundle(ctx, in); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("member overwrite %v", err)
	}
	if _, err := New(objects).VerifyBundle(ctx, in.PaperID, in.SourceID, in.RevisionID); err != nil {
		t.Fatalf("old revision changed: %v", err)
	}
	if err := objects.Delete(ctx, FileKey(in.PaperID, in.SourceID, in.RevisionID, "vendor/custom binary.data")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(objects).VerifyBundle(ctx, in.PaperID, in.SourceID, in.RevisionID); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("missing member considered ready %v", err)
	}
}

func TestPathsAndRolesValidatedBeforeWriting(t *testing.T) {
	for _, name := range []string{"../escape", "/abs", "a/../b", "a/./b", "a//b", "a\\b", "a/", "bad\x00name"} {
		t.Run(name, func(t *testing.T) {
			objects := localObjects(t)
			in := inputFixture(t, objects)
			in.Files[name] = []byte("unsafe")
			if _, err := New(objects).WriteBundle(context.Background(), in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("path accepted %q: %v", name, err)
			}
			items, err := objects.ListPrefix(context.Background(), "content/qa_test/src_test/parses/", 0)
			if err != nil || len(items) > 0 {
				t.Fatalf("validation wrote members: %v %v", items, err)
			}
		})
	}
	objects := localObjects(t)
	in := inputFixture(t, objects)
	in.Files["nested"] = []byte("collides with directory")
	if _, err := New(objects).WriteBundle(context.Background(), in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("file/dir collision accepted %v", err)
	}
	delete(in.Files, "nested")
	in.MiddlePath = "missing.json"
	if _, err := New(objects).WriteBundle(context.Background(), in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing role accepted %v", err)
	}
}

type tracingStore struct {
	objstore.Store
	writes                        []string
	failKey, corruptKey, shortKey string
	unsupported                   bool
}

func (s *tracingStore) PutWithOptions(ctx context.Context, key string, r io.Reader, size int64, opts objstore.PutOptions) (int64, error) {
	if opts.IfNoneMatch != "*" {
		return 0, errors.New("unconditional write forbidden")
	}
	s.writes = append(s.writes, key)
	if s.unsupported {
		return 0, objstore.ErrPreconditionUnsupported
	}
	if key == s.failKey {
		return 0, errors.New("injected write failure")
	}
	if key == s.corruptKey {
		r = bytes.NewReader(bytes.Repeat([]byte("x"), int(size)))
	}
	n, err := s.Store.PutWithOptions(ctx, key, r, size, opts)
	if key == s.shortKey && err == nil {
		n--
	}
	return n, err
}
