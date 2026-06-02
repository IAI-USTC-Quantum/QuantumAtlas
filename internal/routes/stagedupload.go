package routes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
)

// newInMemoryBodyFromBytes wraps an already-in-hand byte slice as a
// stagedBody, computing sha256 over the contents. Used by handlers
// that have to pre-process the request body (e.g. unzipping a
// `mineru_zip` upload into its component md/image parts) before
// driving uploadOne against each part — at that point the bytes are
// already in memory so the stream-reading machinery in stageInMemory
// would be wasted work.
func newInMemoryBodyFromBytes(b []byte) *inMemoryBody {
	h := sha256.Sum256(b)
	return &inMemoryBody{
		bytes: b,
		sha:   hex.EncodeToString(h[:]),
	}
}

// stagedBody is the body that has been fully received from the client,
// validated, sha256'd, and is ready to be re-read by the store
// PUT. It abstracts over two backings:
//
//   - in-memory []byte (for small metadata uploads — JSON, markdown
//     under a few MB) where the convenience of a single allocation
//     outweighs the OS overhead of opening a temp file.
//
//   - on-disk temp file (for PDFs up to 100 MiB) where holding the
//     bytes in process memory would let 10 concurrent uploads pin
//     ~1 GB of RSS — enough to OOM a memory-tight VM (~1 GB class).
//
// Both implementations are race-safe to Open() concurrently (handlers
// don't actually do this today, but it costs nothing and protects
// future refactors). Both implementations require an explicit Close()
// — the file backing deletes its temp file, the memory backing zeroes
// the slice to help GC.
//
// uploadOne (papers.go) calls Open() up to twice — once for the
// initial conditional Put, once on the rare retry path. The returned
// reader is rewound to byte 0 each time. Callers must Close() the
// returned reader between attempts; Open()ing a stagedBody whose
// previous reader is still open is undefined behavior (the file
// backing would re-seek a shared fd to 0).
type stagedBody interface {
	// Sha256 returns the hex-encoded sha256 of the staged bytes.
	Sha256() string
	// Size returns the number of staged bytes.
	Size() int64
	// Open returns a Reader positioned at the first byte. The caller
	// MUST Close the returned reader before calling Open again.
	Open() (io.ReadCloser, error)
	// Close releases backing resources (temp file deletion, slice
	// release). Safe to call multiple times.
	Close() error
}

// stageInMemory reads up to maxBytes from src into a []byte while
// computing sha256, then runs the validate callback against the full
// body. Best for small uploads (metadata JSON, short markdown) where
// the round-trip through tmp would be wasteful.
//
// The validate callback runs after the full body is in memory so it
// can do whole-body checks (json.Unmarshal, etc.). It returns a typed
// uploadError so the caller can map onto an HTTP status.
func stageInMemory(
	ctx context.Context,
	src io.Reader,
	maxBytes int64,
	label string,
	validate func([]byte) *uploadError,
) (stagedBody, *uploadError) {
	_ = ctx // reserved for future cancellation-aware copy
	hash := sha256.New()
	// Allocate a buffer with capacity equal to the cap so well-formed
	// uploads don't trigger resize churn. Stays in memory for the
	// caller's lifetime, so any over-allocation is short-lived.
	buf := make([]byte, 0, min64(maxBytes+1, 1<<20))
	bufWriter := &byteSliceWriter{buf: &buf}
	limited := io.LimitReader(src, maxBytes+1)
	if _, err := io.Copy(io.MultiWriter(bufWriter, hash), limited); err != nil {
		return nil, &uploadError{
			Status: 500,
			Detail: fmt.Sprintf("read %s: %s", label, err.Error()),
		}
	}
	n := int64(len(buf))
	if n > maxBytes {
		return nil, &uploadError{
			Status: 413,
			Detail: fmt.Sprintf("%s upload exceeds limit of %d bytes", label, maxBytes),
		}
	}
	if n == 0 {
		return nil, &uploadError{
			Status: 400,
			Detail: fmt.Sprintf("%s upload was empty", label),
		}
	}
	if vErr := validate(buf); vErr != nil {
		return nil, vErr
	}
	return &inMemoryBody{
		bytes: buf,
		sha:   hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

// stageToTmpFile streams up to maxBytes from src into a fresh O_EXCL
// tmp file under the OS tmp dir while computing sha256. Best for
// large uploads (PDFs) where holding the bytes in memory would risk
// process OOM under concurrent load.
//
// Validation strategy: we accept a headValidate callback that runs on
// the first `headPeek` bytes BEFORE the rest is streamed. This lets
// callers cheaply reject obviously-bad uploads (e.g. PDF without %PDF-
// header) without paying the I/O cost of the full file. If
// headValidate returns nil, streaming continues; if it returns an
// uploadError, we abort and clean up the partial tmp file.
//
// On any error after the tmp file is created, the file is removed
// before returning so callers don't have to worry about cleanup.
//
// The returned stagedBody's Close() removes the tmp file, so the
// handler MUST defer Close() on the happy path AND in every error
// path — except those that immediately return from this function
// (because we've already cleaned up on the way out).
func stageToTmpFile(
	ctx context.Context,
	src io.Reader,
	maxBytes int64,
	label string,
	headPeek int,
	headValidate func(head []byte) *uploadError,
) (stagedBody, *uploadError) {
	_ = ctx // reserved
	if headPeek < 0 {
		headPeek = 0
	}

	// Random suffix so concurrent uploaders never clash on the tmp
	// pattern. We use crypto/rand directly (not os.CreateTemp's own
	// random) so the suffix is auditable and consistent with the
	// LocalStore .part naming scheme.
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, &uploadError{
			Status: 500,
			Detail: fmt.Sprintf("tmp nonce: %s", err.Error()),
		}
	}
	tmpName := fmt.Sprintf("qatlas-upload-%s-%s.tmp", label, hex.EncodeToString(nonce[:]))
	f, err := os.CreateTemp("", tmpName)
	if err != nil {
		return nil, &uploadError{
			Status: 500,
			Detail: fmt.Sprintf("create tmp for %s: %s", label, err.Error()),
		}
	}
	// On any subsequent error, remove the tmp file before returning
	// so the caller never has to clean up after a stageToTmpFile
	// failure. The happy path returns the open file inside the
	// stagedBody and the caller's defer-Close handles removal.
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	hash := sha256.New()
	// Head peek path: read the first headPeek bytes into a local
	// buffer, validate, then write them out + continue streaming the
	// rest. We MUST also feed those bytes through the hash so the
	// final digest matches what's actually in the tmp file.
	var head []byte
	if headPeek > 0 {
		head = make([]byte, headPeek)
		n, rerr := io.ReadFull(src, head)
		// ReadFull returns ErrUnexpectedEOF when EOF arrives early;
		// treat that as "we got fewer than headPeek bytes" which
		// validate can still reject as malformed.
		if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
			cleanup()
			return nil, &uploadError{
				Status: 400,
				Detail: fmt.Sprintf("read %s head: %s", label, rerr.Error()),
			}
		}
		head = head[:n]
		if vErr := headValidate(head); vErr != nil {
			cleanup()
			return nil, vErr
		}
		if _, werr := f.Write(head); werr != nil {
			cleanup()
			return nil, &uploadError{
				Status: 500,
				Detail: fmt.Sprintf("write %s head to tmp: %s", label, werr.Error()),
			}
		}
		hash.Write(head)
	}

	// Stream the rest, tee-ing through the hash. LimitReader caps at
	// maxBytes+1 so we can distinguish "exactly at cap" from "over
	// cap" by checking if anything came back after the cap.
	written, copyErr := io.Copy(
		io.MultiWriter(f, hash),
		io.LimitReader(src, maxBytes+1-int64(len(head))),
	)
	if copyErr != nil {
		cleanup()
		return nil, &uploadError{
			Status: 500,
			Detail: fmt.Sprintf("write %s body to tmp: %s", label, copyErr.Error()),
		}
	}
	totalBytes := int64(len(head)) + written
	if totalBytes > maxBytes {
		cleanup()
		return nil, &uploadError{
			Status: 413,
			Detail: fmt.Sprintf("%s upload exceeds limit of %d bytes", label, maxBytes),
		}
	}
	if totalBytes == 0 {
		cleanup()
		return nil, &uploadError{
			Status: 400,
			Detail: fmt.Sprintf("%s upload was empty", label),
		}
	}
	// Flush + sync the tmp file so a subsequent reopener (the store
	// Put) reads the full content. Sync is overkill on most modern
	// kernels (writeback eventually flushes anyway) but the cost is
	// trivial for one upload and the alternative — silent partial
	// reads under memory pressure — is a debug nightmare.
	if err := f.Sync(); err != nil {
		cleanup()
		return nil, &uploadError{
			Status: 500,
			Detail: fmt.Sprintf("sync %s tmp: %s", label, err.Error()),
		}
	}
	return &tmpFileBody{
		f:    f,
		size: totalBytes,
		sha:  hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

// inMemoryBody is the small-upload backing.
type inMemoryBody struct {
	bytes  []byte
	sha    string
	closed atomic.Bool
}

func (b *inMemoryBody) Sha256() string { return b.sha }
func (b *inMemoryBody) Size() int64    { return int64(len(b.bytes)) }
func (b *inMemoryBody) Open() (io.ReadCloser, error) {
	if b.closed.Load() {
		return nil, errors.New("stagedBody: Open after Close")
	}
	return io.NopCloser(newByteSliceReader(b.bytes)), nil
}
func (b *inMemoryBody) Close() error {
	b.closed.Store(true)
	// Drop the slice so the GC can reclaim it promptly even if
	// something else holds the *inMemoryBody (e.g. test fixtures).
	b.bytes = nil
	return nil
}

// tmpFileBody is the large-upload backing. The underlying os.File is
// reopened on each Open() so a previous reader's Close doesn't affect
// the next one — important for the uploadOne retry path which closes
// the first reader before reopening for the retry.
type tmpFileBody struct {
	f      *os.File // owns the tmp file lifetime; its Name() is the path
	size   int64
	sha    string
	closed atomic.Bool
}

func (b *tmpFileBody) Sha256() string { return b.sha }
func (b *tmpFileBody) Size() int64    { return b.size }
func (b *tmpFileBody) Open() (io.ReadCloser, error) {
	if b.closed.Load() {
		return nil, errors.New("stagedBody: Open after Close")
	}
	// Reopen the file by path so multiple concurrent (or sequential)
	// Open calls each get an independent fd with its own seek offset.
	// Cheap (~1 syscall) and avoids fd-sharing bugs.
	f, err := os.Open(b.f.Name())
	if err != nil {
		return nil, fmt.Errorf("stagedBody: reopen tmp: %w", err)
	}
	return f, nil
}
func (b *tmpFileBody) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	// Close the original fd (we held it for ownership purposes) and
	// unlink the path so the inode is released as soon as all
	// reader-side fds (any returned by Open and still open) finish.
	closeErr := b.f.Close()
	rmErr := os.Remove(b.f.Name())
	if closeErr != nil {
		return closeErr
	}
	if rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return rmErr
	}
	return nil
}

// byteSliceWriter is io.Writer that appends to a *[]byte. Same role
// as bytes.Buffer but with a backing slice we already pre-sized for
// the expected upload, avoiding the buffer's geometric resize.
type byteSliceWriter struct{ buf *[]byte }

func (w *byteSliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// byteSliceReader is a minimal io.Reader over a []byte with no
// dependency on bytes.Reader (avoids importing bytes here just for
// the Reader type — we already keep this file dep-light).
type byteSliceReader struct {
	src []byte
	pos int
}

func newByteSliceReader(src []byte) *byteSliceReader {
	return &byteSliceReader{src: src}
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.src) {
		return 0, io.EOF
	}
	n := copy(p, r.src[r.pos:])
	r.pos += n
	return n, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
