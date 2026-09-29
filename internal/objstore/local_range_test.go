package objstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"
)

// TestLocalGetRange drives LocalStore.GetRange over the inclusive-window
// contract: exact windows, EOF clamping, past-EOF empty reads, missing
// keys, and bad arguments.
func TestLocalGetRange(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	// Deterministic-but-varied payload.
	body := make([]byte, 1<<16)
	for i := range body {
		body[i] = byte(i * 7 % 251)
	}
	if _, err := store.Put(context.Background(), "pdf/big.bin", bytes.NewReader(body), int64(len(body)), ""); err != nil {
		t.Fatalf("put: %v", err)
	}

	read := func(start, end int64) []byte {
		t.Helper()
		rc, err := store.GetRange(context.Background(), "pdf/big.bin", start, end)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", start, end, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read range(%d,%d): %v", start, end, err)
		}
		return b
	}

	if got := read(0, 9); !bytes.Equal(got, body[:10]) {
		t.Errorf("range(0,9) = %d bytes, mismatch", len(got))
	}
	if got := read(1000, 1999); !bytes.Equal(got, body[1000:2000]) {
		t.Errorf("range(1000,1999) mismatch")
	}
	if got := read(int64(len(body))-4, int64(len(body))-1); !bytes.Equal(got, body[len(body)-4:]) {
		t.Errorf("tail range mismatch")
	}
	// Window extending past EOF clamps to size.
	if got := read(int64(len(body))-4, int64(len(body))+9999); !bytes.Equal(got, body[len(body)-4:]) {
		t.Errorf("clamped range mismatch")
	}
	// start == size-1: exactly one byte.
	if got := read(int64(len(body))-1, int64(len(body))-1); len(got) != 1 || got[0] != body[len(body)-1] {
		t.Errorf("single-byte range = %v", got)
	}
	// start >= size: empty, NOT ErrNotFound.
	if got := read(int64(len(body)), int64(len(body))+10); len(got) != 0 {
		t.Errorf("past-EOF range = %d bytes, want 0", len(got))
	}
	// Whole object via range equals Get.
	if got := read(0, int64(len(body))-1); !bytes.Equal(got, body) {
		t.Errorf("full range mismatch")
	}
	// Sum of consecutive windows == whole.
	var reassembled []byte
	for s := int64(0); s < int64(len(body)); s += 4096 {
		e := s + 4095
		if e >= int64(len(body)) {
			e = int64(len(body)) - 1
		}
		reassembled = append(reassembled, read(s, e)...)
	}
	if !bytes.Equal(reassembled, body) {
		t.Errorf("reassembled windows mismatch (sha %x vs %x)", sha256.Sum256(reassembled), sha256.Sum256(body))
	}

	// Missing key.
	if _, err := store.GetRange(context.Background(), "pdf/missing.bin", 0, 9); !IsNotFound(err) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
	// Bad args.
	if _, err := store.GetRange(context.Background(), "pdf/big.bin", -1, 9); err == nil {
		t.Error("negative start accepted")
	}
	if _, err := store.GetRange(context.Background(), "pdf/big.bin", 10, 9); err == nil {
		t.Error("end < start accepted")
	}
}
