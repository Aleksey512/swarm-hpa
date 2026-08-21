package stackapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// fakeReader is a minimal port.SwarmRead for orphan-scan tests.
type fakeReader struct {
	services []model.LiveService
	err      error
}

func (f *fakeReader) AllTasks(context.Context) ([]model.TaskView, error) { return nil, nil }

func (f *fakeReader) AllServices(context.Context) ([]model.LiveService, error) {
	return f.services, f.err
}

func TestHandler_OrphansListed(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "admin", OK: true}}}
	reader := &fakeReader{services: []model.LiveService{
		{ID: "s1", Name: "admin_api", StackNamespace: "admin"},
		{ID: "s2", Name: "whoami", StackNamespace: ""},
		{ID: "s3", Name: "legacy_web", StackNamespace: "legacy"},
		{ID: "s4", Name: "pinned", StackNamespace: "",
			Labels: map[string]string{"swarm.autoscaler.enabled": "true"}},
	}}
	h := New(store, &fakeLive{}, testLogger(), 0).WithOrphanScan(reader, []string{"admin"}, nil)

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got struct {
		Orphans []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"orphans"`
		Summary struct {
			Orphans     int  `json:"orphans"`
			OrphansFail bool `json:"orphans_fail"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Sorted by name: legacy_web (unknown stack) before whoami (no stack).
	want := []struct {
		Name      string
		Namespace string
	}{
		{Name: "legacy_web", Namespace: "legacy"},
		{Name: "whoami"},
	}
	if len(got.Orphans) != len(want) || !reflect.DeepEqual(
		[]struct{ Name, Namespace string }{
			{got.Orphans[0].Name, got.Orphans[0].Namespace},
			{got.Orphans[1].Name, got.Orphans[1].Namespace},
		}, []struct{ Name, Namespace string }{
			{want[0].Name, want[0].Namespace},
			{want[1].Name, want[1].Namespace},
		}) {
		t.Errorf("orphans = %+v, want %+v", got.Orphans, want)
	}
	if got.Summary.Orphans != 2 || got.Summary.OrphansFail {
		t.Errorf("summary = %+v, want orphans=2 fail=false", got.Summary)
	}
}

func TestHandler_OrphansZeroIsEmpty(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "admin", OK: true}}}
	reader := &fakeReader{services: []model.LiveService{
		{ID: "s1", Name: "admin_api", StackNamespace: "admin"},
	}}
	h := New(store, &fakeLive{}, testLogger(), 0).WithOrphanScan(reader, []string{"admin"}, nil)

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got struct {
		Orphans []json.RawMessage `json:"orphans"`
		Summary struct {
			Orphans int `json:"orphans"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Orphans) != 0 || got.Summary.Orphans != 0 {
		t.Errorf("want no orphans, got %+v / %d", got.Orphans, got.Summary.Orphans)
	}
}

func TestHandler_OrphanScanErrorDegrades(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "admin", OK: true}}}
	reader := &fakeReader{err: errors.New("swarm unreachable")}
	h := New(store, &fakeLive{}, testLogger(), 0).WithOrphanScan(reader, []string{"admin"}, nil)

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (orphan scan error must not 5xx)", rr.Code)
	}
	var got struct {
		Orphans      []json.RawMessage `json:"orphans"`
		OrphansError string            `json:"orphans_error"`
		Summary      struct {
			OrphansFail bool `json:"orphans_fail"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OrphansError == "" || len(got.Orphans) != 0 || !got.Summary.OrphansFail {
		t.Errorf("want orphans_error set, no orphans, fail flag; got %+v", got)
	}
}

func TestHandler_OrphanScanDisabledWithoutReader(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "admin", OK: true,
		DesiredReplicas: map[string]uint64{"api": 1}}}}
	h := New(store, &fakeLive{}, testLogger(), 0) // no WithOrphanScan

	rr := do(h, http.MethodGet, "/stacks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got struct {
		Orphans json.RawMessage `json:"orphans"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got.Orphans) != "" && string(got.Orphans) != "null" {
		t.Errorf("orphans = %s, want absent (scan not wired)", got.Orphans)
	}
}

func TestHandler_UIOrphanSection(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "admin", OK: true}}}
	reader := &fakeReader{services: []model.LiveService{
		{ID: "s1", Name: "admin_api", StackNamespace: "admin"},
		{ID: "s2", Name: "whoami", StackNamespace: ""},
		{ID: "s3", Name: "legacy_web", StackNamespace: "legacy"},
	}}
	h := New(store, &fakeLive{}, testLogger(), 0).WithOrphanScan(reader, []string{"admin"}, nil)

	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Orphan services", "whoami", "legacy_web", "not in stacks.yaml", "created outside any stack"} {
		if !strings.Contains(body, want) {
			t.Errorf("UI body missing %q", want)
		}
	}
}

func TestHandler_UIOrphanZeroEmptyState(t *testing.T) {
	store := &fakeStore{items: []model.StackStatus{{Name: "admin", OK: true}}}
	reader := &fakeReader{services: []model.LiveService{
		{ID: "s1", Name: "admin_api", StackNamespace: "admin"},
	}}
	h := New(store, &fakeLive{}, testLogger(), 0).WithOrphanScan(reader, []string{"admin"}, nil)

	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "No orphan services") {
		t.Error("UI body missing the zero-orphan empty state")
	}
}
