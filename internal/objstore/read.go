package objstore

import (
	"context"
	"errors"
	"time"
)

// ReadTimeout bounds interactive asset reads, including SDK retries. Background
// uploads/conversions keep their own longer operation deadlines.
const ReadTimeout = 10 * time.Second

// ErrUnavailable means a read failed before presence could be established. It
// must never be treated as ErrNotFound or used to start replacement acquisition.
var ErrUnavailable = errors.New("objstore: unavailable")

func ReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, ReadTimeout)
}
