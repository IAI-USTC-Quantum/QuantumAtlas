package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ErrPDFIdentityUnproven means format-valid bytes have not been shown to belong
// to the requested published work. Never archive/register them under that DOI.
var ErrPDFIdentityUnproven = errors.New("downloader: PDF ownership could not be proven")

// PublishedIdentity must come from trusted request/catalog/resolver metadata,
// NOT the candidate PDF or an untrusted landing page. DOI alone is deliberately
// insufficient: an unrelated paper can cite the requested DOI in its references.
type PublishedIdentity struct {
	DOI   string
	Title string
}

// PDFProvenanceConfig controls a local, bounded pdftotext invocation. Zero values
// use pdftotext from PATH, 10 seconds, 100 MiB input, and 256 KiB text. The hard
// upper bounds are 15 seconds, DefaultMaxPDFBytes, and 1 MiB. The executable is
// a trusted administrator/test setting, never a path supplied by a publisher.
type PDFProvenanceConfig struct {
	PDFToTextPath string
	Timeout       time.Duration
	MaxPDFBytes   int64
	MaxTextBytes  int64
}

// VerifyPublishedPDF applies CONTENT-level provenance after ordinary HTTP/PDF
// format validation and before an attempt is declared successful. It requires
// an exact normalized, sufficiently distinctive title in the first page's front
// matter, not a DOI occurrence, fuzzy substring, abstract, or bibliography hit.
//
// The API consumes result.Body and closes an original io.Closer on ALL paths.
// On success it replaces Body with a fresh reader of the identical PDF bytes
// and leaves URL/hash/size metadata unchanged. On failure Body is nil, so callers
// cannot accidentally store the consumed/unproven candidate. Use for published
// candidates from ALL lanes (HTTP, browser, agent, new sources, worker); do NOT
// apply to canonical arXiv originals, whose identity is pinned by their fetcher.
func VerifyPublishedPDF(ctx context.Context, result *FetchResult, identity PublishedIdentity, cfg PDFProvenanceConfig) error {
	fail := func(reason string) error { return fmt.Errorf("%w: %s", ErrPDFIdentityUnproven, reason) }
	if result == nil || result.Body == nil {
		return fail("missing PDF body")
	}
	body := result.Body
	result.Body = nil
	if closer, ok := body.(io.Closer); ok {
		defer closer.Close()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrPDFIdentityUnproven, err)
	}
	title := normalizeProvenanceTitle(identity.Title)
	if len([]rune(title)) < 24 || len(strings.Fields(title)) < 3 {
		return fail("trusted title unavailable or insufficiently distinctive")
	}
	if cfg.PDFToTextPath == "" {
		cfg.PDFToTextPath = "pdftotext"
	}
	if cfg.Timeout <= 0 || cfg.Timeout > 15*time.Second {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxPDFBytes <= 0 || cfg.MaxPDFBytes > DefaultMaxPDFBytes {
		cfg.MaxPDFBytes = DefaultMaxPDFBytes
	}
	if cfg.MaxTextBytes <= 0 || cfg.MaxTextBytes > 1024*1024 {
		cfg.MaxTextBytes = 256 * 1024
	}
	pdf, err := io.ReadAll(io.LimitReader(body, cfg.MaxPDFBytes+1))
	if err != nil {
		return fail("read PDF body failed")
	}
	if int64(len(pdf)) > cfg.MaxPDFBytes {
		return fail("PDF exceeds provenance input limit")
	}
	if ClassifyBody(pdf) != BodyPDF {
		return fail("body is not a PDF")
	}
	extractCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	text := &provenanceTextBuffer{limit: cfg.MaxTextBytes, cancel: cancel}
	cmd := exec.CommandContext(extractCtx, cfg.PDFToTextPath, "-f", "1", "-l", "3", "-layout", "-enc", "UTF-8", "-", "-")
	cmd.Stdin = bytes.NewReader(pdf)
	cmd.Stdout = text
	cmd.Stderr = io.Discard
	// A misconfigured wrapper must not retain stdout via a descendant process.
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Run(); err != nil {
		if extractCtx.Err() != nil {
			return fmt.Errorf("%w: extraction failed: %w", ErrPDFIdentityUnproven, extractCtx.Err())
		}
		return fail("pdftotext unavailable or extraction failed")
	}
	if !provenanceFrontMatterTitle(text.String(), title) {
		return fail("trusted title does not match PDF front matter")
	}
	result.Body = bytes.NewReader(pdf)
	return nil
}

// Limit output while it is produced, not after CombinedOutput has allocated it.
// Cancel immediately on overflow so even a noisy/hung configured wrapper exits.
type provenanceTextBuffer struct {
	bytes.Buffer
	limit  int64
	cancel context.CancelFunc
}

func (b *provenanceTextBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.limit {
		b.cancel()
		return 0, errors.New("pdftotext output limit exceeded")
	}
	return b.Buffer.Write(p)
}

var provenanceLineHyphen = regexp.MustCompile(`([\p{L}\p{N}])[-‐]\s*\n\s*([\p{L}\p{N}])`)

func normalizeProvenanceTitle(s string) string {
	s = strings.ReplaceAll(s, "\u00ad", "")
	s = norm.NFKC.String(s) // including PDF ligatures and fullwidth glyphs
	var out strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(unicode.ToLower(r))
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func provenanceFrontMatterTitle(text, expected string) bool {
	// Only the FIRST page supplies title evidence. References on subsequent
	// pages, even among the extracted first three, cannot prove ownership.
	firstPage, _, _ := strings.Cut(text, "\f")
	if len(firstPage) > 8192 {
		firstPage = firstPage[:8192]
	}
	firstPage = provenanceLineHyphen.ReplaceAllString(firstPage, "$1$2")
	var lines []string
	for _, line := range strings.Split(firstPage, "\n") {
		line = normalizeProvenanceTitle(line)
		if line == "" {
			continue
		}
		if provenanceBodyHeading(line) {
			break
		}
		lines = append(lines, line)
		if len(lines) >= 60 {
			break
		}
	}
	for start := range lines {
		// Match whole contiguous lines, not a substring of a sentence. Five
		// wrapped title lines allows ordinary two-column publisher layouts.
		for end := start + 1; end <= min(len(lines), start+5); end++ {
			if strings.Join(lines[start:end], " ") == expected {
				return true
			}
		}
	}
	return false
}

func provenanceBodyHeading(line string) bool {
	line = strings.TrimLeft(line, "0123456789 ")
	for _, heading := range []string{"abstract", "introduction", "references", "bibliography", "acknowledgments", "acknowledgements"} {
		if line == heading || strings.HasPrefix(line, heading+" ") {
			return true
		}
	}
	return false
}
