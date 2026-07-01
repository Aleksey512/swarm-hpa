package healer

import (
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestParseConstraint(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantKey string
		wantOp  string
		wantVal string
		wantOK  bool
	}{
		{"eq no spaces", "node.labels.nodeNum==1", "node.labels.nodeNum", opEq, "1", true},
		{"eq spaced", "node.labels.nodeNum == 1", "node.labels.nodeNum", opEq, "1", true},
		{"neq no spaces", "node.labels.nodeNum!=2", "node.labels.nodeNum", opNeq, "2", true},
		{"neq spaced", "node.hostname != host-2", "node.hostname", opNeq, "host-2", true},
		{"trims leading/trailing and inner runs", "   node.id   ==   n1   ", "node.id", opEq, "n1", true},
		{"hostname eq", "node.hostname==host-1", "node.hostname", opEq, "host-1", true},
		{"eq wins when both operators present", "node.labels.a==b!=c", "node.labels.a", opEq, "b!=c", true},

		{"no operator", "node.labels.nodeNum", "", "", "", false},
		{"empty string", "", "", "", "", false},
		{"empty key", "==1", "", "", "", false},
		{"empty value", "node.labels.x==", "", "", "", false},
		{"operator only", "==", "", "", "", false},
		{"neq empty value", "node.labels.x!=", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, op, val, ok := parseConstraint(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("parseConstraint(%q) ok = %v, want %v", tc.raw, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return // parts are unspecified when ok is false
			}
			if key != tc.wantKey || op != tc.wantOp || val != tc.wantVal {
				t.Errorf("parseConstraint(%q) = (%q, %q, %q), want (%q, %q, %q)",
					tc.raw, key, op, val, tc.wantKey, tc.wantOp, tc.wantVal)
			}
		})
	}
}

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

func TestNodeValue(t *testing.T) {
	n := model.NodeView{
		ID:           "n1",
		Name:         "host-1",
		Availability: model.NodeAvailabilityActive,
		State:        model.NodeStateReady,
		Labels:       map[string]string{"nodeNum": "1"},
	}
	cases := []struct {
		name      string
		key       string
		wantVal   string
		wantKnown bool
	}{
		{"hostname", keyHostname, "host-1", true},
		{"id", keyID, "n1", true},
		{"present label", labelKeyPrefix + "nodeNum", "1", true},
		{"missing label is known-empty", labelKeyPrefix + "zone", "", true},
		{"node.role unknown", "node.role", "", false},
		{"engine label unknown", "engine.labels.foo", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, known := nodeValue(n, tc.key)
			if val != tc.wantVal || known != tc.wantKnown {
				t.Errorf("nodeValue(%q) = (%q, %v), want (%q, %v)", tc.key, val, known, tc.wantVal, tc.wantKnown)
			}
		})
	}
}
