package autoscaler

import (
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func policy(min, max uint64, target float64) model.ServicePolicy {
	return model.ServicePolicy{Enabled: true, Min: min, Max: max, Metric: "cpu", Target: target}
}

func TestDesired(t *testing.T) {
	cases := []struct {
		name    string
		current uint64
		value   float64
		p       model.ServicePolicy
		want    uint64
	}{
		{"scale up 2x", 2, 160, policy(1, 10, 80), 4},
		{"scale down half", 4, 40, policy(1, 10, 80), 2},
		{"scale up rounds up", 3, 100, policy(1, 10, 80), 4}, // 1.25x -> ceil(3.75)=4
		{"within tolerance high", 3, 85, policy(1, 10, 80), 3},
		{"within tolerance low", 3, 73, policy(1, 10, 80), 3},
		{"clamp to max", 5, 200, policy(1, 6, 80), 6},
		{"clamp to min", 4, 10, policy(2, 10, 80), 2},
		{"current zero -> min", 0, 999, policy(3, 10, 80), 3},
		{"target zero -> unchanged", 4, 50, policy(1, 10, 0), 4},
		{"idle (value 0) -> min", 5, 0, policy(2, 10, 80), 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Desired(c.current, c.value, c.p)
			if got != c.want {
				t.Errorf("Desired(current=%d, value=%g, target=%g) = %d, want %d",
					c.current, c.value, c.p.Target, got, c.want)
			}
		})
	}
}
