// S3Store dual-endpoint constructor wiring tests.
//
// These tests are NOT integration-tagged: minio.New is lazy (no
// network I/O at construction) so we can assert the dual-client
// invariant without a live RustFS. The full presign-against-server
// behaviour is covered by TestS3Store_PresignGetWorksAgainstLiveServer
// in s3_test.go behind the integration tag.

package objstore

import "testing"

func TestNewS3StoreDual_SinglePublicEmpty_NoSeparateClient(t *testing.T) {
	s, err := NewS3StoreDual("http://10.0.0.1:9000", "", "buck", "ak", "sk")
	if err != nil {
		t.Fatalf("NewS3StoreDual: %v", err)
	}
	if s.client == nil {
		t.Fatalf("client must be non-nil")
	}
	if s.presignClient != nil {
		t.Fatalf("presignClient must be nil when public endpoint is empty")
	}
}

func TestNewS3StoreDual_PublicEqualsInternal_NoSeparateClient(t *testing.T) {
	endpoint := "http://10.0.0.1:9000"
	s, err := NewS3StoreDual(endpoint, endpoint, "buck", "ak", "sk")
	if err != nil {
		t.Fatalf("NewS3StoreDual: %v", err)
	}
	if s.presignClient != nil {
		t.Fatalf("presignClient must collapse to nil when public == internal")
	}
}

func TestNewS3StoreDual_PublicDiffers_BuildsSeparateClient(t *testing.T) {
	s, err := NewS3StoreDual(
		"http://internal-rustfs.example:9000",
		"https://public-rustfs.example",
		"buck", "ak", "sk",
	)
	if err != nil {
		t.Fatalf("NewS3StoreDual: %v", err)
	}
	if s.presignClient == nil {
		t.Fatalf("presignClient must be non-nil when public differs from internal")
	}
	if s.presignClient == s.client {
		t.Fatalf("presignClient must be a distinct minio client instance, not the same pointer")
	}
}

func TestNewS3StoreDual_BadPublicEndpoint_Errors(t *testing.T) {
	_, err := NewS3StoreDual(
		"http://10.0.0.1:9000",
		"ftp://nope",
		"buck", "ak", "sk",
	)
	if err == nil {
		t.Fatalf("expected error for bad public scheme")
	}
}

func TestNewS3Store_SingleArgConstructorStillWorks(t *testing.T) {
	// Backwards-compat smoke: callers that haven't migrated to the
	// dual form must keep working with no presign client.
	s, err := NewS3Store("http://10.0.0.1:9000", "buck", "ak", "sk")
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if s.presignClient != nil {
		t.Fatalf("legacy NewS3Store must not configure a presignClient")
	}
}
