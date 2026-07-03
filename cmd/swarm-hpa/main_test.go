package main

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs this package's tests under a goroutine-leak guard. It is the
// single TestMain for both the default build and the `integration` build
// (integration_test.go adds tests but no TestMain of its own). The integration
// harness starts the /metrics server and the reconcile loop, so goleak verifies
// that a graceful shutdown leaves no goroutines behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
