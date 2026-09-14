package web

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestCacheLockWaitCancellation(t *testing.T) {
	r, calls := fixtureResolver(t, func(http.ResponseWriter, *http.Request) { t.Error("lock wait made a request") })
	lock := flock.New(filepath.Join(r.cache, "v0.35.0.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := r.resolve(ctx, "0.35.0"); err == nil || !strings.Contains(err.Error(), "lock UI cache") {
		t.Fatalf("lock cancellation not reported: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("download without lock")
	}
}

func TestBundleOpenUsesIndependentResidentBytes(t *testing.T) {
	data, digest := fixtureBundle(t, "0.35.0")
	tree, err := openBundle(data, digest, "0.35.0")
	if err != nil {
		t.Fatal(err)
	}
	// Once validated, file Opens must not re-read/inflate compressed bytes.
	for i := range data {
		data[i] = 0
	}
	first, err := tree.Open("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := tree.Open("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.(io.Seeker).Seek(-2, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	last, err := io.ReadAll(first)
	if err != nil || string(last) != ");" {
		t.Fatalf("SeekEnd = %q %v", last, err)
	}
	all, err := io.ReadAll(second)
	if err != nil || string(all) != "console.log('same UI');" {
		t.Fatalf("readers shared position or reinflated ZIP: %q %v", all, err)
	}
}

func TestZIPCRCAndDeclaredSizeIndependentOfSHA(t *testing.T) {
	for _, kind := range []string{"crc", "size"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := fixtureBundle(t, "0.35.0")
			// Modify the central directory then recompute OUTER SHA256, so
			// these cases reach inner CRC and declared-file-size validation.
			i := bytes.Index(data, []byte{'P', 'K', 1, 2})
			if i < 0 {
				t.Fatal("missing central directory")
			}
			if kind == "crc" {
				data[i+16] ^= 1
			} else {
				binary.LittleEndian.PutUint32(data[i+24:i+28], maxFileSize+1)
			}
			digest := sha256.Sum256(data)
			if _, err := openBundle(data, digest[:], "0.35.0"); err == nil {
				t.Fatal("inner ZIP validation bypassed")
			}
		})
	}
}

func TestZIPEntryCountLimit(t *testing.T) {
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	zw.SetComment(bundleComment("0.35.0"))
	for range maxBundleFiles + 1 {
		if _, err := zw.Create("same"); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data.Bytes())
	if _, err := openBundle(data.Bytes(), digest[:], "0.35.0"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("count limit not enforced: %v", err)
	}
}
