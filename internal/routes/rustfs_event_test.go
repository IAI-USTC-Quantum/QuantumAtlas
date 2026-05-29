package routes

import (
	"testing"
)

// NB: ParseAssetKey moved to internal/paperindex package; see
// paperindex/scan_test.go::TestParseAssetKey for the test that used
// to live here.

func TestParseRustFSEventsEnvelope(t *testing.T) {
	body := []byte(`{
        "EventName": "s3:ObjectCreated:Put",
        "Key": "qatlas-raw/pdf/2401/2401.0001v1.pdf",
        "Records": [
            {
                "eventName": "s3:ObjectCreated:Put",
                "eventTime": "2026-05-28T20:00:00Z",
                "s3": {
                    "bucket": {"name": "qatlas-raw"},
                    "object": {
                        "key": "pdf/2401/2401.0001v1.pdf",
                        "size": 12345,
                        "eTag": "\"abc123\""
                    }
                }
            }
        ]
    }`)
	events, err := parseRustFSEvents(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events)=%d want 1", len(events))
	}
	e := events[0]
	if e.EventName != "s3:ObjectCreated:Put" || e.Bucket != "qatlas-raw" ||
		e.Key != "pdf/2401/2401.0001v1.pdf" || e.Size != 12345 || e.ETag != "abc123" {
		t.Errorf("event fields wrong: %+v", e)
	}
}

func TestParseRustFSEventsSingleObject(t *testing.T) {
	// Bare Event JSON (no envelope) — RustFS Event::new shape.
	body := []byte(`{
        "eventName": "s3:ObjectRemoved:Delete",
        "eventTime": "2026-05-28T20:00:00Z",
        "s3": {
            "bucket": {"name": "qatlas-raw"},
            "object": {"key": "markdown/0704/0704.2988v1.md"}
        }
    }`)
	events, err := parseRustFSEvents(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events)=%d want 1", len(events))
	}
	if events[0].Key != "markdown/0704/0704.2988v1.md" {
		t.Errorf("got key %q", events[0].Key)
	}
}

func TestParseRustFSEventsURLEncodedKey(t *testing.T) {
	body := []byte(`{
        "Records": [{
            "eventName": "s3:ObjectCreated:Put",
            "s3": {
                "bucket": {"name": "qatlas-raw"},
                "object": {"key": "pdf%2F2401%2F2401.0001v1.pdf"}
            }
        }]
    }`)
	events, err := parseRustFSEvents(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if events[0].Key != "pdf/2401/2401.0001v1.pdf" {
		t.Errorf("url-decode failed: got %q", events[0].Key)
	}
}

func TestParseRustFSEventsRejectGarbage(t *testing.T) {
	_, err := parseRustFSEvents([]byte(`{this is not json`))
	if err == nil {
		t.Errorf("expected parse error on malformed input")
	}
}
