package healer

import (
	"fmt"
	"strings"
	"time"

	"github.com/wmid/swarm-hpa/internal/core/model"
)

// Constraint operators understood by the detector. Anything else is treated as
// unparseable and conservatively ignored (see Detect / nodeSatisfies).
const (
	opEq  = "=="
	opNeq = "!="
)

// Constraint key prefixes/keys the detector can evaluate. Keys outside this set
// (e.g. node.role, engine.labels.*, platform.*) are unknown and never used to
// exclude a node — see nodeSatisfies.
const (
	keyHostname    = "node.hostname"
	keyID          = "node.id"
	labelKeyPrefix = "node.labels."
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
		if n.IsActive() && nodeSatisfies(n, svc.Constraints) {
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

// nodeSatisfies reports whether the node meets every placement constraint that
// the detector can evaluate. Constraints it cannot parse, or whose key it does
// not recognise, are skipped rather than treated as failures — this avoids
// silently widening exclusion for exotic constraint forms. A parseable,
// recognised constraint the node violates makes the whole node unsatisfying.
func nodeSatisfies(n model.NodeView, constraints []string) bool {
	for _, c := range constraints {
		key, op, value, ok := parseConstraint(c)
		if !ok {
			continue // unparseable: never exclude
		}
		actual, known := nodeValue(n, key)
		if !known {
			continue // key we cannot evaluate: never exclude
		}
		equal := actual == value
		switch op {
		case opEq:
			if !equal {
				return false
			}
		case opNeq:
			if equal {
				return false
			}
		}
	}
	return true
}

// nodeValue resolves the node attribute addressed by a constraint key, and
// whether the detector knows how to evaluate that key. A missing node.labels.*
// key yields ("", true): the node is known not to carry the label, so an `==`
// constraint fails and a `!=` constraint holds — matching Swarm's semantics.
func nodeValue(n model.NodeView, key string) (value string, known bool) {
	switch {
	case key == keyHostname:
		return n.Name, true
	case key == keyID:
		return n.ID, true
	case strings.HasPrefix(key, labelKeyPrefix):
		return n.Labels[strings.TrimPrefix(key, labelKeyPrefix)], true
	default:
		return "", false
	}
}

// parseConstraint splits a Docker placement constraint of the simple equality
// form "<key> <op> <value>" (e.g. "node.labels.nodeNum == 1") into its parts.
// Surrounding whitespace is trimmed. ok is false when no ==/!= operator is found
// or either side is empty, in which case the caller ignores the constraint.
func parseConstraint(raw string) (key, op, value string, ok bool) {
	for _, o := range []string{opEq, opNeq} {
		if i := strings.Index(raw, o); i >= 0 {
			key = strings.TrimSpace(raw[:i])
			value = strings.TrimSpace(raw[i+len(o):])
			if key == "" || value == "" {
				return "", "", "", false
			}
			return key, o, value, true
		}
	}
	return "", "", "", false
}
