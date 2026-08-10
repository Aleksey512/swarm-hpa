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

func TestStore_FilesDeepCopy(t *testing.T) {
	s := New(testLogger())
	files := []model.StackFileStatus{
		{File: "a.yaml", Status: "ok"},
		{File: "b.yaml", Status: "failed", Error: "boom"},
	}
	s.SetStatus("a", model.StackStatus{Files: files})
	// Mutate the caller's slice after SetStatus — the store must be unaffected.
	files[0].Status = "skipped"
	files[1].Error = "mutated"

	got := s.Snapshot()
	if len(got[0].Files) != 2 {
		t.Fatalf("Files len = %d, want 2", len(got[0].Files))
	}
	if got[0].Files[0].Status != "ok" || got[0].Files[1].Error != "boom" {
		t.Errorf("SetStatus did not copy input Files: %+v", got[0].Files)
	}
	// Mutate the snapshot's slice — a fresh snapshot must be unaffected.
	got[0].Files[0].Status = "mutated"
	again := s.Snapshot()
	if again[0].Files[0].Status != "ok" {
		t.Errorf("Snapshot did not deep-copy Files: got %q, want ok", again[0].Files[0].Status)
	}
}

func TestStore_NilFilesStaysNil(t *testing.T) {
	s := New(testLogger())
	s.SetStatus("a", model.StackStatus{OK: true})
	got := s.Snapshot()
	if got[0].Files != nil {
		t.Errorf("nil Files should stay nil, got %v", got[0].Files)
	}
}

func TestStore_SetStatePreservesLastResult(t *testing.T) {
	s := New(testLogger())
	s.SetStatus("web", model.StackStatus{
		Repo: "myapp", Revision: "rev1", OK: true,
		DeployCount: 2, DesiredReplicas: map[string]uint64{"web": 3},
		Files: []model.StackFileStatus{{File: "compose.yaml", Status: "ok"}},
	})
	// Mid-pass: flip to syncing. Only Repo+State must change.
	s.SetState("web", "myapp", "syncing")

	got := s.Snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d statuses, want 1", len(got))
	}
	st := got[0]
	if st.State != "syncing" || st.Repo != "myapp" {
		t.Errorf("SetState did not set Repo/State: repo=%q state=%q", st.Repo, st.State)
	}
	// Last result must be untouched.
	if st.Revision != "rev1" || !st.OK || st.DeployCount != 2 ||
		st.DesiredReplicas["web"] != 3 || len(st.Files) != 1 || st.Files[0].Status != "ok" {
		t.Errorf("SetState clobbered last result: %+v", st)
	}
}

func TestStore_SetStateSeedsEntryBeforeFirstSync(t *testing.T) {
	// API races the first tick: SetState lands before any SetStatus.
	s := New(testLogger())
	s.SetState("web", "myapp", "waiting")

	got := s.Snapshot()
	if len(got) != 1 {
		t.Fatalf("got %d statuses, want 1", len(got))
	}
	st := got[0]
	if st.Name != "web" || st.Repo != "myapp" || st.State != "waiting" {
		t.Errorf("seeded entry mismatch: %+v", st)
	}
}

func TestStore_SetStatusClearsStateToIdle(t *testing.T) {
	s := New(testLogger())
	s.SetState("web", "myapp", "syncing")
	if got := s.Snapshot()[0]; got.State != "syncing" {
		t.Fatalf("precondition: state=%q want syncing", got.State)
	}
	// End of pass: a full SetStatus must reset State to "".
	s.SetStatus("web", model.StackStatus{Repo: "myapp", Revision: "rev1", OK: true})

	st := s.Snapshot()[0]
	if st.State != "" {
		t.Errorf("SetStatus did not reset State: got %q, want empty (idle)", st.State)
	}
	if st.Revision != "rev1" || !st.OK {
		t.Errorf("SetStatus result mismatch: %+v", st)
	}
}

func TestStore_SetStateConcurrent(t *testing.T) {
	s := New(testLogger())
	// Seed one full status so SetState races over an existing entry.
	s.SetStatus("web", model.StackStatus{Repo: "myapp", Revision: "rev1", OK: true})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			state := "syncing"
			if i%2 == 0 {
				state = "waiting"
			}
			s.SetState("web", "myapp", state)
		}(i)
		go func() {
			defer wg.Done()
			_ = s.Snapshot()
		}()
	}
	wg.Wait()
}
