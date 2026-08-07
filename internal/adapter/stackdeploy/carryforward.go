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

// errNoServices is returned when no document of the group has a top-level
// services map — i.e. the group is not a valid compose stack.
var errNoServices = errors.New("carry-forward: compose has no 'services' map")

// ApplyCarryForward is the single-document form of ApplyCarryForwardGroup, kept
// for callers and tests that deploy one compose file.
func ApplyCarryForward(compose map[string]any, live []model.StackService, log *slog.Logger) (int, error) {
	return ApplyCarryForwardGroup([]map[string]any{compose}, live, log)
}

// ApplyCarryForwardGroup rewrites the documents of one merge group so services
// that are autoscaler-managed keep their LIVE replica count instead of the
// compose value. This makes `docker stack deploy` a no-op for the autoscaler's
// replicas — the swarm-cd↔HPA replicas conflict dissolves. The documents are
// mutated in place; the returned count is how many distinct SERVICES were
// adjusted (not how many documents were rewritten).
//
// docs are the group's compose documents in `-c` order: docs[0] is the base file
// and docs[1:] are its overrides. docker/cli merges them last-wins, which forces
// two things a per-document pass would get wrong:
//
//   - Detection runs on the MERGED view. The swarm.autoscaler.* labels (and the
//     min/max bounds, and mode: global) may be declared only in the base file
//     while an override re-declares the service, or only in an override. Reading
//     one document at a time would either miss the service entirely or read stale
//     bounds.
//   - The replica rewrite is applied to EVERY document that declares the service,
//     with the same value. Whichever document wins the merge, the deployed
//     replica count is the live one. Writing only the base (or only the last
//     document) would let the other side's compose value win.
//
// Rules (unchanged from the single-document behavior):
//   - A service counts as autoscaler-managed when its merged COMPOSE labels carry
//     swarm.autoscaler.enabled=true (compose is the source of truth for intent —
//     this respects a human disabling autoscaling in Git on the next sync).
//   - min/max bounds come from the same merged compose labels (respects in-flight
//     Git changes); an absent/invalid bound means "no bound on that side".
//   - The live replica count comes from live, keyed by short service name.
//   - global-mode services and services with no live counterpart are skipped
//     (first deploy keeps the compose value).
func ApplyCarryForwardGroup(docs []map[string]any, live []model.StackService, log *slog.Logger) (int, error) {
	merged, hasServices := mergeServices(docs)
	if !hasServices {
		return 0, errNoServices
	}
	liveByName := make(map[string]model.StackService, len(live))
	for _, s := range live {
		liveByName[s.Name] = s
	}

	changed := 0
	for name, ms := range merged {
		if ms.global {
			continue
		}
		if ms.labels[config.LabelEnabled] != "true" {
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
		minV := parseUintLabel(ms.labels, config.LabelMin)
		maxV := parseUintLabel(ms.labels, config.LabelMax)
		want := ls.Replicas
		if maxV > 0 && want > maxV {
			want = maxV
		}
		if minV > 0 && want < minV {
			want = minV
		}
		// Write the same value into every document declaring the service so the
		// merge result does not depend on which document wins.
		for _, svc := range ms.decls {
			setReplicas(svc, want)
		}
		log.Debug("carry-forward: preserved live replicas",
			"service", name, "live", ls.Replicas, "replicas", want, "min", minV, "max", maxV,
			"docs_rewritten", len(ms.decls), "label_source_index", ms.labelSourceIdx)
		if ms.labelSourceIdx > 0 {
			// The opt-in label came from an override, not the base file — the
			// exact case a per-document carry-forward would have missed.
			log.Debug("carry-forward: autoscaler label came from an override document",
				"service", name, "doc_index", ms.labelSourceIdx)
		}
		changed++
	}
	return changed, nil
}

// mergedService is the effective view of one service across a merge group: the
// labels/mode docker/cli's last-wins merge would produce, plus every document
// declaration of the service so the replica rewrite can reach all of them.
type mergedService struct {
	labels map[string]string
	global bool
	decls  []map[string]any
	// labelSourceIdx is the index of the last document that set
	// swarm.autoscaler.enabled; -1 when no document sets it.
	labelSourceIdx int
}

// mergeServices folds the group's documents into the per-service view described
// by mergedService, following docker/cli's last-wins merge for labels and mode.
// Documents without a services map (a legitimate override carrying only
// configs:/secrets:/networks:) contribute nothing. hasServices reports whether
// ANY document had a top-level services map — an empty `services: {}` still
// counts, matching the pre-group behavior of not erroring on it.
func mergeServices(docs []map[string]any) (merged map[string]*mergedService, hasServices bool) {
	merged = make(map[string]*mergedService)
	for i, doc := range docs {
		services, ok := doc["services"].(map[string]any)
		if !ok {
			continue
		}
		hasServices = true
		for name, raw := range services {
			svc, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ms, seen := merged[name]
			if !seen {
				ms = &mergedService{labels: map[string]string{}, labelSourceIdx: -1}
				merged[name] = ms
			}
			ms.decls = append(ms.decls, svc)
			for k, v := range serviceLabels(svc) {
				ms.labels[k] = v
				if k == config.LabelEnabled {
					ms.labelSourceIdx = i
				}
			}
			// Only an EXPLICIT mode overrides what an earlier document set; a
			// document that omits mode leaves the previous value in place.
			if mode, ok := explicitMode(svc); ok {
				ms.global = mode == "global"
			}
		}
	}
	return merged, hasServices
}

// explicitMode returns the service's declared mode (deploy.mode takes precedence
// over the top-level mode) and whether one was declared at all.
func explicitMode(svc map[string]any) (string, bool) {
	if d, ok := svc["deploy"].(map[string]any); ok {
		if m, ok := d["mode"].(string); ok && m != "" {
			return m, true
		}
	}
	if m, ok := svc["mode"].(string); ok && m != "" {
		return m, true
	}
	return "", false
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
// (- "key=value").
//
// deploy.labels take precedence over the service's top-level labels when a key
// appears in both, because they are what Swarm stores as SERVICE labels — the
// ones the daemon reads at runtime. A top-level `labels:` key becomes a CONTAINER
// label, which the daemon never sees; it is only honored here as a fallback for
// composes that put the opt-in in the wrong place. gitopsync.flatServiceLabels
// resolves the same conflict the same way, so carry-forward and the drift
// snapshot can never disagree about whether a service is autoscaler-owned.
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
	merge(svc["labels"])
	if d, ok := svc["deploy"].(map[string]any); ok {
		merge(d["labels"]) // last write wins → deploy.labels beat top-level labels
	}

	out := make(map[string]string, len(flat))
	for k, v := range flat {
		out[k] = fmt.Sprint(v)
	}
	return out
}
