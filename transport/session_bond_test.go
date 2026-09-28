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
