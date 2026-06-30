package reconciler

import (
	"testing"
	"time"
)

func TestCooldownWindow(t *testing.T) {
	clk := newFakeClock()
	cd := NewCooldown(clk)
	const window = time.Minute

	if !cd.Allowed("s", window) {
		t.Fatal("a never-acted service should be allowed")
	}
	cd.Record("s")
	if cd.Allowed("s", window) {
		t.Error("should be suppressed immediately after Record")
	}
	clk.advance(30 * time.Second)
	if cd.Allowed("s", window) {
		t.Error("still within the window at 30s")
	}
	clk.advance(31 * time.Second) // total 61s >= 60s
	if !cd.Allowed("s", window) {
		t.Error("should be allowed once the window elapses")
	}
}

func TestCooldownZeroWindowAlwaysAllows(t *testing.T) {
	cd := NewCooldown(newFakeClock())
	cd.Record("s")
	if !cd.Allowed("s", 0) {
		t.Error("a zero window must always allow")
	}
}

func TestCooldownPerService(t *testing.T) {
	cd := NewCooldown(newFakeClock())
	cd.Record("a")
	if !cd.Allowed("b", time.Minute) {
		t.Error("cooldown on service a must not affect service b")
	}
}
