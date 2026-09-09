package main

import (
	"strings"
	"testing"
	"time"
)

func TestEnvironmentAndFlagPrecedence(t *testing.T) {
	t.Setenv("DL_WORKER_MASTER_URL", "https://master.example/")
	t.Setenv("DL_WORKER_ENROLLMENT_TOKEN", "enrollment-secret")
	t.Setenv("DL_WORKER_NAME", "env-name")
	t.Setenv("DL_WORKER_DATA_DIR", t.TempDir())
	t.Setenv("DL_WORKER_CONCURRENCY", "4")
	t.Setenv("DL_WORKER_MAX_SPOOL_BYTES", "1073741824")
	t.Setenv("DL_WORKER_RESULT_TTL", "48h")
	t.Setenv("DL_BROWSER_CDP_URL", "http://127.0.0.1:9223")
	t.Setenv("DL_UNPAYWALL_EMAIL", "contact@example.org")
	t.Setenv("DL_S2_API_KEY", "s2-secret")
	c, health, err := config([]string{"--concurrency=2", "--name=flag-name"})
	if err != nil {
		t.Fatal(err)
	}
	if health || c.Concurrency != 2 || c.Name != "flag-name" || c.ResultTTL != 48*time.Hour || c.MasterURL != "https://master.example" || c.EnrollmentToken != "enrollment-secret" || c.S2APIKey != "s2-secret" {
		t.Fatal("configuration mismatch")
	}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestMalformedEnvironmentDoesNotEchoValue(t *testing.T) {
	t.Setenv("DL_WORKER_CONCURRENCY", "accidental-secret")
	_, _, err := config(nil)
	if err == nil || strings.Contains(err.Error(), "accidental-secret") {
		t.Fatal("unsafe environment validation")
	}
}
func TestDefaultConcurrencyAndExplicitHTTPFlag(t *testing.T) {
	t.Setenv("DL_WORKER_CONCURRENCY", "")
	t.Setenv("DL_WORKER_MAX_SPOOL_BYTES", "")
	t.Setenv("DL_WORKER_RESULT_TTL", "")
	c, _, err := config([]string{"--master-url=http://127.0.0.1:8080", "--name=test"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Concurrency != 2 || c.TaskTimeout != 6*time.Minute || c.Validate() == nil {
		t.Fatal("defaults or HTTPS requirement incorrect")
	}
	c, _, err = config([]string{"--master-url=http://127.0.0.1:8080", "--name=test", "--allow-http"})
	if err != nil || c.Validate() != nil {
		t.Fatal("explicit development HTTP rejected", err)
	}
}
