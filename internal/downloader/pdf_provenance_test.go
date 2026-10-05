package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// provenanceFixturePDF builds genuine, self-contained PDF fixtures with an
// xref table and printable first-page/front-matter/reference text. The real
// pdftotext executable is used in the content tests; no publisher is contacted.
func provenanceFixturePDF(pages ...[]string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var kids []string
	for _, lines := range pages {
		pageID := len(objects) + 1
		streamID := pageID + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		var stream strings.Builder
		stream.WriteString("BT /F1 16 Tf 50 780 Td 24 TL\n")
		for _, line := range lines {
			line = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(line)
			fmt.Fprintf(&stream, "(%s) Tj T*\n", line)
		}
		stream.WriteString("ET\n")
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 842] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", streamID),
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", stream.Len(), stream.String()))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return pdf.Bytes()
}

func TestPDFProvenanceContent(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	const title = "An Improved Quantum Algorithm for Searching an Ordered List"
	identity := PublishedIdentity{DOI: "10.1145/3800579", Title: title}
	for _, tt := range []struct {
		name  string
		pages [][]string
		want  bool
	}{
		{"matching title in front matter", [][]string{{title, "Alice Smith and Bob Jones", "Abstract", "A quantum search algorithm."}}, true},
		{"wrapped typographic title", [][]string{{"AN IMPROVED QUANTUM ALGORITHM", "FOR SEARCHING AN ORDERED LIST", "Alice Smith", "Abstract", "Body"}}, true},
		{"line break hyphenation", [][]string{{"An Improved Quantum Algo-", "rithm for Searching an Ordered List", "Alice Smith", "Abstract", "Body"}}, true},
		{"target title inside abstract is not ownership", [][]string{{"TensorFlow: Large Scale Machine Learning", "Abstract", title, "Body"}}, false},
		{"valid unrelated PDF with DOI reference", [][]string{{"TensorFlow: Large Scale Machine Learning", "Alice Smith", "Abstract", "Unrelated work", "References", "Requested DOI: 10.1145/3800579"}}, false},
		{"title and DOI only in references", [][]string{{"TensorFlow: Large Scale Machine Learning", "References", title, "doi:10.1145/3800579"}}, false},
		{"title is only prose substring", [][]string{{"TensorFlow: Large Scale Machine Learning", "We compare against " + title + " in this study."}}, false},
		{"different article changes one title word", [][]string{{"An Improved Classical Algorithm for Searching an Ordered List", "Alice Smith", "Abstract", "Body"}}, false},
		{"title on later reference page", [][]string{{"TensorFlow: Large Scale Machine Learning", "Abstract", "Body"}, {"References", title, "doi:10.1145/3800579"}}, false},
		{"image only PDF is unproven", [][]string{{}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pdf := provenanceFixturePDF(tt.pages...)
			// These are legal PDF-shaped bytes which the old format-only check
			// accepted, including every wrong-paper fixture above.
			b := NewBrowserLane(BrowserConfig{CDPURL: "unused"})
			res, err := b.validateBody(&browserBody{kind: BodyPDF, body: pdf, status: 200})
			if err != nil {
				t.Fatalf("fixture fails existing format validation: %v", err)
			}
			err = VerifyPublishedPDF(context.Background(), res, identity, PDFProvenanceConfig{})
			if tt.want {
				if err != nil {
					t.Fatalf("verified article rejected: %v", err)
				}
				body, err := io.ReadAll(res.Body)
				if err != nil || !bytes.Equal(body, pdf) {
					t.Fatalf("success must preserve full PDF body: err=%v len=%d", err, len(body))
				}
			} else {
				if !errors.Is(err, ErrPDFIdentityUnproven) {
					t.Fatalf("unproven PDF accepted: err=%v", err)
				}
				if res.Body != nil {
					t.Fatal("failed verification must make candidate unusable")
				}
			}
		})
	}
}

func TestPDFProvenanceArxivOriginalPathUnchanged(t *testing.T) {
	// Canonical arXiv originals must remain usable without a DOI title or a
	// local text extractor. An image-only PDF cannot pass published provenance,
	// but here the immutable requested arXiv/version URL owns its identity.
	pdf := provenanceFixturePDF([]string{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pdf/2401.01234v2" {
			t.Errorf("unexpected arXiv original path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	}))
	defer server.Close()
	fetcher, err := arxiv.New(arxiv.Config{BaseURL: server.URL + "/pdf/"})
	if err != nil {
		t.Fatal(err)
	}
	d := &Downloader{arxiv: fetcher}
	out, err := d.FetchPDF(context.Background(), registry.PaperRef{ArxivID: "2401.01234v2", DOI: "10.1145/3800579"})
	if err != nil || out == nil || out.Strategy != "arxiv" || out.ArxivCanonical != "2401.01234v2" || out.ArxivVersion != 2 {
		t.Fatalf("arXiv original path changed: outcome=%+v err=%v", out, err)
	}
	got, err := io.ReadAll(out.Result.Body)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatalf("arXiv body changed: %v", err)
	}
}

type provenanceClosingReader struct {
	*bytes.Reader
	closed int
}

func (r *provenanceClosingReader) Close() error { r.closed++; return nil }

func TestPDFProvenanceBodyOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controlled-pdftotext")
	const title = "An Improved Quantum Algorithm for Searching an Ordered List"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+title+"' 'Abstract'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pdf := provenanceFixturePDF([]string{title})
	for _, success := range []bool{true, false} {
		r := &provenanceClosingReader{Reader: bytes.NewReader(pdf)}
		result := &FetchResult{Body: r}
		identity := PublishedIdentity{Title: title}
		if !success {
			identity.Title = ""
		}
		err := VerifyPublishedPDF(context.Background(), result, identity, PDFProvenanceConfig{PDFToTextPath: path})
		if (err == nil) != success || r.closed != 1 || (result.Body != nil) != success {
			t.Fatalf("success=%v err=%v closed=%d bodyPresent=%v", success, err, r.closed, result.Body != nil)
		}
	}
}

func TestPDFProvenanceFailClosedAndBounded(t *testing.T) {
	identity := PublishedIdentity{DOI: "10.1145/3800579", Title: "An Improved Quantum Algorithm for Searching an Ordered List"}
	pdf := provenanceFixturePDF([]string{identity.Title})
	for _, tt := range []struct {
		name     string
		identity PublishedIdentity
		config   PDFProvenanceConfig
	}{
		{"title unavailable", PublishedIdentity{DOI: identity.DOI}, PDFProvenanceConfig{}},
		{"title too generic", PublishedIdentity{DOI: identity.DOI, Title: "A Study"}, PDFProvenanceConfig{}},
		{"extractor unavailable", identity, PDFProvenanceConfig{PDFToTextPath: "/definitely/missing/pdftotext"}},
		{"input bound", identity, PDFProvenanceConfig{MaxPDFBytes: 16}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := &FetchResult{Body: bytes.NewReader(pdf)}
			if err := VerifyPublishedPDF(context.Background(), res, tt.identity, tt.config); !errors.Is(err, ErrPDFIdentityUnproven) || res.Body != nil {
				t.Fatalf("not fail closed: err=%v body=%v", err, res.Body)
			}
		})
	}
	for _, tt := range []struct {
		name, script string
		cfg          PDFProvenanceConfig
	}{
		{"output bound", "#!/bin/sh\nyes oversized-output\n", PDFProvenanceConfig{MaxTextBytes: 64, Timeout: 300 * time.Millisecond}},
		{"extractor timeout", "#!/bin/sh\nexec sleep 10\n", PDFProvenanceConfig{Timeout: 100 * time.Millisecond}},
		{"strict extraction contract", "#!/bin/sh\n[ \"$*\" = '-f 1 -l 3 -layout -enc UTF-8 - -' ] || exit 7\nprintf '%s\\n' 'An Improved Quantum Algorithm for Searching an Ordered List' 'Abstract'\n", PDFProvenanceConfig{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controlled-pdftotext")
			if err := os.WriteFile(path, []byte(tt.script), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := tt.cfg
			cfg.PDFToTextPath = path
			res := &FetchResult{Body: bytes.NewReader(pdf)}
			start := time.Now()
			err := VerifyPublishedPDF(context.Background(), res, identity, cfg)
			if tt.name == "strict extraction contract" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrPDFIdentityUnproven) || res.Body != nil || time.Since(start) > 2*time.Second {
				t.Fatalf("unbounded or accepted extractor failure: err=%v elapsed=%v", err, time.Since(start))
			}
		})
	}
}
