// Package stackstatus holds the pure drift computation for the GitOps status API:
// comparing the desired replica snapshot (taken at the last render) against the
// live Swarm services of a stack. It is pure — stdlib + core/model only — so the
// status API adapter can depend on it without crossing the ports-and-adapters
// boundary, and the rule is unit-testable without a live Swarm.
package stackstatus

import (
	"sort"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// Drift compares desired (non-autoscaled, non-global) replicas against the live
// Swarm services of a stack and returns one ServiceDrift per desired service,
// sorted by service name. Drifted is true when a desired service is missing from
// live, is deployed as global (mode mismatch), or has a different live replica
// count. Live services that are not in desired (autoscaled or global services) are
// ignored — their replica divergence is intentional, not drift.
func Drift(desired map[string]uint64, live []model.StackService) []model.ServiceDrift {
	liveByName := make(map[string]model.StackService, len(live))
	for _, s := range live {
		liveByName[s.Name] = s
	}

	out := make([]model.ServiceDrift, 0, len(desired))
	for name, want := range desired {
		ls := liveByName[name]
		_, deployed := liveByName[name]
		drifted := !deployed || !ls.Replicated || ls.Replicas != want
		out = append(out, model.ServiceDrift{
			Service: name,
			Desired: want,
			Live:    ls.Replicas,
			Drifted: drifted,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}
