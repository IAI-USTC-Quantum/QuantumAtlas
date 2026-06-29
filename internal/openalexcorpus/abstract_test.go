package openalexcorpus

import "testing"

func TestReconstructAbstract(t *testing.T) {
	// "the quick brown fox" via an inverted index (word -> positions).
	inv := map[string][]int{
		"the":   {0},
		"quick": {1},
		"brown": {2},
		"fox":   {3},
	}
	if got, want := ReconstructAbstract(inv), "the quick brown fox"; got != want {
		t.Errorf("ReconstructAbstract = %q, want %q", got, want)
	}
}

func TestReconstructAbstractRepeatedWord(t *testing.T) {
	// A word can appear at multiple positions.
	inv := map[string][]int{
		"to": {0, 2},
		"be": {1, 3},
		"or": {4},
	}
	if got, want := ReconstructAbstract(inv), "to be to be or"; got != want {
		t.Errorf("ReconstructAbstract = %q, want %q", got, want)
	}
}

func TestReconstructAbstractEmpty(t *testing.T) {
	if got := ReconstructAbstract(nil); got != "" {
		t.Errorf("ReconstructAbstract(nil) = %q, want empty", got)
	}
	if got := ReconstructAbstract(map[string][]int{}); got != "" {
		t.Errorf("ReconstructAbstract(empty) = %q, want empty", got)
	}
	if got := ReconstructAbstract(map[string][]int{"x": {}}); got != "" {
		t.Errorf("ReconstructAbstract(word with no positions) = %q, want empty", got)
	}
}
