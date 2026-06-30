package reconciler

import (
	"testing"
	"time"
)

func TestCooldownWindow(t *testing.T) {
	clk := newFakeClock()
	cd := NewCooldown(time.Minute, clk)

	if !cd.Allowed("s") {
		t.Fatal("a never-acted service should be allowed")
	}
	cd.Record("s")
	if cd.Allowed("s") {
		t.Error("should be suppressed immediately after Record")
	}
	clk.advance(30 * time.Second)
	if cd.Allowed("s") {
		t.Error("still within the window at 30s")
	}
	clk.advance(31 * time.Second) // total 61s >= 60s
	if !cd.Allowed("s") {
		t.Error("should be allowed once the window elapses")
	}
}

func TestCooldownZeroWindowAlwaysAllows(t *testing.T) {
	cd := NewCooldown(0, newFakeClock())
	cd.Record("s")
	if !cd.Allowed("s") {
		t.Error("a zero window must always allow")
	}
}

func TestCooldownPerService(t *testing.T) {
	cd := NewCooldown(time.Minute, newFakeClock())
	cd.Record("a")
	if !cd.Allowed("b") {
		t.Error("cooldown on service a must not affect service b")
	}
}
