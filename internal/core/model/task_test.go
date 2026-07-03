package model

import "testing"

func TestTaskViewIsPending(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		desired string
		want    bool
	}{
		{"pending + desired running", TaskStatePending, TaskStateRunning, true},
		{"running + desired running", TaskStateRunning, TaskStateRunning, false},
		{"pending + desired shutdown", TaskStatePending, "shutdown", false},
		{"pending + desired pending", TaskStatePending, TaskStatePending, false},
		{"empty state", "", TaskStateRunning, false},
		{"pending + empty desired", TaskStatePending, "", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tv := TaskView{State: tc.state, DesiredState: tc.desired}
			if got := tv.IsPending(); got != tc.want {
				t.Errorf("IsPending(state=%q, desired=%q) = %v, want %v", tc.state, tc.desired, got, tc.want)
			}
		})
	}
}
