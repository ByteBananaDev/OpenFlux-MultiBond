package transport

import (
	"testing"
	"time"

	"openflux/transport/bond"
	"openflux/transport/control"
)

func TestSessionBondImportsExistingTransportsAndPicksBest(t *testing.T) {
	params := PeerParameters{
		Capabilities:  control.CapabilityIPv4 | control.CapabilityTCP,
		MaxPacketSize: 1500,
	}
	s, err := NewSession(params, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := &negotiationWire{}
	b := &negotiationWire{}
	if err := s.AddTransport("fast", a, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransport("slow", b, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}

	cfg := bond.DefaultConfig()
	cfg.TargetActive = 1
	cfg.MinActive = 1
	cfg.RecoveryHold = 0
	if err := s.EnableBond(cfg); err != nil {
		t.Fatal(err)
	}

	st := s.bondState()
	if st == nil {
		t.Fatal("bond state was not created")
	}
	st.scheduler.SetConnected("fast", true)
	st.scheduler.SetConnected("slow", true)
	for i := 0; i < 4; i++ {
		st.scheduler.ObserveRTT("fast", 70*time.Millisecond)
		st.scheduler.ObserveRTT("slow", 110*time.Millisecond)
	}
	st.scheduler.ObserveThroughput("fast", 200_000)
	st.scheduler.ObserveThroughput("slow", 20_000)

	snap := st.scheduler.Rebalance()
	if len(snap.Channels) != 2 {
		t.Fatalf("registered channels=%d, want 2", len(snap.Channels))
	}
	if len(snap.Active) != 1 || snap.Active[0].Name != "fast" {
		t.Fatalf("active=%v, want fast", snap.Active)
	}

	chosen := s.bondPickFlow(0x1234, []*transportLink{s.links["fast"], s.links["slow"]})
	if chosen == nil || chosen.name != "fast" {
		if chosen == nil {
			t.Fatal("bond selector returned nil")
		}
		t.Fatalf("chosen=%s, want fast", chosen.name)
	}
}

func TestSessionBondPinsExistingFlow(t *testing.T) {
	params := PeerParameters{
		Capabilities:  control.CapabilityIPv4 | control.CapabilityTCP,
		MaxPacketSize: 1500,
	}
	s, err := NewSession(params, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := &negotiationWire{}
	b := &negotiationWire{}
	if err := s.AddTransport("a", a, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransport("b", b, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}
	cfg := bond.DefaultConfig()
	cfg.TargetActive = 2
	cfg.MinActive = 1
	cfg.RecoveryHold = 0
	if err := s.EnableBond(cfg); err != nil {
		t.Fatal(err)
	}

	st := s.bondState()
	for _, name := range []string{"a", "b"} {
		st.scheduler.SetConnected(name, true)
		for i := 0; i < 4; i++ {
			st.scheduler.ObserveRTT(name, 60*time.Millisecond)
		}
		st.scheduler.ObserveThroughput(name, 100_000)
	}
	st.scheduler.Rebalance()

	live := []*transportLink{s.links["a"], s.links["b"]}
	first := s.bondPickFlow(0xdeadbeef, live)
	if first == nil {
		t.Fatal("first selection returned nil")
	}

	// Heavily improve the other channel. An existing flow must stay pinned.
	other := "a"
	if first.name == "a" {
		other = "b"
	}
	for i := 0; i < 5; i++ {
		st.scheduler.ObserveThroughput(other, 10_000_000)
	}
	st.scheduler.Rebalance()

	second := s.bondPickFlow(0xdeadbeef, live)
	if second == nil || second.name != first.name {
		if second == nil {
			t.Fatal("pinned selection returned nil")
		}
		t.Fatalf("flow moved from %s to %s after score change", first.name, second.name)
	}
}

func TestSessionBondRepinsWhenCarrierDemoted(t *testing.T) {
	params := PeerParameters{
		Capabilities:  control.CapabilityIPv4 | control.CapabilityTCP,
		MaxPacketSize: 1500,
	}
	s, err := NewSession(params, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := &negotiationWire{}
	b := &negotiationWire{}
	if err := s.AddTransport("a", a, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransport("b", b, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}
	cfg := bond.DefaultConfig()
	cfg.TargetActive = 2
	cfg.MinActive = 1
	cfg.BadRTTHold = 0
	cfg.RecoveryHold = 0
	cfg.EmergencyMaxRTT = 180 * time.Millisecond
	if err := s.EnableBond(cfg); err != nil {
		t.Fatal(err)
	}

	st := s.bondState()
	for _, name := range []string{"a", "b"} {
		st.scheduler.SetConnected(name, true)
		for i := 0; i < 4; i++ {
			st.scheduler.ObserveRTT(name, 60*time.Millisecond)
		}
		st.scheduler.ObserveThroughput(name, 100_000)
	}
	st.scheduler.Rebalance()

	live := []*transportLink{s.links["a"], s.links["b"]}
	first := s.bondPickFlow(0xabcddcba, live)
	if first == nil {
		t.Fatal("first selection returned nil")
	}

	// Force the pinned carrier beyond the emergency ceiling so it leaves ACTIVE.
	for i := 0; i < 8; i++ {
		st.scheduler.ObserveRTT(first.name, 250*time.Millisecond)
	}
	st.scheduler.Rebalance()
	if st.scheduler.IsActive(first.name) {
		t.Fatalf("bad carrier %s remained active", first.name)
	}

	second := s.bondPickFlow(0xabcddcba, live)
	if second == nil {
		t.Fatal("repin selection returned nil")
	}
	if second.name == first.name {
		t.Fatalf("flow remained pinned to demoted carrier %s", first.name)
	}
}

func TestSessionBondRepinsWhenCarrierDisconnectsAndStaysPinnedAfterRecovery(t *testing.T) {
	params := PeerParameters{
		Capabilities:  control.CapabilityIPv4 | control.CapabilityTCP,
		MaxPacketSize: 1500,
	}
	s, err := NewSession(params, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := &negotiationWire{}
	b := &negotiationWire{}
	if err := s.AddTransport("a", a, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransport("b", b, testSessionSecret, testSessionCtx, 50); err != nil {
		t.Fatal(err)
	}

	cfg := bond.DefaultConfig()
	cfg.TargetActive = 2
	cfg.MinActive = 1
	cfg.RecoveryHold = 0
	if err := s.EnableBond(cfg); err != nil {
		t.Fatal(err)
	}

	st := s.bondState()
	for _, name := range []string{"a", "b"} {
		st.scheduler.SetConnected(name, true)
		for i := 0; i < 4; i++ {
			st.scheduler.ObserveRTT(name, 60*time.Millisecond)
		}
		st.scheduler.ObserveThroughput(name, 100_000)
	}
	snap := st.scheduler.Rebalance()
	if len(snap.Active) != 2 {
		t.Fatalf("active=%d, want 2 before disconnect", len(snap.Active))
	}

	live := []*transportLink{s.links["a"], s.links["b"]}
	const flow = uint64(0x1122334455667788)
	first := s.bondPickFlow(flow, live)
	if first == nil {
		t.Fatal("initial selection returned nil")
	}

	failed := first.name
	other := "a"
	if failed == "a" {
		other = "b"
	}

	st.scheduler.SetConnected(failed, false)
	snap = st.scheduler.Rebalance()
	if len(snap.Active) != 1 || snap.Active[0].Name != other {
		t.Fatalf("active after disconnect=%v, want only %s", snap.Active, other)
	}
	if len(snap.Failed) != 1 || snap.Failed[0].Name != failed {
		t.Fatalf("failed after disconnect=%v, want only %s", snap.Failed, failed)
	}

	second := s.bondPickFlow(flow, live)
	if second == nil || second.name != other {
		if second == nil {
			t.Fatal("selection after disconnect returned nil")
		}
		t.Fatalf("flow did not repin from %s to %s; got %s", failed, other, second.name)
	}

	st.scheduler.SetConnected(failed, true)
	snap = st.scheduler.Rebalance()
	if len(snap.Active) != 2 {
		t.Fatalf("active=%d after recovery, want 2", len(snap.Active))
	}

	third := s.bondPickFlow(flow, live)
	if third == nil || third.name != other {
		if third == nil {
			t.Fatal("selection after recovery returned nil")
		}
		t.Fatalf("recovered carrier caused flow flap from %s back to %s", other, third.name)
	}
}
