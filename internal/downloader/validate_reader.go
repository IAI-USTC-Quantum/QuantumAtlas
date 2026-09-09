package downloader

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// inspectStoredPDF validates an existing canonical object before accepting it as
// the winner of a conditional write. It streams instead of allocating a PDF-sized
// buffer, using the same signature/minimum/maximum/trailer checks as FetchClient.
func inspectStoredPDF(r io.Reader) (string, int64, error) {
	h := sha256.New()
	scan := &pdfStreamCheck{}
	n, err := io.Copy(io.MultiWriter(h, scan), io.LimitReader(r, DefaultMaxPDFBytes+1))
	if err != nil {
		return "", n, err
	}
	if n > DefaultMaxPDFBytes {
		return "", n, ErrTooLarge
	}
	if n < DefaultMinPDFBytes {
		return "", n, ErrTooSmall
	}
	if ClassifyBody(scan.head) != BodyPDF {
		return "", n, ErrNotPDF
	}
	if !bytes.Contains(scan.tail, []byte("%%EOF")) && !scan.hasXref {
		return "", n, ErrTruncated
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type pdfStreamCheck struct {
	head, tail, overlap []byte
	hasXref             bool
}

func (s *pdfStreamCheck) Write(p []byte) (int, error) {
	n := len(p)
	if len(s.head) < 5 {
		s.head = append(s.head, p[:min(5-len(s.head), n)]...)
	}
	if !s.hasXref {
		joined := append(s.overlap, p[:min(len(p), 8)]...)
		s.hasXref = bytes.Contains(joined, []byte("startxref")) || bytes.Contains(p, []byte("startxref"))
		if len(p) >= 8 {
			s.overlap = append(s.overlap[:0], p[len(p)-8:]...)
		} else {
			s.overlap = append(s.overlap, p...)
			if len(s.overlap) > 8 {
				s.overlap = s.overlap[len(s.overlap)-8:]
			}
		}
	}
	if len(p) >= 2048 {
		s.tail = append(s.tail[:0], p[len(p)-2048:]...)
	} else {
		s.tail = append(s.tail, p...)
		if len(s.tail) > 2048 {
			s.tail = s.tail[len(s.tail)-2048:]
		}
	}
	return n, nil
}
