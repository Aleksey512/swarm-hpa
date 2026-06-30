package autoscaler

import (
	"math"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// DefaultTolerance is the deadband around the target (mirroring the Kubernetes
// HPA 10% tolerance): when the metric is within +/-10% of target, the replica
// count is held steady to avoid flapping.
const DefaultTolerance = 0.1

// Desired computes the target replica count for a service from its current
// replicas, the current metric value, and its policy.
//
// value and p.Target are expressed in the metric's own units. For the
// dockerstats CPU metric, 100 == one full core (and can exceed 100 on
// multi-core containers), so operators must set swarm.autoscaler.target in
// those units. The result is always clamped to [p.Min, p.Max].
func Desired(current uint64, value float64, p model.ServicePolicy) uint64 {
	switch {
	case p.Target <= 0:
		// Misconfigured policy: never act on the metric, only respect bounds.
		return clamp(current, p.Min, p.Max)
	case current == 0:
		// No running replica to measure from: bring the service up to its min.
		return clamp(p.Min, p.Min, p.Max)
	}

	ratio := value / p.Target
	if math.Abs(ratio-1.0) <= DefaultTolerance {
		// Within the deadband: hold steady (still respecting bounds).
		return clamp(current, p.Min, p.Max)
	}

	desired := uint64(math.Ceil(float64(current) * ratio))
	return clamp(desired, p.Min, p.Max)
}

func clamp(v, lo, hi uint64) uint64 {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}
