package config

import (
	"fmt"
	"strconv"

	"github.com/wmid/swarm-hpa/internal/core/model"
)

// Label keys for the swarm.autoscaler.* opt-in namespace. A service is managed
// only when it carries these labels — this is the project's hard opt-in boundary.
const (
	LabelEnabled = "swarm.autoscaler.enabled"
	LabelMin     = "swarm.autoscaler.min"
	LabelMax     = "swarm.autoscaler.max"
	LabelMetric  = "swarm.autoscaler.metric"
	LabelTarget  = "swarm.autoscaler.target"
)

// ParsePolicy parses swarm.autoscaler.* labels into a ServicePolicy.
//
// managed is false (with a nil error) when the service has not opted in — the
// enabled label is absent or not exactly "true" — and the caller should skip it
// silently. When managed is true the policy is validated; a non-nil error means
// the service opted in but is misconfigured, and the caller should skip it and
// log the reason.
//
// The function is pure: no logging, no I/O.
func ParsePolicy(labels map[string]string) (policy model.ServicePolicy, managed bool, err error) {
	if labels[LabelEnabled] != "true" {
		return model.ServicePolicy{}, false, nil
	}

	minReplicas, err := parseUintLabel(labels, LabelMin)
	if err != nil {
		return model.ServicePolicy{}, true, err
	}
	maxReplicas, err := parseUintLabel(labels, LabelMax)
	if err != nil {
		return model.ServicePolicy{}, true, err
	}
	target, err := parseFloatLabel(labels, LabelTarget)
	if err != nil {
		return model.ServicePolicy{}, true, err
	}

	policy = model.ServicePolicy{
		Enabled: true,
		Min:     minReplicas,
		Max:     maxReplicas,
		Metric:  labels[LabelMetric],
		Target:  target,
	}
	if err := validatePolicy(policy); err != nil {
		return model.ServicePolicy{}, true, err
	}
	return policy, true, nil
}

func parseUintLabel(labels map[string]string, key string) (uint64, error) {
	raw, ok := labels[key]
	if !ok || raw == "" {
		return 0, fmt.Errorf("%s: required", key)
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	return v, nil
}

func parseFloatLabel(labels map[string]string, key string) (float64, error) {
	raw, ok := labels[key]
	if !ok || raw == "" {
		return 0, fmt.Errorf("%s: required", key)
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	return v, nil
}

func validatePolicy(p model.ServicePolicy) error {
	if p.Max < 1 {
		return fmt.Errorf("%s must be >= 1", LabelMax)
	}
	if p.Min > p.Max {
		return fmt.Errorf("%s (%d) must be <= %s (%d)", LabelMin, p.Min, LabelMax, p.Max)
	}
	if p.Target <= 0 {
		return fmt.Errorf("%s must be > 0, got %g", LabelTarget, p.Target)
	}
	if p.Metric == "" {
		return fmt.Errorf("%s: required", LabelMetric)
	}
	return nil
}
