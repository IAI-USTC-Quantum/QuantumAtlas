package routes

import (
	"testing"
)

func TestParseAssetKey(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		ok      bool
		kind    string
		arxivID string
		yymm    string
	}{
		{"pdf normal", "pdf/2401/2401.0001v1.pdf", true, "pdf", "2401.0001v1", "2401"},
		{"markdown normal", "markdown/0704/0704.2988v1.md", true, "markdown", "0704.2988v1", "0704"},
		{"json normal", "json/2510/2510.12345v2.json", true, "json", "2510.12345v2", "2510"},
		{"images normal", "images/2401/2401.0001v1/page-001.png", true, "images", "2401.0001v1", "2401"},
		{"images deep", "images/2401/2401.0001v1/sub/dir/x.png", true, "images", "2401.0001v1", "2401"},

		{"index parquet", "index/papers.parquet", false, "", "", ""},
		{"empty key", "", false, "", "", ""},
		{"too few parts", "pdf/foo", false, "", "", ""},
		{"pdf extra path", "pdf/2401/sub/foo.pdf", false, "", "", ""},
		{"unknown kind", "audit/2401/foo.json", false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, arxiv, yymm, ok := parseAssetKey(tc.key)
			if ok != tc.ok {
				t.Errorf("ok=%v want %v (key=%q)", ok, tc.ok, tc.key)
				return
			}
			if !ok {
				return
			}
			if kind != tc.kind || arxiv != tc.arxivID || yymm != tc.yymm {
				t.Errorf("got (kind=%q arxiv=%q yymm=%q) want (%q %q %q)",
					kind, arxiv, yymm, tc.kind, tc.arxivID, tc.yymm)
			}
		})
	}
}

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
