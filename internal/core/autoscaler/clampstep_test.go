package autoscaler

import "testing"

func TestClampStep(t *testing.T) {
	cases := []struct {
		name                     string
		current, desired, maxStep uint64
		want                     uint64
	}{
		{"unlimited (maxStep 0)", 2, 10, 0, 10},
		{"no change", 5, 5, 2, 5},
		{"scale up clamped", 2, 10, 3, 5},
		{"scale up within limit", 2, 4, 3, 4},
		{"scale down clamped", 10, 2, 3, 7},
		{"scale down within limit", 5, 4, 3, 4},
		{"scale down to zero within limit", 2, 0, 5, 0},
		{"scale down clamped above zero", 10, 0, 4, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampStep(tc.current, tc.desired, tc.maxStep); got != tc.want {
				t.Errorf("ClampStep(%d, %d, %d) = %d, want %d",
					tc.current, tc.desired, tc.maxStep, got, tc.want)
			}
		})
	}
}
