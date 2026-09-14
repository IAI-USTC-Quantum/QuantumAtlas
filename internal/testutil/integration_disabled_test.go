//go:build !integration

package testutil

import "testing"

func TestIntegrationDisabledDespiteEnvironment(t *testing.T) {
	for key, value := range map[string]string{
		"QATLAS_TEST_PG_DSN":              "postgres://127.0.0.1:1/qatlas_test?sslmode=disable",
		"TEST_DOWNLOADFLEET_DATABASE_URL": "postgres://127.0.0.1:1/qatlas_test?sslmode=disable",
		"MINERU_LIVE_TEST":                "1",
		"QATLAS_TEST_LIVE":                "1",
	} {
		t.Setenv(key, value)
	}
	if IntegrationEnabled {
		t.Fatal("real-service tests must require -tags integration, regardless of environment")
	}
}
