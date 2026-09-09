package downloadfleet

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/pocketbase/pocketbase/tools/router"
)

// net/http may return final bytes together with EOF. PocketBase then rewinds
// its request-body cache, so decoding a second JSON value on that reader would
// mistake the replay for trailing input. Exercise the real framework wrapper.
type eofWithBytes struct{ data []byte }

func (r *eofWithBytes) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}
func (r *eofWithBytes) Close() error { return nil }
func TestDecodeJSONPocketBaseReplay(t *testing.T) {
	valid := `{"id":"test-worker-id-123","name":"campus","secret":"test-secret","enrollment_token":"test-enrollment"}`
	for _, tc := range []struct {
		name, body string
		good       bool
	}{{"single", valid, true}, {"trailing", valid + ` {}`, false}, {"unknown", `{"unexpected":1}`, false}, {"too large", `{"name":"` + strings.Repeat("x", 65<<10) + `"}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, wp.RegisterPath, nil)
			req.Body = &router.RereadableReadCloser{ReadCloser: &eofWithBytes{data: []byte(tc.body)}}
			var parsed wp.RegisterRequest
			err := decodeJSON(httptest.NewRecorder(), req, &parsed)
			if (err == nil) != tc.good {
				t.Fatalf("good=%v err=%v", tc.good, err)
			}
		})
	}
}
