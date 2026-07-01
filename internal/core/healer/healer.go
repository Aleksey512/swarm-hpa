package healer

import (
	"fmt"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
	"github.com/Aleksey512/swarm-hpa/internal/core/placement"
)

// Verdict is the result of stuck-task detection for a single service. It is a
// plain value so callers can log the reason and act on Stuck without re-deriving
// anything.
type Verdict struct {
	// Stuck reports whether the service matches the full stuck-pending
	// signature and therefore warrants a force-update.
	Stuck bool
	// Reason is a short, human-readable explanation of the verdict (stuck or
	// not), suitable for structured logs.
	Reason string
	// TaskID is the long-pending task that triggered a stuck verdict (empty
	// when not stuck).
	TaskID string
	// NodeID is the recovered, constraint-satisfying node that makes the task
	// healable (empty when not stuck).
	NodeID string
}

// Detect decides whether a service is genuinely stuck and force-updatable,
// applying the precise 5-point signature from moby/moby#42215. It is pure: a
// deterministic function of its inputs (no I/O, no clock).
//
// A service is Stuck only when ALL of the following hold:
//  1. it carries placement constraints,
//  2. at least one of its tasks is pending while Swarm wants it running, and has
//     been so for at least `threshold` (now - task.Since >= threshold), and
//  3. some cluster node both satisfies every (parseable) constraint AND is now
//     Active+Ready — i.e. the constrained target has recovered.
//
// The signature is deliberately conservative: a false positive force-restarts a
// healthy production service, so the recovered-Active-node requirement keeps the
// bar high. Unparseable or unknown-key constraints never *exclude* a node (we do
// not silently widen exclusion); the Active-node requirement still gates the
// verdict.
func Detect(svc model.ManagedService, tasks []model.TaskView, nodes []model.NodeView, threshold time.Duration, now time.Time) Verdict {
	if len(svc.Constraints) == 0 {
		return Verdict{Reason: "no placement constraints"}
	}

	pending, ok := longestPending(tasks, threshold, now)
	if !ok {
		return Verdict{Reason: fmt.Sprintf("no task pending beyond %s", threshold)}
	}

	for _, n := range nodes {
		if n.IsActive() && placement.NodeSatisfies(n, svc.Constraints) {
			return Verdict{
				Stuck:  true,
				Reason: fmt.Sprintf("task pending >= %s and constraint-satisfying node is Active+Ready", threshold),
				TaskID: pending.ID,
				NodeID: n.ID,
			}
		}
	}

	return Verdict{Reason: "no constraint-satisfying node is Active+Ready"}
}

// longestPending returns the first task that is pending+desired-running and has
// been pending for at least `threshold`, and whether such a task exists.
func longestPending(tasks []model.TaskView, threshold time.Duration, now time.Time) (model.TaskView, bool) {
	for _, t := range tasks {
		if t.IsPending() && now.Sub(t.Since) >= threshold {
			return t, true
		}
	}
	return model.TaskView{}, false
}
