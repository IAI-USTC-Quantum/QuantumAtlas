// Package pdfraster renders one page of a PDF to PNG and crops a
// normalized bbox region, using an external rasterizer process
// (default: pdftoppm from poppler-utils).
//
// Why external: the project builds with CGO_ENABLED=0 (Dockerfile +
// goreleaser) and there is no mature pure-Go PDF rasterizer; bundling
// MuPDF via CGO is not an option for this build matrix. The renderer is
// therefore a configured binary invoked with FIXED arguments (the PDF
// travels via a temp file; no request data reaches argv). When the
// binary is not installed the caller must answer honestly (503) — a
// missing renderer is never papered over with a redrawn approximation
// (plan §5.2: 有bbox时块图来自原PDF渲染后裁剪，不伪造).
package pdfraster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrRendererUnavailable marks "the configured rasterizer binary is not
// installed" — callers map it to 503 with a hint, not to a fake image.
var ErrRendererUnavailable = errors.New("pdfraster: renderer command not found")

// ErrPageOutOfRange marks "the PDF has no such page" (pdftoppm exits
// non-zero reporting a page-range error) — callers map it to 404.
var ErrPageOutOfRange = errors.New("pdfraster: page out of range for the source PDF")

// DefaultCommand is the rasterizer used when the operator configured
// nothing (poppler-utils' pdftoppm; Debian/Alpine package poppler-utils).
const DefaultCommand = "pdftoppm"

// DefaultDPI is the rasterization density used when the operator
// configured nothing. 150 dpi on US Letter ≈ 1275×1650 px — enough that
// a cropped block stays legible while remaining cheap to serve.
const DefaultDPI = 150

// RenderPagePNG rasterizes page0 (0-BASED page index) of pdfBytes at
// dpi and returns the full-page PNG. command may be empty (→
// DefaultCommand). The PDF is spooled to a private temp file because
// pdftoppm cannot read the document from stdin.
func RenderPagePNG(ctx context.Context, pdfBytes []byte, page0, dpi int, command string) ([]byte, error) {
	if command == "" {
		command = DefaultCommand
	}
	if dpi <= 0 {
		dpi = DefaultDPI
	}
	if page0 < 0 {
		return nil, ErrPageOutOfRange
	}
	bin, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrRendererUnavailable, command)
	}
	dir, err := os.MkdirTemp("", "qatlas-raster-*")
	if err != nil {
		return nil, fmt.Errorf("pdfraster: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	pdfPath := filepath.Join(dir, "page.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		return nil, fmt.Errorf("pdfraster: spool pdf: %w", err)
	}
	outPrefix := filepath.Join(dir, "page")
	page1 := page0 + 1 // pdftoppm counts pages from 1
	cmd := exec.CommandContext(ctx, bin,
		"-png", "-singlefile",
		"-r", fmt.Sprintf("%d", dpi),
		"-f", fmt.Sprintf("%d", page1),
		"-l", fmt.Sprintf("%d", page1),
		pdfPath, outPrefix,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "Wrong page range") || strings.Contains(msg, "first page out of range") || page1 < 1 {
			return nil, fmt.Errorf("%w: page %d: %s", ErrPageOutOfRange, page1, strings.TrimSpace(msg))
		}
		return nil, fmt.Errorf("pdfraster: %s failed: %v: %s", command, err, strings.TrimSpace(msg))
	}
	pngBytes, err := os.ReadFile(outPrefix + ".png")
	if err != nil {
		return nil, fmt.Errorf("pdfraster: read rendered page: %w", err)
	}
	return pngBytes, nil
}

// CropPNG crops bbox ([x0,y0,x1,y1], each coordinate a [0,1] fraction
// of the rendered width/height) out of a PNG and re-encodes it. The
// crop rect is clamped to the image and always at least 1×1 px.
func CropPNG(pngBytes []byte, bbox [4]float64) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("pdfraster: decode rendered page: %w", err)
	}
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	x0 := clampInt(int(bbox[0]*w), 0, b.Dx()-1)
	y0 := clampInt(int(bbox[1]*h), 0, b.Dy()-1)
	x1 := clampInt(int(bbox[2]*w), x0+1, b.Dx())
	y1 := clampInt(int(bbox[3]*h), y0+1, b.Dy())
	rect := image.Rect(x0, y0, x1, y1)
	crop := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(crop, crop.Bounds(), img, rect.Min, draw.Src)
	var out bytes.Buffer
	if err := png.Encode(&out, crop); err != nil {
		return nil, fmt.Errorf("pdfraster: encode crop: %w", err)
	}
	return out.Bytes(), nil
}

// RenderCropPNG is RenderPagePNG + CropPNG in one call — the shape the
// block-image handler wants.
func RenderCropPNG(ctx context.Context, pdfBytes []byte, page0 int, bbox [4]float64, dpi int, command string) ([]byte, error) {
	page, err := RenderPagePNG(ctx, pdfBytes, page0, dpi, command)
	if err != nil {
		return nil, err
	}
	return CropPNG(page, bbox)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
