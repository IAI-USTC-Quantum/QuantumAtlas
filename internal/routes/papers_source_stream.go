package routes

// papers_source_stream.go: streaming Range delivery for the Q1
// source-PDF endpoint (plan §12.2 "Range 同样鉴权", §8 Q1 "Range requests
// are authenticated identically").
//
// Small PDFs (<= streamRangeThreshold) keep the legacy whole-read path:
// buffer + sha256-verify server-side + http.ServeContent over a
// bytes.Reader. Larger PDFs switch to store.GetRange-backed
// rangedReadSeeker so the object never fully transits server memory:
// http.ServeContent still drives Range/206/If-Range semantics, but each
// Read turns into a bounded ranged GetObject / file section read.
//
// Verification trade-off on the large path: the server can't hash what
// it doesn't read. The ETag and X-QAtlas-Sha256 headers still carry the
// sha256 the registry row pins, so clients verify end-to-end; the row
// size is cross-checked against the object's actual size (Stat) before
// streaming so a truncated/replaced object is caught up-front.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// streamRangeThreshold is the PDF size at or below which the handler
// prefers the whole-read path (server-side sha verification), above
// which it streams via ranged reads. Source PDFs are typically 0.5–8 MiB
// (arXiv PDF cap); 4 MiB keeps the common small paper fully verified
// while bounding buffered memory for the rest.
const streamRangeThreshold = 4 << 20 // 4 MiB

// serveSourcePDF streams the source PDF named name for src via
// http.ServeContent. Both headers (ETag / X-QAtlas-Sha256 /
// Content-Disposition) and the body are written; returns the handler
// error. Errors map: ErrNotFound → caller's honest-404, size mismatch →
// 500 (store corruption), anything else → 500.
func serveSourcePDF(re *core.RequestEvent, store objstore.Store, src registry.PaperSource, name string) error {
	ctx := re.Request.Context()

	// Small object: whole read + server-side hash verification. This is
	// also the fallback when the size is unknown.
	if src.SizeBytes >= 0 && src.SizeBytes <= streamRangeThreshold {
		pdfBytes, err := blockReadVerified(ctx, store, src.ObjstoreKey, src.Sha256)
		if err != nil {
			return err
		}
		setSourcePDFHeaders(re, src, name)
		http.ServeContent(re.Response, re.Request, src.SourceID+".pdf", time.Time{}, bytes.NewReader(pdfBytes))
		return nil
	}

	// Large object: confirm the bytes exist and agree with the pinned
	// size before streaming, then hand ServeContent a ranged ReadSeeker.
	info, exists, err := store.Stat(ctx, src.ObjstoreKey)
	if err != nil {
		return err
	}
	if !exists {
		return objstore.ErrNotFound
	}
	if info.Size >= 0 && info.Size != src.SizeBytes {
		return fmt.Errorf("source pdf %s size drift: row pins %d bytes, store has %d",
			src.ObjstoreKey, src.SizeBytes, info.Size)
	}
	setSourcePDFHeaders(re, src, name)
	rs := newRangedReadSeeker(ctx, store, src.ObjstoreKey, src.SizeBytes)
	http.ServeContent(re.Response, re.Request, src.SourceID+".pdf", time.Time{}, rs)
	return rs.err // surface a mid-stream read failure honestly
}

// setSourcePDFHeaders stamps the shared caching/identity headers on the
// response before ServeContent writes the body.
func setSourcePDFHeaders(re *core.RequestEvent, src registry.PaperSource, name string) {
	re.Response.Header().Set("ETag", `"`+src.Sha256+`"`)
	re.Response.Header().Set("X-QAtlas-Sha256", src.Sha256)
	re.Response.Header().Set("Cache-Control", "private, max-age=86400")
	re.Response.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename=%q", sanitizeFilename(name)))
}

// rangedReadSeeker adapts an objstore.Store's GetRange into the
// io.ReadSeeker http.ServeContent requires. Seek only moves the logical
// offset; Read opens one bounded ranged fetch from the current offset.
// err captures the first read failure so the caller can log it after
// ServeContent returns (the body has usually been partially written by
// then — ServeContent cannot un-send it).
type rangedReadSeeker struct {
	ctx   context.Context
	store objstore.Store
	key   string
	size  int64
	off   int64
	err   error
}

func newRangedReadSeeker(ctx context.Context, store objstore.Store, key string, size int64) *rangedReadSeeker {
	return &rangedReadSeeker{ctx: ctx, store: store, key: key, size: size}
}

func (r *rangedReadSeeker) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.off >= r.size {
		return 0, io.EOF
	}
	end := r.off + int64(len(p)) - 1
	if end >= r.size {
		end = r.size - 1
	}
	rc, err := r.store.GetRange(r.ctx, r.key, r.off, end)
	if err != nil {
		r.err = err
		return 0, err
	}
	n, err := io.ReadFull(rc, p)
	_ = rc.Close()
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		r.err = err
		return n, err
	}
	if n == 0 {
		// Store returned short/empty for a window the Stat said exists:
		// treat as truncation, not a clean EOF (caller's Content-Length
		// came from size, so silence here would under-deliver).
		r.err = io.ErrUnexpectedEOF
		return 0, r.err
	}
	r.off += int64(n)
	return n, nil
}

func (r *rangedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		// offset as-is
	case io.SeekCurrent:
		offset += r.off
	case io.SeekEnd:
		offset += r.size
	default:
		return 0, fmt.Errorf("rangedReadSeeker: bad whence %d", whence)
	}
	if offset < 0 {
		return 0, fmt.Errorf("rangedReadSeeker: negative seek %d", offset)
	}
	r.off = offset
	return r.off, nil
}
