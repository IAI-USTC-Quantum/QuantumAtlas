package objstore

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestLocalMemberMetadataSuffixIsOrdinaryObject(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	// Reverse lexicographic upload order proves that uploading the sibling
	// does not clear/delete a valid original member with the sidecar suffix.
	for _, key := range []string{"nested/member..json.meta.json", "nested/member..json"} {
		if _, err := s.PutWithOptions(ctx, key, bytes.NewBufferString(key), int64(len(key)), PutOptions{IfNoneMatch: "*", Metadata: map[string]string{"sha256": "example"}}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListPrefix(ctx, "", 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("original inventory hidden/metadata leaked: %v %v", items, err)
	}
	if err := s.Delete(ctx, "nested/member..json"); err != nil {
		t.Fatal(err)
	}
	r, _, err := s.Get(ctx, "nested/member..json.meta.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(body) != "nested/member..json.meta.json" {
		t.Fatalf("sibling metadata member changed: %q %v", body, err)
	}
}

func TestListPrefixMatchesLogicalScopeNotNestedBasenames(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	for _, key := range []string{"content/qa/src/parses/pr/files/out/markdown.md", "content/qa/src/parses/pr/files/pdf/source.pdf", "content/qa/src/parses/pr/files/images/image.png"} {
		if _, err := s.Put(ctx, key, bytes.NewBufferString("bytes"), 5, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"markdown/", "pdf/", "images/", "markdown", "pdf", "images"} {
		items, err := s.ListPrefix(ctx, prefix, 0)
		if err != nil || len(items) != 0 {
			t.Fatalf("prefix %q leaked nested names: %v %v", prefix, items, err)
		}
	}
	items, err := s.ListPrefix(ctx, "content/qa/src/parses/pr/files/out/mark", 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("valid partialstem broke: %v %v", items, err)
	}
}

func TestObjectKeysAllowDotsButNotTraversalComponents(t *testing.T) {
	for _, key := range []string{"content/qa_test/src_test/parses/pr_test/files/name..json", "nested/.hidden.json", "original.meta.json", "files/.objstore-meta/original.json"} {
		if err := validateKey(key); err != nil {
			t.Fatalf("valid name %q rejected: %v", key, err)
		}
	}
	for _, key := range []string{"a/../b", "a/./b", "a//b", "/abs", "a\\b", "a\x00b"} {
		if err := validateKey(key); err == nil {
			t.Fatalf("invalid key %q accepted", key)
		}
	}
}
