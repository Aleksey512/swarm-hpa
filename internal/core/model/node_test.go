package model

import "testing"

func TestNodeViewIsActive(t *testing.T) {
	cases := []struct {
		name         string
		availability string
		state        string
		want         bool
	}{
		{"active + ready", NodeAvailabilityActive, NodeStateReady, true},
		{"active + down", NodeAvailabilityActive, "down", false},
		{"pause + ready", "pause", NodeStateReady, false},
		{"drain + ready", "drain", NodeStateReady, false},
		{"active + unknown", NodeAvailabilityActive, "unknown", false},
		{"empty availability", "", NodeStateReady, false},
		{"active + empty state", NodeAvailabilityActive, "", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := NodeView{Availability: tc.availability, State: tc.state}
			if got := n.IsActive(); got != tc.want {
				t.Errorf("IsActive(avail=%q, state=%q) = %v, want %v", tc.availability, tc.state, got, tc.want)
			}
		})
	}
}
