package comments

import (
	"encoding/json"
	"errors"
)

// AnchorValidator checks that a discussion anchor names a real block of
// a real parse revision (plan §4.2 permanent anchor).
//
// Q2 develops fixture-first (plan §12.5): the default production wiring
// passes nil, which means "accept the anchor as declared" — the Q1
// parse_revisions tables do not exist on this branch yet. Q1→Q2
// integration replaces this with a registry-backed validator (and the
// migration grows a real FK). TODO(integration): see plan §8 Q2
// "发布前必须接Q1真实锚点".
type AnchorValidator interface {
	// ValidateBlock returns ErrAnchorNotFound (wrapped is fine) when
	// (paperID, parseRevision, pageIdx, blockIndex) does not name a
	// block in that parse. A nil error accepts the anchor.
	ValidateBlock(paperID, parseRevision string, pageIdx, blockIndex int) error
}

// ErrAnchorNotFound: the requested page/block does not exist in the
// requested parse revision. Golden-anchors semantics (plan §12.5):
// never fall back to a nearest block — a miss is a miss.
var ErrAnchorNotFound = errors.New("comments: anchor block not found in parse revision")

// errAnchorUnknown signals "this validator cannot judge this revision"
// (e.g. the fixture validator seeing an unknown revision id). The route
// layer treats unknown-to-fixture as not-found during the fixture-first
// phase; the integration validator will instead always judge.
type permissiveValidator struct{}

// PermissiveAnchor accepts every anchor. The fixture-first default.
func PermissiveAnchor() AnchorValidator { return permissiveValidator{} }

func (permissiveValidator) ValidateBlock(string, string, int, int) error { return nil }

// fixtureBlock is the minimal docvortex.middle block shape Q2 needs:
// public 1-based index + 0-based page (plan §12.5 fixtures).
type fixtureBlock struct {
	PageIdx int `json:"page_idx"`
	Index   int `json:"index"`
}

type fixtureDoc struct {
	Blocks []fixtureBlock `json:"blocks"`
}

// FixtureValidator validates anchors against in-memory synthetic parse
// documents keyed by revision id. It implements the §12.5 golden-anchor
// contract: block (page_idx, block.index) must exist exactly; no
// nearest-block fallback; a missing page is a miss.
type FixtureValidator struct {
	blocks map[string]map[int][]int // revision → page_idx → block indexes
}

// NewFixtureValidator builds a validator from parse documents keyed by
// the revision id callers will use.
func NewFixtureValidator(docs map[string][]byte) (*FixtureValidator, error) {
	v := &FixtureValidator{blocks: make(map[string]map[int][]int, len(docs))}
	for rev, raw := range docs {
		var doc fixtureDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		pages := make(map[int][]int)
		for _, b := range doc.Blocks {
			pages[b.PageIdx] = append(pages[b.PageIdx], b.Index)
		}
		v.blocks[rev] = pages
	}
	return v, nil
}

// ValidateBlock implements AnchorValidator.
func (v *FixtureValidator) ValidateBlock(_, revision string, pageIdx, blockIndex int) error {
	pages, ok := v.blocks[revision]
	if !ok {
		// Unknown revision: during fixture-first development any
		// revision not in the fixture set is treated as missing so
		// tests (and the golden anchors) exercise the true miss path.
		return ErrAnchorNotFound
	}
	for _, idx := range pages[pageIdx] {
		if idx == blockIndex {
			return nil
		}
	}
	return ErrAnchorNotFound
}
