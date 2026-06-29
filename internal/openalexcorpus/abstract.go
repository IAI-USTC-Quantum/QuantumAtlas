package openalexcorpus

import (
	"sort"
	"strings"
)

// ReconstructAbstract rebuilds plain abstract text from OpenAlex's
// abstract_inverted_index (a {word: [positions...]} map). It mirrors the
// Python helper in search/qatlas_search/backends/openalex.py
// (_reconstruct_abstract): collect (position, word) pairs, sort by
// position, join with spaces. Returns "" when the index is empty or nil
// (OpenAlex omits the field for records without an abstract).
//
// OpenAlex does not ship the plain abstract — only this inverted index —
// for licensing reasons; reconstruction is lossy on exact whitespace but
// faithful on word order, which is what downstream embedding / search
// needs.
func ReconstructAbstract(inverted map[string][]int) string {
	if len(inverted) == 0 {
		return ""
	}
	type posWord struct {
		pos  int
		word string
	}
	positions := make([]posWord, 0, len(inverted))
	for word, idxs := range inverted {
		for _, i := range idxs {
			positions = append(positions, posWord{pos: i, word: word})
		}
	}
	if len(positions) == 0 {
		return ""
	}
	// Stable on equal positions so a deterministic order is produced even
	// if OpenAlex ever emits a duplicate index (it shouldn't).
	sort.SliceStable(positions, func(a, b int) bool {
		return positions[a].pos < positions[b].pos
	})
	var b strings.Builder
	for i, pw := range positions {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(pw.word)
	}
	return b.String()
}
