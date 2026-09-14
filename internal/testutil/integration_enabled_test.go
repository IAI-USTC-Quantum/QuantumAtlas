//go:build integration

package testutil

import "testing"

func TestIntegrationEnabledByBuildTag(t *testing.T) {
	if !IntegrationEnabled {
		t.Fatal("-tags integration must enable the real-service tests' environment checks")
	}
}
