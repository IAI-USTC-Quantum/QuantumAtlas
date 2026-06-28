package verifications

import "testing"

func TestEncodePayload(t *testing.T) {
	got := EncodePayload(map[string]any{"sorry_free": true})
	if got != `{"sorry_free":true}` {
		t.Fatalf("EncodePayload = %q", got)
	}
	if got := EncodePayload(nil); got != "" {
		t.Fatalf("EncodePayload(nil) = %q", got)
	}
}
