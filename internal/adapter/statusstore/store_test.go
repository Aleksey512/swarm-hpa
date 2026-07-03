package statusstore

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestStore_SetSnapshot(t *testing.T) {
	s := New(testLogger())
	s.SetStatus("b", model.StackStatus{Revision: "rev2", OK: true, DeployCount: 3, DesiredReplicas: map[string]uint64{"web": 3}, LastSync: time.Unix(100, 0)})
	s.SetStatus("a", model.StackStatus{Revision: "rev1", OK: false, ErrorStage: "deploy", ErrorMessage: "boom"})

	got := s.Snapshot()
	if len(got) != 2 {
		t.Fatalf("got %d statuses, want 2", len(got))
	}
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("snapshot not sorted by name: %+v", got)
	}
	if got[0].OK || got[0].ErrorStage != "deploy" || got[0].ErrorMessage != "boom" {
		t.Errorf("status a mismatch: %+v", got[0])
	}
	if got[1].Revision != "rev2" || got[1].DeployCount != 3 || got[1].DesiredReplicas["web"] != 3 {
		t.Errorf("status b mismatch: %+v", got[1])
	}
}

func TestStore_SnapshotIsDeepCopy(t *testing.T) {
	s := New(testLogger())
	s.SetStatus("a", model.StackStatus{Revision: "rev1", DesiredReplicas: map[string]uint64{"web": 3}})

	got := s.Snapshot()
	got[0].DesiredReplicas["web"] = 999
	got[0].Revision = "mutated"

	again := s.Snapshot()
	if again[0].DesiredReplicas["web"] != 3 {
		t.Errorf("Snapshot did not deep-copy DesiredReplicas: got %d, want 3", again[0].DesiredReplicas["web"])
	}
	if again[0].Revision == "mutated" {
		t.Error("Snapshot returned a value aliased with the store; revision leaked")
	}
}

func TestStore_SetStatusCopiesInput(t *testing.T) {
	s := New(testLogger())
	desired := map[string]uint64{"web": 3}
	s.SetStatus("a", model.StackStatus{DesiredReplicas: desired})
	desired["web"] = 999 // mutate the caller's map after SetStatus

	got := s.Snapshot()
	if got[0].DesiredReplicas["web"] != 3 {
		t.Errorf("SetStatus did not copy input DesiredReplicas: got %d, want 3", got[0].DesiredReplicas["web"])
	}
}

func TestStore_NilDesiredStaysNil(t *testing.T) {
	s := New(testLogger())
	s.SetStatus("a", model.StackStatus{OK: true})
	got := s.Snapshot()
	if got[0].DesiredReplicas != nil {
		t.Errorf("nil DesiredReplicas should stay nil, got %v", got[0].DesiredReplicas)
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := New(testLogger())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			s.SetStatus(string(rune('a'+n%26)), model.StackStatus{OK: true, DesiredReplicas: map[string]uint64{"web": uint64(n)}})
		}(i)
		go func() {
			defer wg.Done()
			_ = s.Snapshot()
		}()
	}
	wg.Wait()
}
