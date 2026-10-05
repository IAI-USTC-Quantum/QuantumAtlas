package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise real HTTP transport: a Unicode string in a Header map does not
// establish how requests/http.client (Latin-1 header decoding) will display it.
func TestResolutionDefaultsHeadersASCII(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(http.ResponseWriter, *http.Request)
		want  string
	}{
		{"paper-id", func(w http.ResponseWriter, _ *http.Request) {
			applyResolutionHeaders(w, computeResolution("qa_test", "2501.00010v2", "2501.00010v2"))
		}, "qa_test -> arxiv id 2501.00010v2"},
		{"doi", func(w http.ResponseWriter, _ *http.Request) {
			applyResolutionHeaders(w, computeResolution("10.1103/test", "2501.00010", "2501.00010v2"))
		}, "DOI -> arxiv id 2501.00010"},
		{"old-style-category", func(w http.ResponseWriter, _ *http.Request) {
			applyResolutionHeaders(w, computeResolution("9508027", "9508027", "9508027v2"))
		}, "section 3.1"},
		{"paper-alias", func(w http.ResponseWriter, _ *http.Request) {
			applyResolutionHeaders(w, paperResolution("qa_alias", "qa_canonical").resolution)
		}, "qa_alias -> qa_canonical"},
		{"doi-canonical", func(w http.ResponseWriter, r *http.Request) {
			applyDOICanonicalHeaders(newTestReqEvent(r, w), "2501.00010v2", "10.1103/test", "2501.00010v2")
		}, "2501.00010v2 -> DOI 10.1103/test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.apply(w, r)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			header := response.Header.Get("X-QAtlas-Defaults-Applied")
			if !strings.Contains(header, tc.want) {
				t.Errorf("defaults = %q; want %q", header, tc.want)
			}
			for _, b := range []byte(header) {
				if b < 0x20 || b > 0x7e {
					t.Fatalf("defaults header must use printable ASCII, got %q", header)
				}
			}
		})
	}
}
