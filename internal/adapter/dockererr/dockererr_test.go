package dockererr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/docker/docker/errdefs"
)

func TestIsVersionConflict(t *testing.T) {
	outOfSequence := errors.New("rpc error: code = Unknown desc = update out of sequence")

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"errdefs conflict", errdefs.Conflict(errors.New("version mismatch")), true},
		// The key case: the real error string the daemon emits on a concurrent
		// autoscaler↔deploy ServiceUpdate. errdefs does not classify it as a
		// Conflict (gRPC code=Unknown), so the string match must catch it.
		{"update out of sequence (gRPC code=Unknown)", outOfSequence, true},
		{"bare substring", errors.New("update out of sequence"), true},
		{"wrapped out of sequence", fmt.Errorf("deploy stack: %w", outOfSequence), true},
		{"unrelated network error", errors.New("connection refused"), false},
		{"empty message", errors.New(""), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsVersionConflict(c.err); got != c.want {
				t.Errorf("IsVersionConflict(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
