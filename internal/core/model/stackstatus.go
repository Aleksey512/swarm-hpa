package model

import "time"

// StackStatus is the runtime status of one GitOps stack, written by the sync loop
// after each sync attempt and read by the GET /stacks API. DesiredReplicas is the
// non-autoscaled, non-global replica snapshot taken from the last successful render
// (empty before the first render); the drift check compares it against live Swarm
// state on demand.
//
// Repo is the key into the repos map (the name written in repos.yaml) that backs
// this stack — surfaced in the /stacks UI so the source repo is visible per stack.
//
// State is the TRANSIENT sync state of the current (in-flight) sync pass, written
// live by the loop so the /stacks UI can show which stacks sync in parallel vs
// serialize on a shared repo. It is a best-effort overlay: a Snapshot taken during
// a pass catches one instant. Values:
//   - ""        idle: not currently syncing (between ticks, or never synced)
//   - "syncing" a sync pass is past the repo lock and running for this stack
//   - "waiting" blocked on the shared-repo lock because another stack on the same
//     repo is syncing (one on-disk worktree per repo forces serialization)
//
// State is reset to "" by the full SetStatus write at the END of a sync, so the
// last result (Revision/OK/ErrorStage/...) stays visible between ticks. The loop
// uses the partial SetState update to flip waiting→syncing mid-pass without
// clobbering that last result.
type StackStatus struct {
	Name            string
	Repo            string // repos.yaml key backing this stack (for the /stacks UI)
	State           string // transient: ""|"syncing"|"waiting" — see struct doc
	Revision        string
	OK              bool
	ErrorStage      string // failing stage when OK is false: git|render|secrets|rotate|deploy
	ErrorMessage    string
	LastSync        time.Time
	DeployCount     uint64
	DesiredReplicas map[string]uint64 // service name → desired replicas (non-autoscaled, non-global)
	// Files is the per-GROUP deploy outcome of the last sync, in deploy order
	// (multi-compose stacks). Empty when the stack has not reached the deploy
	// stage this tick (e.g. a git/render/secrets/rotate failure) or has never
	// synced. See StackFileStatus for the Status state machine.
	Files []StackFileStatus
}

// StackFileStatus is the deploy outcome of ONE merge group of a (possibly
// multi-file) stack, surfaced in the GET /stacks UI. One entry = one
// `docker stack deploy`: the base File plus any Overrides merged into it, so the
// Status state machine below is per GROUP, not per individual file. Status is:
//   - ""        pending: not attempted this tick (the stack failed before deploy,
//     or has not synced yet)
//   - "ok"      deployed successfully this tick
//   - "failed"  this file's deploy errored; Error holds why
//   - "skipped" not reached because an earlier group failed — sequential deploys
//     stop at the first failure (Swarm deploys are additive and NOT
//     transactional, so groups before the failure are already applied)
//
// PullPolicy is the effective image pull policy used for this group's deploy
// (precedence: file → stack → global), populated up front from the stack config.
type StackFileStatus struct {
	File string
	// Overrides are the compose files merged into THIS file's deploy, in `-c`
	// order (docker/cli merges them last-wins). Empty for a plain single-file
	// deploy.
	Overrides  []string
	PullPolicy string
	Status     string
	Error      string
}

// ServiceDrift is the per-service result of comparing desired replicas against
// live Swarm replicas. Only non-autoscaled, non-global services are evaluated:
// autoscaler-owned services intentionally diverge (the HPA scales them) and global
// services have no replica count, so neither is reported as drift.
type ServiceDrift struct {
	Service string
	Desired uint64
	Live    uint64
	Drifted bool
}
