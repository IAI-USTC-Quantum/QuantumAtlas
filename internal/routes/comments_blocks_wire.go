package routes

import (
	"context"
	"errors"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/comments"
)

// blockDiscussionsLister embeds the Q2 discussion list into the Q1
// combined-read block response (plan §12.2: the single-block GET is the
// combined read "source+anchor+content+discussions").
//
// It is installed by RegisterComments rather than threaded through
// RegisterPapers' already-long signature. Boot order guarantees safety:
// RegisterPapers mounts the routes, RegisterComments installs the lister
// during the same serve-setup phase, and HTTP traffic only arrives after
// setup completes — so the write happens-before any read. When comments
// are not configured (nil store) the placeholder response stays.
type blockDiscussionsLister func(ctx context.Context, paperID, parseRevision string, pageIdx, blockIndex, limit int) (items []comments.Discussion, nextCursor string, err error)

var blockDiscussions blockDiscussionsLister

// combinedBlockDiscussionsLimit caps the discussions embedded in the
// combined read; the dedicated list endpoint handles deeper paging.
const combinedBlockDiscussionsLimit = 20

// installBlockDiscussionsLister wires the Q2 store into the Q1 block
// endpoint. Call with a nil store to restore the placeholder.
func installBlockDiscussionsLister(store comments.Store) {
	if store == nil {
		blockDiscussions = nil
		return
	}
	blockDiscussions = func(ctx context.Context, paperID, parseRevision string, pageIdx, blockIndex, limit int) ([]comments.Discussion, string, error) {
		if limit <= 0 || limit > combinedBlockDiscussionsLimit {
			limit = combinedBlockDiscussionsLimit
		}
		items, err := store.ListDiscussions(ctx, comments.ListFilter{
			PaperID:       paperID,
			ParseRevision: parseRevision,
			PageIdx:       &pageIdx,
			BlockIndex:    &blockIndex,
			Limit:         limit,
		})
		if err != nil {
			if errors.Is(err, comments.ErrUnavailable) {
				return nil, "", err
			}
			return nil, "", err
		}
		var next string
		if len(items) == limit {
			next = items[len(items)-1].DiscussionID
		}
		return items, next, nil
	}
}
