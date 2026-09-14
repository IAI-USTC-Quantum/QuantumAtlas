//go:build embedui

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io/fs"
	"testing"
)

func TestEmbeddedFirstAndIdenticalBundle(t *testing.T) {
	// Unknown version and unusable cache must not affect built-in UI startup.
	t.Setenv("XDG_CACHE_HOME", "/not-a-writable-ui-cache")
	tree, err := Resolve(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Pack(tree, "0.35.0", &out); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(out.Bytes())
	downloaded, err := openBundle(out.Bytes(), digest[:], "0.35.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		want, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		got, err := fs.ReadFile(downloaded, name)
		if err != nil {
			return err
		}
		if !bytes.Equal(want, got) {
			t.Errorf("embedded and downloadable UI differ: %s", name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
