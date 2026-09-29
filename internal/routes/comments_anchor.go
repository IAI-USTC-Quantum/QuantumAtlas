package routes

import (
	"context"
	"errors"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/comments"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// registryAnchorValidator is the Q1→Q2 integration glue (plan §8 Q2
// "发布前必须接Q1真实锚点"): it validates a discussion anchor against
// the real parse_revisions rows and the docvortex.middle artifact they
// point at, reusing the exact resolution path of the Q1 block endpoints
// so a creatable discussion anchor always names a block GET can return.
//
// Semantics follow the §12.5 golden anchors: (paperID, revision,
// page_idx, block_index) must exist exactly; no nearest-block fallback;
// a missing page, revision, paper, or stored artifact bytes is a miss.
type registryAnchorValidator struct {
	catalog blockCatalog
	store   objstore.Store
}

// NewRegistryAnchorValidator wires the production validator. Both
// arguments are required; a nil store makes every anchor "unavailable"
// rather than silently permissive.
func NewRegistryAnchorValidator(catalog blockCatalog, store objstore.Store) comments.AnchorValidator {
	return registryAnchorValidator{catalog: catalog, store: store}
}

func (v registryAnchorValidator) ValidateBlock(paperID, parseRevision string, pageIdx, blockIndex int) error {
	ctx := context.Background()

	// Canonical resolution first: an anchor pinned under a qa_ id that
	// was later merged must still resolve to the surviving paper (the
	// Q1 endpoints behave the same way for reads).
	rp, status, detail := resolveCanonicalPaper(ctx, v.catalog, paperID)
	if status != 0 {
		if status == 503 {
			return fmt.Errorf("anchor validation: %s", detail)
		}
		return fmt.Errorf("%w: %s", comments.ErrAnchorNotFound, detail)
	}

	rev, found, err := v.catalog.GetParseRevision(ctx, rp.canonical, parseRevision)
	if err != nil {
		return fmt.Errorf("anchor validation: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: no parse revision %s under %s", comments.ErrAnchorNotFound, parseRevision, rp.canonical)
	}
	if v.store == nil {
		return errors.New("anchor validation: object store not configured")
	}
	data, err := blockReadVerified(ctx, v.store, rev.ObjstoreKey, rev.ArtifactSha256)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return fmt.Errorf("%w: parse artifact bytes missing (%s)", comments.ErrAnchorNotFound, rev.ObjstoreKey)
		}
		return fmt.Errorf("anchor validation: fetch artifact: %w", err)
	}
	doc, err := mineru.ParseMiddleJSON(data)
	if err != nil {
		return fmt.Errorf("anchor validation: decode artifact: %w", err)
	}
	if _, ok := doc.FindBlock(pageIdx, blockIndex); !ok {
		return fmt.Errorf("%w: page %d block %d absent in %s", comments.ErrAnchorNotFound, pageIdx, blockIndex, parseRevision)
	}
	return nil
}
