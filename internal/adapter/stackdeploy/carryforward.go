package stackdeploy

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Aleksey512/swarm-hpa/internal/config"
	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// errNoServices is returned when the rendered compose has no top-level services
// map — i.e. it is not a valid compose document.
var errNoServices = errors.New("carry-forward: compose has no 'services' map")

// ApplyCarryForward rewrites the rendered compose map so services that are
// autoscaler-managed keep their LIVE replica count instead of the compose value.
// This makes `docker stack deploy` a no-op for the autoscaler's replicas — the
// swarm-cd↔HPA replicas conflict dissolves. compose is mutated in place; the
// returned count is how many services were adjusted.
//
// Rules:
//   - A service counts as autoscaler-managed when its COMPOSE labels carry
//     swarm.autoscaler.enabled=true (compose is the source of truth for intent —
//     this respects a human disabling autoscaling in Git on the next sync).
//   - min/max bounds come from the same compose labels (respects in-flight Git
//     changes); an absent/invalid bound means "no bound on that side".
//   - The live replica count comes from live, keyed by short service name.
//   - global-mode services and services with no live counterpart are skipped
//     (first deploy keeps the compose value).
func ApplyCarryForward(compose map[string]any, live []model.StackService, log *slog.Logger) (int, error) {
	services, ok := compose["services"].(map[string]any)
	if !ok {
		return 0, errNoServices
	}
	liveByName := make(map[string]model.StackService, len(live))
	for _, s := range live {
		liveByName[s.Name] = s
	}

	changed := 0
	for name, raw := range services {
		svc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if isGlobalMode(svc) {
			continue
		}
		labels := serviceLabels(svc)
		if labels[config.LabelEnabled] != "true" {
			continue // compose-owned: Git controls its replicas
		}
		ls, hasLive := liveByName[name]
		if !hasLive {
			log.Debug("carry-forward: autoscaled service not yet live; keeping compose replicas", "service", name)
			continue
		}
		if !ls.Replicated {
			continue
		}
		minV := parseUintLabel(labels, config.LabelMin)
		maxV := parseUintLabel(labels, config.LabelMax)
		want := ls.Replicas
		if maxV > 0 && want > maxV {
			want = maxV
		}
		if minV > 0 && want < minV {
			want = minV
		}
		setReplicas(svc, want)
		log.Debug("carry-forward: preserved live replicas",
			"service", name, "live", ls.Replicas, "replicas", want, "min", minV, "max", maxV)
		changed++
	}
	return changed, nil
}

// isGlobalMode reports whether the compose service is mode: global (top-level or
// under deploy:), which has no replicas field.
func isGlobalMode(svc map[string]any) bool {
	if d, ok := svc["deploy"].(map[string]any); ok {
		if m, _ := d["mode"].(string); m == "global" {
			return true
		}
	}
	if m, _ := svc["mode"].(string); m == "global" {
		return true
	}
	return false
}

// parseUintLabel parses a swarm.autoscaler.{min,max} label; a missing or invalid
// value yields 0 (meaning "no bound").
func parseUintLabel(labels map[string]string, key string) uint64 {
	raw, ok := labels[key]
	if !ok || raw == "" {
		return 0
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// setReplicas sets services[name].deploy.replicas, creating the deploy map if absent.
func setReplicas(svc map[string]any, replicas uint64) {
	d, _ := svc["deploy"].(map[string]any)
	if d == nil {
		d = map[string]any{}
		svc["deploy"] = d
	}
	d["replicas"] = replicas
}

// serviceLabels flattens a compose service's deploy.labels + top-level labels into
// a map[string]string, handling both the map form (key: value) and the list form
// (- "key=value"). Deploy labels take precedence over service-level labels.
func serviceLabels(svc map[string]any) map[string]string {
	flat := map[string]any{}
	merge := func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				flat[k] = val
			}
		case []any:
			for _, item := range t {
				s := fmt.Sprint(item)
				if k, vv, ok := strings.Cut(s, "="); ok {
					flat[k] = vv
				} else {
					flat[s] = "true"
				}
			}
		}
	}
	if d, ok := svc["deploy"].(map[string]any); ok {
		merge(d["labels"])
	}
	merge(svc["labels"])

	out := make(map[string]string, len(flat))
	for k, v := range flat {
		out[k] = fmt.Sprint(v)
	}
	return out
}
