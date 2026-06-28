package theorems

import "testing"

func TestEncodeLines(t *testing.T) {
	if got := EncodeLines([]int{12, 34}); got != "[12,34]" {
		t.Fatalf("EncodeLines = %q", got)
	}
	if got := EncodeLines(nil); got != "" {
		t.Fatalf("EncodeLines(nil) = %q", got)
	}
}
