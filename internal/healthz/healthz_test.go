package healthz

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSanitise_StripsDetailFields locks down the contract that the
// public /api/health response must not leak deployment-topology
// detail (mesh IPs, bucket names, wiki commit, etc.).
//
// If a future refactor adds a new detail field to Check, this test
// will fail by surfacing the new field in the marshalled JSON of a
// sanitised Result — pushing the author to either add the field to
// Sanitise's safelist or explicitly decide it's safe to leak.
func TestSanitise_StripsDetailFields(t *testing.T) {
	dirty := false
	raw := Result{
		Status:        "healthy",
		Version:       "0.10.0",
		UptimeSeconds: 1234,
		Time:          "2026-06-02T00:00:00Z",
		Checks: map[string]Check{
			"rawstore": {
				Status:    "ok",
				LatencyMS: 770,
				Backend:   "s3-router",
				Endpoint:  "http://10.144.18.10:9000",
				Buckets:   []string{"qatlas-pdf", "qatlas-md", "qatlas-images"},
			},
			"neo4j": {
				Status:    "ok",
				LatencyMS: 742,
				URI:       "bolt://10.144.18.10:7687",
				Database:  "neo4j",
			},
			"wiki": {
				Status:     "ok",
				Dir:        "/home/timidly/QuantumAtlas-Wiki",
				Commit:     "38f365b",
				CommitTime: "2026-05-29T13:16:58+08:00",
				Branch:     "main",
				Dirty:      &dirty,
			},
		},
	}

	clean := raw.Sanitise()

	// Top-level passthrough fields stay.
	if clean.Status != "healthy" || clean.Version != "0.10.0" || clean.UptimeSeconds != 1234 || clean.Time != "2026-06-02T00:00:00Z" {
		t.Fatalf("top-level fields not preserved: %+v", clean)
	}

	// Every Check projects to just Status (no error here, no Error field).
	for name, c := range clean.Checks {
		if c.Status != "ok" {
			t.Errorf("check %s status %q, want ok", name, c.Status)
		}
		// Marshal and check the JSON contains no detail keys.
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal check %s: %v", name, err)
		}
		s := string(b)
		// Concrete leak strings from the populated Result above.
		// Any of them surfacing in the sanitised JSON = bug.
		leakSamples := []string{
			"10.144.18.10", "qatlas-pdf", "qatlas-md", "qatlas-images",
			"bolt://", "neo4j", "/home/timidly/", "QuantumAtlas-Wiki",
			"38f365b", "2026-05-29", "main", "dirty",
			"latency_ms", "backend", "endpoint", "uri", "database",
			"dir", "commit", "commit_time", "branch", "error",
		}
		for _, leak := range leakSamples {
			if contains(s, leak) {
				t.Errorf("sanitised check %s JSON %s leaks %q", name, s, leak)
			}
		}
	}
}

// TestSanitise_DropsErrorString locks down the post-v0.12-audit
// contract: degraded probes do NOT leak any Error string on the
// anonymous tier. Raw err.Error() from SDK drivers and our own
// probeRouter "bucket %s: %v" formatter embed bucket names / mesh
// IPs / bolt URIs inline, so keeping Error on the public tier was
// silently defeating the rest of the redaction. The aggregate
// Status ("degraded") + per-check Status ("error") already give
// monitors a usable alert signal without needing the underlying
// topology-tainted cause.
func TestSanitise_DropsErrorString(t *testing.T) {
	raw := Result{
		Status:  "degraded",
		Version: "0.10.0",
		Checks: map[string]Check{
			"neo4j": {
				Status:    "error",
				Error:     "Neo4jError: connection refused to bolt://10.144.18.10:7687",
				URI:       "bolt://10.144.18.10:7687",
				LatencyMS: 5000,
			},
			"rawstore": {
				Status: "error",
				Error:  "bucket qatlas-pdf: HEAD failed: NoSuchBucket",
				Bucket: "qatlas-pdf",
			},
		},
	}
	clean := raw.Sanitise()
	for name, c := range clean.Checks {
		if c.Status != "error" {
			t.Errorf("check %s: status %q, want error (Status must survive)", name, c.Status)
		}
		if c.Error != "" {
			t.Errorf("check %s: Error %q must be empty on anon tier", name, c.Error)
		}
		if c.URI != "" {
			t.Errorf("check %s: URI must be stripped, got %q", name, c.URI)
		}
		if c.Bucket != "" {
			t.Errorf("check %s: Bucket must be stripped, got %q", name, c.Bucket)
		}
		if c.LatencyMS != 0 {
			t.Errorf("check %s: LatencyMS must be stripped, got %d", name, c.LatencyMS)
		}
		// Belt-and-braces: marshal and grep for the leak samples that
		// the raw Error strings contain. Any of these surfacing = bug.
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal check %s: %v", name, err)
		}
		s := string(b)
		for _, leak := range []string{"10.144.18.10", "qatlas-pdf", "bolt://", "NoSuchBucket", "Neo4jError"} {
			if contains(s, leak) {
				t.Errorf("check %s: sanitised JSON %s leaks %q", name, s, leak)
			}
		}
	}
}

// TestSanitise_PBWrap covers SanitisePB end-to-end: code/message
// passthrough, sanitised data, no detail leakage.
func TestSanitise_PBWrap(t *testing.T) {
	raw := PBResult{
		Code:    200,
		Message: "API is healthy.",
		Data: Result{
			Status:  "healthy",
			Version: "0.10.0",
			Checks: map[string]Check{
				"rawstore": {Status: "ok", Endpoint: "http://leak.example.com", Bucket: "secret-bucket"},
			},
		},
	}
	clean := raw.Sanitise()
	if clean.Code != 200 || clean.Message != "API is healthy." {
		t.Fatal("envelope fields dropped")
	}
	b, _ := json.Marshal(clean)
	s := string(b)
	for _, leak := range []string{"leak.example.com", "secret-bucket", "endpoint", "bucket"} {
		if contains(s, leak) {
			t.Errorf("SanitisePB leaks %q in JSON %s", leak, s)
		}
	}
}

// TestSanitise_DoesNotMutateOriginal — Sanitise must return a deep
// copy. Mutating the sanitised Result must not poison the source so
// the same Result can be sent over the wire to both the anon (sanitised)
// and authenticated (raw) callers without one corrupting the other.
func TestSanitise_DoesNotMutateOriginal(t *testing.T) {
	raw := Result{
		Status:  "healthy",
		Version: "0.10.0",
		Checks: map[string]Check{
			"x": {Status: "ok", Bucket: "before"},
		},
	}
	clean := raw.Sanitise()

	// Mutate the clean copy's Checks map values; raw must stay put.
	clean.Checks["x"] = Check{Status: "MUTATED", Bucket: "after"}

	if raw.Checks["x"].Status != "ok" || raw.Checks["x"].Bucket != "before" {
		t.Errorf("Sanitise leaked aliasing: raw mutated to %+v", raw.Checks["x"])
	}
}

// TestSanitise_NilChecks handles the zero value (no probes registered
// yet, e.g. early startup) without panicking.
func TestSanitise_NilChecks(t *testing.T) {
	clean := Result{Status: "healthy", Version: "0.10.0"}.Sanitise()
	if clean.Checks == nil {
		t.Fatal("Sanitise should always produce a non-nil Checks map (even if empty)")
	}
	if !reflect.DeepEqual(clean.Checks, map[string]Check{}) {
		t.Errorf("expected empty map, got %v", clean.Checks)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
