package pdfraster

// pdfraster_test.go: crop math is pure Go and always tested; the
// external-renderer path exercises the real fixture PDF when a
// rasterizer (pdftoppm) is installed and SKIPS honestly otherwise —
// a skip is recorded as a 未跑项, never counted as a passing crop.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const fixturePDF = "../../tests/fixtures/blockcomments/minimal-2page.pdf"

func rendererAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath(DefaultCommand); err != nil {
		return false
	}
	return true
}

func TestCropPNG_Math(t *testing.T) {
	// 1000x2000 image; bbox halves.
	img := image.NewNRGBA(image.Rect(0, 0, 1000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 1000; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	crop, err := CropPNG(buf.Bytes(), [4]float64{0.1, 0.25, 0.6, 0.5})
	if err != nil {
		t.Fatalf("CropPNG: %v", err)
	}
	got, err := png.Decode(bytes.NewReader(crop))
	if err != nil {
		t.Fatalf("decode crop: %v", err)
	}
	// x: 100..600 → 500 px, y: 500..1000 → 500 px.
	if got.Bounds().Dx() != 500 || got.Bounds().Dy() != 500 {
		t.Fatalf("crop size = %dx%d, want 500x500", got.Bounds().Dx(), got.Bounds().Dy())
	}
	// Pixel content must come from the original region (top-left of the
	// crop == original at (100,500)). Compare through the color.Model so
	// a decoded NRGBA vs source NRGBA representation difference cannot
	// fake a mismatch.
	want := img.At(100, 500)
	if color.NRGBAModel.Convert(got.At(0, 0)) != color.NRGBAModel.Convert(want) {
		t.Errorf("crop(0,0) = %v, want original(100,500) = %v", got.At(0, 0), want)
	}
}

func TestCropPNG_ClampsTinyBBox(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	crop, err := CropPNG(buf.Bytes(), [4]float64{0.5, 0.5, 0.5, 0.5})
	if err != nil {
		t.Fatalf("CropPNG degenerate: %v", err)
	}
	got, err := png.Decode(bytes.NewReader(crop))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Bounds().Dx() < 1 || got.Bounds().Dy() < 1 {
		t.Fatalf("degenerate crop must still be ≥1px, got %v", got.Bounds())
	}
}

func TestRenderPagePNG_MissingRenderer(t *testing.T) {
	// A command that can never exist exercises the honest-unavailable
	// path regardless of what is installed.
	_, err := RenderPagePNG(context.Background(), []byte("%PDF-1.4"), 0, 150, "/nonexistent/qatlas-rasterizer-xyz")
	if !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("err = %v, want ErrRendererUnavailable", err)
	}
}

func TestRenderCropPNG_FixturePage(t *testing.T) {
	if !rendererAvailable(t) {
		t.Skipf("%s not installed — positive render/crop path not exercised here (recorded as 未跑项)", DefaultCommand)
	}
	pdf, err := os.ReadFile(fixturePDF)
	if err != nil {
		t.Fatalf("read fixture pdf: %v", err)
	}
	// Page 2 (0-based idx 1), full-page bbox.
	ctx := context.Background()
	full, err := RenderPagePNG(ctx, pdf, 1, 150, "")
	if err != nil {
		t.Fatalf("render page 2: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(full))
	if err != nil {
		t.Fatalf("decode full page: %v", err)
	}
	// US Letter 612x792 pt @150dpi → 1275x1650 (± a couple px across
	// poppler builds).
	if dx, dy := img.Bounds().Dx(), img.Bounds().Dy(); dx < 1270 || dx > 1280 || dy < 1645 || dy > 1655 {
		t.Errorf("rendered size = %dx%d, want ≈1275x1650", dx, dy)
	}

	// Crop the same bbox golden case parse-A page 1 block 1 uses
	// ([0.1,0.08,0.55,0.12] on page idx 0).
	crop, err := RenderCropPNG(ctx, pdf, 0, [4]float64{0.1, 0.08, 0.55, 0.12}, 150, "")
	if err != nil {
		t.Fatalf("render crop: %v", err)
	}
	cimg, err := png.Decode(bytes.NewReader(crop))
	if err != nil {
		t.Fatalf("decode crop: %v", err)
	}
	// 1275x1650 page → x 127..701 (574), y 132..198 (66).
	if dx, dy := cimg.Bounds().Dx(), cimg.Bounds().Dy(); dx != 701-127 || dy != 198-132 {
		t.Errorf("crop size = %dx%d, want 574x66", dx, dy)
	}
	if !bytes.HasPrefix(crop, []byte("\x89PNG")) {
		t.Error("crop output is not a PNG")
	}
}

func TestRenderPagePNG_PageOutOfRange(t *testing.T) {
	if !rendererAvailable(t) {
		t.Skipf("%s not installed — page-out-of-range path not exercised (recorded as 未跑项)", DefaultCommand)
	}
	pdf, err := os.ReadFile(filepath.Clean(fixturePDF))
	if err != nil {
		t.Fatalf("read fixture pdf: %v", err)
	}
	if _, err := RenderPagePNG(context.Background(), pdf, 9, 150, ""); !errors.Is(err, ErrPageOutOfRange) {
		t.Fatalf("err = %v, want ErrPageOutOfRange", err)
	}
}
