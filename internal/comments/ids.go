package comments

import (
	"strings"

	"github.com/oklog/ulid/v2"
)

// ID minting for discussions ("cd_") and replies ("cr_").
//
// ulid.DefaultEntropy is a per-process MONOTONIC source: ULIDs minted
// in the same millisecond still strictly increase, so ordering by id
// equals ordering by creation time within this process. That is what
// the keyset cursors (ORDER BY id DESC / ASC) rely on. qatlasd is a
// single process per deployment, so process-local monotonicity is
// sufficient; if a multi-writer deployment ever appears the cursors
// must switch to a (created_at, id) composite key.
//
// Prefixes make the ids self-describing on the wire, mirroring the qa_
// paper-id convention: cd_ = comment discussion, cr_ = comment reply.
func newID(prefix string) string {
	return prefix + strings.ToLower(ulid.MustNew(ulid.Now(), ulid.DefaultEntropy()).String())
}

// NewDiscussionID mints "cd_" + lowercase ULID.
func NewDiscussionID() string { return newID("cd_") }

// NewReplyID mints "cr_" + lowercase ULID.
func NewReplyID() string { return newID("cr_") }
