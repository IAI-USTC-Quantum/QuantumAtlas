package routes

// ingest_test_helpers.go: tiny helpers for the mineru-ingest tests.
import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// zipWriterT wraps a zip.Writer with fail-fast writes for tests.
type zipWriterT struct {
	zw *zip.Writer
	t  *testing.T
}

func newZipWriter(t *testing.T, buf *bytes.Buffer) *zipWriterT {
	return &zipWriterT{zw: zip.NewWriter(buf), t: t}
}

func (w *zipWriterT) write(name string, b []byte) {
	fw, err := w.zw.Create(name)
	if err != nil {
		w.t.Fatalf("create %s: %v", name, err)
	}
	if _, err := fw.Write(b); err != nil {
		w.t.Fatalf("write %s: %v", name, err)
	}
}

func (w *zipWriterT) close() {
	if err := w.zw.Close(); err != nil {
		w.t.Fatalf("close zip: %v", err)
	}
}

func readAllFromStore(t *testing.T, store objstore.Store, key string) ([]byte, error) {
	t.Helper()
	rc, _, err := store.Get(context.Background(), key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func sha256Bytes(b []byte) [32]byte { return sha256.Sum256(b) }
