// Package taskerrors classifies Swarm task status errors into a small,
// bounded set of classes. Raw error strings must never become Prometheus
// labels (cardinality discipline — same precedent as refusing Git revisions
// as labels); every consumer reasons about the classes defined here.
package taskerrors

import "strings"

// Class is a bounded classification of a Swarm task status error. The string
// values are stable: they are used as Prometheus label values and in logs.
type Class string

const (
	// ClassVxlanFileExists is the old Swarm networking bug where a rejected
	// task fails with `error creating vxlan interface: file exists` (usually
	// wrapped in `network sandbox join failed`). The classic post-deploy
	// alert target.
	ClassVxlanFileExists Class = "vxlan_file_exists"

	// ClassNetworkSandboxJoin is any other `network sandbox join failed`
	// error (subnet sandbox join issues that are not the vxlan interface
	// clash).
	ClassNetworkSandboxJoin Class = "network_sandbox_join_failed"

	// ClassOther is everything else — unclassified task errors.
	ClassOther Class = "other"
)

// Substrings that identify the classes. Checked in order: the most specific
// (vxlan) first, then the broader sandbox-join family.
const (
	vxlanFileExists   = "error creating vxlan interface: file exists"
	sandboxJoinFailed = "network sandbox join failed"
)

// Classify maps a raw Swarm task status error string to a Class. It is pure
// and nil-safe: empty or unrecognised text classifies as ClassOther.
func Classify(errText string) Class {
	switch {
	case contains(errText, vxlanFileExists):
		return ClassVxlanFileExists
	case contains(errText, sandboxJoinFailed):
		return ClassNetworkSandboxJoin
	default:
		return ClassOther
	}
}

// contains is a tiny wrapper so the match intent reads at the call site and
// stays trivially swappable (e.g. to a case-insensitive fold if Swarm ever
// changes casing).
func contains(s, substr string) bool {
	return len(substr) > 0 && strings.Contains(s, substr)
}
