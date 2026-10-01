package routes

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// Storage failures have a distinct, retryable contract. Detailed backend errors
// stay in server logs rather than exposing internal endpoints to readers.
func assetStorageUnavailable(re *core.RequestEvent, err error) error {
	slog.Warn("asset storage read failed", "path", re.Request.URL.Path, "error", err)
	re.Response.Header().Set("Retry-After", "5")
	return re.JSON(http.StatusServiceUnavailable, map[string]any{
		"code":      "asset_store_unavailable",
		"detail":    "asset storage temporarily unavailable; retry shortly",
		"kind":      "retryable",
		"retryable": true,
		"state":     "unavailable",
	})
}

// S3 readers are lazy: a successful Get/HEAD does not establish that the first
// GET succeeds. Read before committing 200 so a transport failure can be 503.
func primeAssetReader(rc io.Reader) (*bufio.Reader, error) {
	reader := bufio.NewReader(rc)
	_, err := reader.Peek(1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return reader, err
}
