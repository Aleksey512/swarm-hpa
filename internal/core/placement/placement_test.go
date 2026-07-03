package placement

import (
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func TestNodeSatisfies(t *testing.T) {
	node := model.NodeView{
		ID:           "n1",
		Name:         "host-1",
		Availability: model.NodeAvailabilityActive,
		State:        model.NodeStateReady,
		Labels:       map[string]string{"nodeNum": "1", "tier": "gpu"},
	}

	cases := []struct {
		name        string
		constraints []string
		want        bool
	}{
		{"label eq match", []string{"node.labels.nodeNum==1"}, true},
		{"label eq match spaced", []string{"node.labels.nodeNum == 1"}, true},
		{"label eq mismatch", []string{"node.labels.nodeNum==2"}, false},
		{"label eq missing label excludes", []string{"node.labels.zone==eu"}, false},
		{"label neq holds", []string{"node.labels.nodeNum!=2"}, true},
		{"label neq violated", []string{"node.labels.nodeNum!=1"}, false},
		{"label neq missing label holds", []string{"node.labels.zone!=eu"}, true},
		{"hostname eq", []string{"node.hostname==host-1"}, true},
		{"hostname mismatch", []string{"node.hostname==host-2"}, false},
		{"id eq", []string{"node.id==n1"}, true},
		{"unparseable skipped", []string{"node.labels.nodeNum"}, true},
		{"unknown key skipped", []string{"node.role==manager"}, true},
		{"engine labels skipped", []string{"engine.labels.foo==bar"}, true},
		{"multiple all satisfied", []string{"node.labels.nodeNum==1", "node.hostname==host-1"}, true},
		{"multiple one fails", []string{"node.labels.nodeNum==1", "node.hostname==host-2"}, false},
		{"unknown key never widens exclusion", []string{"node.role==worker", "node.labels.nodeNum==1"}, true},
		{"unknown key plus failing parseable still fails", []string{"node.role==worker", "node.labels.nodeNum==2"}, false},
		{"no constraints vacuously true", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NodeSatisfies(node, tc.constraints); got != tc.want {
				t.Errorf("NodeSatisfies(%v) = %v, want %v", tc.constraints, got, tc.want)
			}
		})
	}
}

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
