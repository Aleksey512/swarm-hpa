package stackstatus

import (
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestDrift(t *testing.T) {
	cases := []struct {
		name    string
		desired map[string]uint64
		live    []model.StackService
		want    []model.ServiceDrift
	}{
		{
			name:    "empty desired yields empty",
			desired: nil,
			live:    []model.StackService{{Name: "web", Replicas: 3, Replicated: true}},
			want:    []model.ServiceDrift{},
		},
		{
			name:    "match not drifted",
			desired: map[string]uint64{"web": 3},
			live:    []model.StackService{{Name: "web", Replicas: 3, Replicated: true}},
			want:    []model.ServiceDrift{{Service: "web", Desired: 3, Live: 3, Drifted: false}},
		},
		{
			name:    "replica mismatch drifted",
			desired: map[string]uint64{"web": 3},
			live:    []model.StackService{{Name: "web", Replicas: 5, Replicated: true}},
			want:    []model.ServiceDrift{{Service: "web", Desired: 3, Live: 5, Drifted: true}},
		},
		{
			name:    "missing live drifted (not deployed yet)",
			desired: map[string]uint64{"web": 3},
			live:    nil,
			want:    []model.ServiceDrift{{Service: "web", Desired: 3, Live: 0, Drifted: true}},
		},
		{
			name:    "global live for replicated desired is mode drift",
			desired: map[string]uint64{"web": 3},
			live:    []model.StackService{{Name: "web", Replicas: 0, Replicated: false}},
			want:    []model.ServiceDrift{{Service: "web", Desired: 3, Live: 0, Drifted: true}},
		},
		{
			name:    "autoscaled and global live services ignored (not in desired)",
			desired: map[string]uint64{"web": 3, "worker": 2},
			live: []model.StackService{
				{Name: "worker", Replicas: 2, Replicated: true},
				{Name: "web", Replicas: 4, Replicated: true},        // drifted
				{Name: "autoscaled", Replicas: 9, Replicated: true}, // not in desired → ignored
				{Name: "agent", Replicas: 0, Replicated: false},     // global, not in desired → ignored
			},
			want: []model.ServiceDrift{
				{Service: "web", Desired: 3, Live: 4, Drifted: true},
				{Service: "worker", Desired: 2, Live: 2, Drifted: false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Drift(tc.desired, tc.live)
			if !driftsEqual(got, tc.want) {
				t.Errorf("Drift =\n  got:  %+v\n  want: %+v", got, tc.want)
			}
		})
	}
}

func driftsEqual(a, b []model.ServiceDrift) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDrift_DeterministicOrder(t *testing.T) {
	// Map iteration order is random in Go; the output must still be sorted so the
	// JSON API is stable. Run several times to shake out any order dependence.
	desired := map[string]uint64{"z": 1, "a": 1, "m": 1}
	for i := 0; i < 20; i++ {
		got := Drift(desired, nil)
		if len(got) != 3 || got[0].Service != "a" || got[1].Service != "m" || got[2].Service != "z" {
			t.Fatalf("iteration %d: Drift not sorted by service: %+v", i, got)
		}
	}
}
