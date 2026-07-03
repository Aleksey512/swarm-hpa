package config

import (
	"fmt"
	"strconv"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// Label keys for the swarm.autoscaler.* opt-in namespace. A service is managed
// only when it carries these labels — this is the project's hard opt-in boundary.
const (
	LabelEnabled = "swarm.autoscaler.enabled"
	LabelMin     = "swarm.autoscaler.min"
	LabelMax     = "swarm.autoscaler.max"
	LabelMetric  = "swarm.autoscaler.metric"
	LabelTarget  = "swarm.autoscaler.target"
	// LabelSource selects this service's metric provider: dockerstats|prometheus
	// (empty = use the daemon's global default). Routed by the metrics Router.
	LabelSource = "swarm.autoscaler.source"
	// LabelQuery is the PromQL expression used when the source is prometheus.
	LabelQuery = "swarm.autoscaler.query"
	// LabelHeal opts a service into stuck-pending healing independently of
	// autoscaling. When absent, healing follows LabelEnabled (an autoscaled
	// service is healed too). "true" enables heal-only — no autoscaler policy is
	// required, so a placement-pinned singleton can be healed without pretending
	// to autoscale. "false" opts an autoscaled service out of healing.
	LabelHeal = "swarm.autoscaler.heal"
	// LabelRebalance opts a service into load-aware task rebalancing, an
	// independent capability: "true" makes the service eligible for the manager
	// to redistribute its tasks across nodes when node load is skewed. Defaults
	// to false (absent), so rebalancing never touches a service that did not opt
	// in — even one that autoscales or heals.
	LabelRebalance = "swarm.autoscaler.rebalance"
)

// ParsePolicy parses swarm.autoscaler.* labels into a ServicePolicy and resolves
// the two independent opt-ins:
//
//   - autoscale: LabelEnabled=="true" AND a valid min/max/metric/target policy.
//   - heal: LabelHeal when present (parsed as a bool), otherwise defaults to the
//     enabled state (so an autoscaled service is healed too, as before).
//   - rebalance: LabelRebalance when present (parsed as a bool), otherwise false
//     — an independent opt-in that defaults off even for autoscaled services.
//
// A service is "managed" when autoscale || heal || rebalance. When none holds,
// all three flags are false (with a nil error) and the caller should skip it
// silently. A non-nil error means the service opted in but is misconfigured
// (invalid autoscaler policy, or an unparseable heal/rebalance value) and the
// caller should skip it and log the reason.
//
// Heal-only or rebalance-only services need no min/max/metric/target — the
// returned policy is the zero value and must not be used for scaling.
//
// The function is pure: no logging, no I/O.
func ParsePolicy(labels map[string]string) (policy model.ServicePolicy, autoscale, heal, rebalance bool, err error) {
	enabled := labels[LabelEnabled] == "true"

	// Heal defaults to the enabled state; an explicit heal label overrides it.
	heal = enabled
	if raw, ok := labels[LabelHeal]; ok && raw != "" {
		v, perr := strconv.ParseBool(raw)
		if perr != nil {
			return model.ServicePolicy{}, false, false, false, fmt.Errorf("%s=%q: %w", LabelHeal, raw, perr)
		}
		heal = v
	}

	// Rebalance is an independent opt-in defaulting to off.
	if raw, ok := labels[LabelRebalance]; ok && raw != "" {
		v, perr := strconv.ParseBool(raw)
		if perr != nil {
			return model.ServicePolicy{}, false, false, false, fmt.Errorf("%s=%q: %w", LabelRebalance, raw, perr)
		}
		rebalance = v
	}

	if !enabled {
		// Not autoscaled: heal-only, rebalance-only, both, or not opted in at all.
		// No autoscaler policy is required in any of these cases.
		return model.ServicePolicy{}, false, heal, rebalance, nil
	}

	// Autoscaling opted in → a full, valid policy is required.
	minReplicas, err := parseUintLabel(labels, LabelMin)
	if err != nil {
		return model.ServicePolicy{}, false, false, false, err
	}
	maxReplicas, err := parseUintLabel(labels, LabelMax)
	if err != nil {
		return model.ServicePolicy{}, false, false, false, err
	}
	target, err := parseFloatLabel(labels, LabelTarget)
	if err != nil {
		return model.ServicePolicy{}, false, false, false, err
	}

	policy = model.ServicePolicy{
		Enabled: true,
		Min:     minReplicas,
		Max:     maxReplicas,
		Metric:  labels[LabelMetric],
		Target:  target,
		Source:  labels[LabelSource],
		Query:   labels[LabelQuery],
	}
	if err := validatePolicy(policy); err != nil {
		return model.ServicePolicy{}, false, false, false, err
	}
	return policy, true, heal, rebalance, nil
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
	switch p.Source {
	case "", ProviderDockerStats, ProviderPrometheus, ProviderAgents:
		// empty = global default; otherwise must name a known provider.
	default:
		return fmt.Errorf("%s=%q: must be %s, %s or %s", LabelSource, p.Source, ProviderDockerStats, ProviderPrometheus, ProviderAgents)
	}
	return nil
}
