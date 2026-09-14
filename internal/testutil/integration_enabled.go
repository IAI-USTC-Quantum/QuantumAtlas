//go:build integration

// Package testutil provides opt-in gates for tests that use real services.
// It must only be imported by test files.
package testutil

// IntegrationEnabled permits real-service tests to check their existing explicit
// target/opt-in environment variables. The build tag alone is not enough.
const IntegrationEnabled = true
