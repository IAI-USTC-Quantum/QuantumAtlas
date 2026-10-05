package match

import (
	"errors"
	"fmt"
)

// HTTPError exposes only the upstream status, never its internal URL,
// response body, service token, or database connection details.
type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("match upstream returned HTTP %d", e.Status)
}

// ErrInvalidResponse distinguishes an invalid upstream response from a
// successful query with no matching papers.
var ErrInvalidResponse = errors.New("match upstream returned an invalid response")
