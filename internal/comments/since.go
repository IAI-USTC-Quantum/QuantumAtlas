package comments

// since.go — incremental-sync cursor for the discussion list (plan
// §13.4.5: GET /api/papers/{paper_id}/discussions?since=<cursor>).
//
// The cursor is a two-tuple "<unixmillis>-<discussion_id>" pointing at
// the last discussion the client has already seen (its updated_at +
// discussion_id). A poll returns every discussion whose
// (updated_at, discussion_id) is STRICTLY greater than that position —
// the id half breaks same-millisecond ties so rows minted (or bumped)
// within one millisecond are never skipped, mirroring the keyset
// pagination's monotonic-ULID argument in ids.go.
//
// Deliberately simple: no signing, no opaque encoding, no server
// session. A cursor is just a position; clients may construct one by
// hand from any discussion's updated_at + discussion_id.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrBadSinceCursor marks an undecodable ?since= value (the route
// layer maps it to 400).
var ErrBadSinceCursor = errors.New("comments: malformed since cursor (want <unixmillis>-<discussion_id>)")

// EncodeSinceCursor renders (t, discussionID) as "<millis>-<id>".
func EncodeSinceCursor(t time.Time, discussionID string) string {
	return fmt.Sprintf("%d-%s", t.UnixMilli(), discussionID)
}

// DecodeSinceCursor parses "<millis>-<discussion_id>". The id is
// everything after the FIRST '-' (millis never contains one), so ids
// with dashes survive round-trips. Empty id or a non-numeric millis is
// ErrBadSinceCursor.
func DecodeSinceCursor(s string) (time.Time, string, error) {
	dash := strings.IndexByte(s, '-')
	if dash <= 0 {
		return time.Time{}, "", ErrBadSinceCursor
	}
	millis, err := strconv.ParseInt(s[:dash], 10, 64)
	if err != nil || millis < 0 {
		return time.Time{}, "", ErrBadSinceCursor
	}
	id := s[dash+1:]
	if id == "" {
		return time.Time{}, "", ErrBadSinceCursor
	}
	return time.UnixMilli(millis), id, nil
}

// sinceAfter reports whether (updatedAt, id) is strictly past the
// (sinceT, sinceID) position — the shared in-memory equivalent of the
// PG row-value comparison (date_trunc('ms', updated_at), discussion_id)
// > ($1, $2). Both sides live in MILLISPACE: the row's timestamp is
// floored to the millisecond before comparing, because the cursor
// format only carries whole millis and storage timestamps are finer
// (time.Now() = ns, PG = µs). Without the floor, a row whose
// updated_at has a sub-milli remainder would always compare past its
// own truncated cursor and re-appear on every poll.
func sinceAfter(updatedAt time.Time, id string, sinceT time.Time, sinceID string) bool {
	updatedAt = updatedAt.Truncate(time.Millisecond)
	if !updatedAt.Equal(sinceT) {
		return updatedAt.After(sinceT)
	}
	return id > sinceID
}
