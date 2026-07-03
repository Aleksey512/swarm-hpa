package healer

import (
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestLongestPending(t *testing.T) {
	running := model.TaskView{ID: "r1", ServiceID: "s1", State: model.TaskStateRunning, DesiredState: model.TaskStateRunning, Since: t0}
	// pending in actual state but Swarm wants it stopped too -> not IsPending.
	pendingShutdown := model.TaskView{ID: "d1", ServiceID: "s1", State: model.TaskStatePending, DesiredState: model.TaskStatePending, Since: t0}

	cases := []struct {
		name   string
		tasks  []model.TaskView
		now    time.Time
		wantID string
		wantOK bool
	}{
		{"empty", nil, nowBeyond, "", false},
		{"all running", []model.TaskView{running}, nowBeyond, "", false},
		{"pending desired-stopped skipped", []model.TaskView{pendingShutdown}, nowBeyond, "", false},
		{"pending below threshold", []model.TaskView{pendingSince("p1", t0)}, nowBelow, "", false},
		{"pending exactly at threshold (boundary)", []model.TaskView{pendingSince("p1", t0)}, t0.Add(threshold), "p1", true},
		{"pending beyond threshold", []model.TaskView{pendingSince("p1", t0)}, nowBeyond, "p1", true},
		{"first qualifying task wins", []model.TaskView{pendingSince("p1", t0), pendingSince("p2", t0)}, nowBeyond, "p1", true},
		{"skips running then finds pending", []model.TaskView{running, pendingSince("p2", t0)}, nowBeyond, "p2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := longestPending(tc.tasks, threshold, tc.now)
			if ok != tc.wantOK {
				t.Fatalf("longestPending ok = %v, want %v (verdict %+v)", ok, tc.wantOK, got)
			}
			if ok && got.ID != tc.wantID {
				t.Errorf("longestPending id = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}
