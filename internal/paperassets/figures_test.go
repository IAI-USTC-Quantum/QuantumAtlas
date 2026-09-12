package paperassets

// Tests for ExtractFigures: the multi-panel Willow pattern (consecutive
// images + one caption below, blank lines in between), caption-above,
// isolated caption-less images, the 12-line caption window, and the
// no-match payloads (old-style / empty markdown).

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func sha(n int) string {
	return strings.Repeat("a", 63) + string(rune('0'+n%10))
}

func TestExtractFigures_MultiPanelCaptionBelow(t *testing.T) {
	// Willow-paper style: three consecutive panels, blank line, one
	// shared caption below.
	md := strings.Join([]string{
		"# Benchmark results",
		"",
		"We compare the surface-code schemes below.",
		"",
		"![](images/" + sha(1) + ".jpg)",
		"![](images/" + sha(2) + ".jpg)",
		"",
		"![](images/" + sha(3) + ".jpg)",
		"",
		"FIG. 1. Surface code performance. Logical error rate per round as a",
		"function of distance.",
		"",
		"Next section starts here.",
	}, "\n")

	figs := ExtractFigures(md)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1 (one multi-panel group): %+v", len(figs), figs)
	}
	f := figs[0]
	wantImages := []string{sha(1) + ".jpg", sha(2) + ".jpg", sha(3) + ".jpg"}
	if !reflect.DeepEqual(f.Images, wantImages) {
		t.Errorf("images = %v, want %v", f.Images, wantImages)
	}
	if f.FigNo != 1 {
		t.Errorf("fig_no = %d, want 1", f.FigNo)
	}
	if want := "FIG. 1. Surface code performance. Logical error rate per round as a"; f.Caption != want {
		t.Errorf("caption = %q, want %q", f.Caption, want)
	}
	if want := "We compare the surface-code schemes below."; f.Context != want {
		t.Errorf("context = %q, want %q", f.Context, want)
	}
}

func TestExtractFigures_CaptionAbove(t *testing.T) {
	md := strings.Join([]string{
		"Some preceding prose line.",
		"",
		"Figure 2. Circuit layout for the magic-state distillation.",
		"",
		"![](images/" + sha(4) + ".png)",
		"",
		"Trailing prose.",
	}, "\n")

	figs := ExtractFigures(md)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1: %+v", len(figs), figs)
	}
	f := figs[0]
	if f.FigNo != 2 {
		t.Errorf("fig_no = %d, want 2", f.FigNo)
	}
	if want := "Figure 2. Circuit layout for the magic-state distillation."; f.Caption != want {
		t.Errorf("caption = %q, want %q", f.Caption, want)
	}
	// The caption line itself is skipped for context.
	if want := "Some preceding prose line."; f.Context != want {
		t.Errorf("context = %q, want %q", f.Context, want)
	}
}

func TestExtractFigures_IsolatedImageNoCaption(t *testing.T) {
	md := strings.Join([]string{
		"Intro text.",
		"",
		"![](images/" + sha(5) + ".gif)",
		"",
		"A paragraph of ordinary prose follows the image and is long enough",
		"that it clearly is not a caption.",
	}, "\n")

	figs := ExtractFigures(md)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1: %+v", len(figs), figs)
	}
	f := figs[0]
	if f.Caption != "" || f.FigNo != 0 {
		t.Errorf("caption = %q, fig_no = %d, want empty/0", f.Caption, f.FigNo)
	}
	if want := "Intro text."; f.Context != want {
		t.Errorf("context = %q, want %q", f.Context, want)
	}
}

func TestExtractFigures_NoImages(t *testing.T) {
	for name, md := range map[string]string{
		"empty":        "",
		"prose only":   "# Title\n\nJust text, no figures.\n",
		"old payload":  "PDF downloaded from arxiv\n\nbody text\n",
		"bad sha":      "![](images/notashortcut.jpg)\n",
		"bad ext":      "![](images/" + sha(1) + ".tiff)\n",
		"uppercase":    "![](images/" + strings.ToUpper(sha(1)) + ".jpg)\n",
		"other prefix": "![](assets/" + sha(1) + ".jpg)\n",
	} {
		if figs := ExtractFigures(md); len(figs) != 0 {
			t.Errorf("%s: figures = %+v, want none", name, figs)
		}
	}
}

func TestExtractFigures_CaptionBeyondWindow(t *testing.T) {
	// The caption sits 13 lines below the image: outside the 12-line
	// window, so Caption must stay empty.
	pad := make([]string, 0, 13)
	for i := 0; i < 12; i++ {
		pad = append(pad, fmt.Sprintf("filler line %d", i))
	}
	md := strings.Join(append([]string{
		"![](images/" + sha(6) + ".jpg)",
		"",
	}, append(pad, "FIG. 7. Too far away.")...), "\n")

	figs := ExtractFigures(md)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1: %+v", len(figs), figs)
	}
	if figs[0].Caption != "" || figs[0].FigNo != 0 {
		t.Errorf("caption = %q fig_no = %d, want empty/0 (outside 12-line window)",
			figs[0].Caption, figs[0].FigNo)
	}
}

func TestExtractFigures_LongProseStopsCaptionScan(t *testing.T) {
	// A >200-char prose line between the image and a nearer-in-lines
	// caption stops the downward scan.
	long := strings.Repeat("x", 201)
	md := strings.Join([]string{
		"![](images/" + sha(7) + ".jpg)",
		"",
		long,
		"",
		"FIG. 9. Should not be attached.",
	}, "\n")

	figs := ExtractFigures(md)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1: %+v", len(figs), figs)
	}
	if figs[0].Caption != "" {
		t.Errorf("caption = %q, want empty (scan stopped at long prose)", figs[0].Caption)
	}
}

func TestExtractFigures_TwoGroups(t *testing.T) {
	md := strings.Join([]string{
		"First context line.",
		"![](images/" + sha(1) + ".jpg)",
		"FIG. 1. First figure.",
		"",
		"Second context line.",
		"",
		"![](images/" + sha(2) + ".png)",
		"![](images/" + sha(3) + ".png)",
		"图 12. 中文图注。",
	}, "\n")

	figs := ExtractFigures(md)
	if len(figs) != 2 {
		t.Fatalf("figures = %d, want 2: %+v", len(figs), figs)
	}
	if figs[0].FigNo != 1 || figs[0].Caption != "FIG. 1. First figure." {
		t.Errorf("fig[0] = %+v, want FIG. 1 caption", figs[0])
	}
	if figs[1].FigNo != 12 || figs[1].Caption != "图 12. 中文图注。" {
		t.Errorf("fig[1] = %+v, want 图 12 caption", figs[1])
	}
	if !reflect.DeepEqual(figs[1].Images, []string{sha(2) + ".png", sha(3) + ".png"}) {
		t.Errorf("fig[1].images = %v", figs[1].Images)
	}
	if want := "Second context line."; figs[1].Context != want {
		// The FIG. 1 caption of group 1 and this line precede group 2;
		// the nearest non-caption line wins.
		t.Errorf("fig[1].context = %q, want %q", figs[1].Context, want)
	}
}

func TestExtractFigures_ContextTruncated(t *testing.T) {
	long := strings.Repeat("中", 300)
	md := long + "\n![](images/" + sha(1) + ".jpg)\nFIG. 1. x\n"
	figs := ExtractFigures(md)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1", len(figs))
	}
	if got := len([]rune(figs[0].Context)); got != 200 {
		t.Errorf("context runes = %d, want 200", got)
	}
}

func TestExtractFigures_BoldCaptionVariant(t *testing.T) {
	md := "![](images/" + sha(1) + ".jpg)\n**FIG. 3. Bolded caption.**\n"
	figs := ExtractFigures(md)
	if len(figs) != 1 || figs[0].FigNo != 3 {
		t.Fatalf("figures = %+v, want one figure with fig_no 3", figs)
	}
	if want := "**FIG. 3. Bolded caption.**"; figs[0].Caption != want {
		t.Errorf("caption = %q, want %q", figs[0].Caption, want)
	}
}
