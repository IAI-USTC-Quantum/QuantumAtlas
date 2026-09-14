//go:build !integration

// Package testutil provides opt-in gates for tests that use real services.
// It must only be imported by test files.
package testutil

// IntegrationEnabled requires the integration build tag. Environment variables
// alone must never enable real-service tests during an ordinary go test run.
const IntegrationEnabled = false
