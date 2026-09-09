package downloadfleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func testPDF() []byte { return []byte("%PDF-1.7\n" + strings.Repeat(" ", 12<<10) + "\n%%EOF\n") }
func TestDefaultsAndValidation(t *testing.T) {
	c := Config{}.defaults()
	if c.MaxInFlight != 6 || c.MaxWorkerAttempts != 3 || c.TaskTimeout != 15*time.Minute || c.WorkerTimeout != 6*time.Minute || c.LeaseDuration != time.Minute {
		t.Fatal(c)
	}
	if _, e := New(nil, Config{Enabled: true}); e == nil {
		t.Fatal("enabled without pool accepted")
	}
	for _, c := range []Config{{MaxWorkerAttempts: 33}, {MaxInFlight: -1}, {TaskTimeout: 25 * time.Hour}, {LeaseDuration: 7 * time.Minute}, {SpoolMaxBytes: -1}} {
		if _, e := New(nil, c); e == nil {
			t.Fatalf("invalid config accepted: %+v", c)
		}
	}
	if _, e := New(nil, Config{MaxWorkerAttempts: 4, TaskTimeout: time.Hour, WorkerTimeout: 15 * time.Minute, LeaseDuration: 2 * time.Minute}); e != nil {
		t.Fatal("configured above defaults rejected", e)
	}
	s, e := New(nil, Config{})
	if e != nil || s.Enabled() {
		t.Fatalf("disabled: %v %v", s, e)
	}
	if _, e = s.FetchPDF(context.Background(), registry.PaperRef{}); !errors.Is(e, ErrDisabled) {
		t.Fatal(e)
	}
}
func TestIdentityAndResultFencing(t *testing.T) {
	a, e := identity(registry.PaperRef{DOI: "HTTPS://DOI.ORG/10.1234/Foo"})
	if e != nil || a != "doi:10.1234/foo" {
		t.Fatal(a, e)
	}
	a, e = identity(registry.PaperRef{ArxivID: "arXiv:2501.00010v2"})
	if e != nil || a != "arxiv:2501.00010v2" {
		t.Fatal(a, e)
	}
	for _, r := range []registry.PaperRef{{}, {DOI: "10.bad/x"}, {ArxivID: "../../etc/passwd"}} {
		if _, e := identity(r); e == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	doi, canonical, v, e := resultIdentity(registry.PaperRef{ArxivID: "2501.00010"}, wp.ResultMetadata{ArxivCanonical: "2501.00010v3", ArxivVersion: 3})
	if e != nil || doi != "" || canonical != "2501.00010v3" || v != 3 {
		t.Fatal(doi, canonical, v, e)
	}
	for _, tc := range []struct {
		r registry.PaperRef
		m wp.ResultMetadata
	}{
		{registry.PaperRef{ArxivID: "2501.00010"}, wp.ResultMetadata{ArxivCanonical: "2501.00011v1"}},
		{registry.PaperRef{ArxivID: "2501.00010v1"}, wp.ResultMetadata{ArxivCanonical: "2501.00010v2"}},
		{registry.PaperRef{ArxivID: "2501.00010"}, wp.ResultMetadata{ArxivCanonical: "2501.00010v2", ArxivVersion: 3}},
		{registry.PaperRef{DOI: "10.1234/a"}, wp.ResultMetadata{DOI: "10.1234/b"}},
		{registry.PaperRef{DOI: "10.1234/a"}, wp.ResultMetadata{ArxivCanonical: "2501.00010v1"}},
	} {
		if _, _, _, e := resultIdentity(tc.r, tc.m); e == nil {
			t.Fatalf("accepted forged identity %+v", tc)
		}
	}
}
func TestIndependentPDFValidation(t *testing.T) {
	valid := testPDF()
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{{"valid", valid, true}, {"html", []byte(strings.Repeat("<html>", 3000)), false}, {"truncated", valid[:len(valid)-10], false}, {"small", []byte("%PDF-1.7\n%%EOF"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := os.CreateTemp(t.TempDir(), "pdf")
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			if _, e = f.Write(tc.data); e != nil {
				t.Fatal(e)
			}
			if e = validatePDF(f, int64(len(tc.data))); (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}
func TestJSONLimitsAndDisabledHTTP(t *testing.T) {
	for _, body := range []string{`{"limit":1} {"limit":2}`, `{"unknown":1}`, `{"limit":"x"}`, strings.Repeat(" ", 65<<10) + `{}`} {
		var v wp.ClaimRequest
		r := httptest.NewRequest(http.MethodPost, wp.ClaimPath, strings.NewReader(body))
		if e := decodeJSON(httptest.NewRecorder(), r, &v); e == nil {
			t.Fatal("accepted malformed/oversized JSON")
		}
	}
	s, _ := New(nil, Config{})
	w := httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, httptest.NewRequest("POST", wp.RegisterPath, strings.NewReader(`{}`)))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	for _, e := range []error{ErrUnauthorized, ErrForbidden, ErrConflict, ErrNotFound} {
		w := httptest.NewRecorder()
		httpError(w, e)
		if w.Code < 400 || w.Code >= 500 {
			t.Fatal(w.Code)
		}
	}
}
func TestRandomCredentialsAndOutcomeSerialization(t *testing.T) {
	a, b := randomID(), randomID()
	if a == b || !validID.MatchString(a) || len(hash(a)) != 32 || bytes.Equal(hash(a), []byte(a)) {
		t.Fatal("invalid random credential")
	}
	out := downloader.FetchOutcome{Archived: true, Result: &downloader.FetchResult{Size: 123, Sha256: "hash"}, WorkerID: "node", RemoteTaskID: "task"}
	raw, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	var got downloader.FetchOutcome
	if e = json.Unmarshal(raw, &got); e != nil || got.Result.Body != nil || !got.Archived {
		t.Fatal(got, e)
	}
	if validFailure("arbitrary") {
		t.Fatal("unbounded classification accepted")
	}
}
