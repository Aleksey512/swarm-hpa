package stackstatus

import (
	"reflect"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestOrphans(t *testing.T) {
	tests := []struct {
		name      string
		stacks    []string
		live      []model.LiveService
		wantNames []string
	}{
		{
			name:      "empty inputs",
			stacks:    nil,
			live:      nil,
			wantNames: nil,
		},
		{
			name:   "service of a configured stack is not an orphan",
			stacks: []string{"admin"},
			live: []model.LiveService{
				{Name: "admin_analytics", StackNamespace: "admin"},
			},
			wantNames: nil,
		},
		{
			name:   "bare docker service create leftover is an orphan",
			stacks: []string{"admin"},
			live: []model.LiveService{
				{Name: "whoami", StackNamespace: "", Labels: nil},
			},
			wantNames: []string{"whoami"},
		},
		{
			name:   "service of an unknown stack namespace is an orphan",
			stacks: []string{"admin"},
			live: []model.LiveService{
				{Name: "legacy_nginx", StackNamespace: "legacy", Labels: map[string]string{
					"com.docker.stack.namespace": "legacy",
				}},
			},
			wantNames: []string{"legacy_nginx"},
		},
		{
			name:   "autoscaler-managed non-stack service is not an orphan",
			stacks: []string{"admin"},
			live: []model.LiveService{
				{Name: "sidecar", StackNamespace: "", Labels: map[string]string{
					"swarm.autoscaler.enabled": "true",
				}},
			},
			wantNames: nil,
		},
		{
			name:   "heal-only service is not an orphan",
			stacks: []string{"admin"},
			live: []model.LiveService{
				{Name: "postgres-pin", StackNamespace: "", Labels: map[string]string{
					"swarm.autoscaler.heal": "true",
				}},
			},
			wantNames: nil,
		},
		{
			name:   "mixed cluster — only true orphans survive, sorted by name",
			stacks: []string{"admin", "monitoring"},
			live: []model.LiveService{
				{Name: "zombie", StackNamespace: ""},
				{Name: "monitoring_grafana", StackNamespace: "monitoring"},
				{Name: "admin_admin_analytics", StackNamespace: "admin"},
				{Name: "old-stack_api", StackNamespace: "old-stack"},
				{Name: "pinned", StackNamespace: "", Labels: map[string]string{
					"swarm.autoscaler.enabled": "false",
				}},
			},
			wantNames: []string{"old-stack_api", "zombie"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Orphans(tt.stacks, tt.live)
			gotNames := make([]string, 0, len(got))
			for _, s := range got {
				gotNames = append(gotNames, s.Name)
			}
			if tt.wantNames == nil {
				tt.wantNames = []string{}
			}
			if !reflect.DeepEqual(gotNames, tt.wantNames) {
				t.Errorf("Orphans() names = %v, want %v", gotNames, tt.wantNames)
			}
		})
	}
}

func TestManagedByDaemon(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"nil labels", nil, false},
		{"empty labels", map[string]string{}, false},
		{"exact prefix only is not an opt-in", map[string]string{"swarm.autoscaler": "x"}, false},
		{"enabled label", map[string]string{"swarm.autoscaler.enabled": "true"}, true},
		{"unrelated label with similar suffix", map[string]string{"not.swarm.autoscaler.enabled": "true"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := managedByDaemon(tt.labels); got != tt.want {
				t.Errorf("managedByDaemon(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}
