package gitopsync

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in this package under a goroutine-leak guard. The
// gitops loop (Run) and its tests spawn goroutines; goleak fails the run if any
// survive after the tests complete, catching a loop or ticker that does not stop
// on context cancellation.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
