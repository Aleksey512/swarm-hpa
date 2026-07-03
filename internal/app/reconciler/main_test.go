package reconciler

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in this package under a goroutine-leak guard. The
// reconcile loop (Run) and its test harness (runUntilCancel) spawn goroutines;
// goleak fails the run if any survive after the tests complete, catching a loop
// or ticker that does not stop on context cancellation.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
