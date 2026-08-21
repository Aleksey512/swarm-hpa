package stackstatus

import (
	"sort"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// autoscalerLabelPrefix is the opt-in label namespace that brings a service
// under this daemon's management. It mirrors the prefix the config parser
// recognizes; kept local so the pure core does not depend on config.
const autoscalerLabelPrefix = "swarm.autoscaler."

// Orphans returns the live services that belong to NO configured stack and are
// NOT managed by this daemon — leftovers this daemon would neither deploy nor
// scale: a service whose com.docker.stack.namespace label names an unknown
// stack, or a service with no stack namespace at all that also lacks the
// swarm.autoscaler.* opt-in labels.
//
// configuredStacks is the set of stack names from stacks.yaml. Membership is
// decided by the namespace LABEL (authoritative — stamped by docker stack
// deploy), never by name-prefix matching; and never by the desired-replica
// snapshot, which deliberately excludes autoscaled and global services.
func Orphans(configuredStacks []string, live []model.LiveService) []model.LiveService {
	configured := make(map[string]struct{}, len(configuredStacks))
	for _, s := range configuredStacks {
		configured[s] = struct{}{}
	}

	out := make([]model.LiveService, 0)
	for _, svc := range live {
		if _, ok := configured[svc.StackNamespace]; ok {
			continue // belongs to a configured stack
		}
		if managedByDaemon(svc.Labels) {
			continue // autoscaler/healer opt-in: known non-stack workload
		}
		out = append(out, svc)
	}

	// Deterministic order for stable UI diffs and tests.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// managedByDaemon reports whether the service carries any swarm.autoscaler.*
// label — the opt-in that makes a non-stack service a deliberate, observed
// workload rather than an orphan.
func managedByDaemon(labels map[string]string) bool {
	for k := range labels {
		if len(k) > len(autoscalerLabelPrefix) && k[:len(autoscalerLabelPrefix)] == autoscalerLabelPrefix {
			return true
		}
	}
	return false
}
