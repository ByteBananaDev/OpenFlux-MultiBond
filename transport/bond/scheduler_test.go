package bond

import (
	"testing"
	"time"
)

func TestRTTEWMAIgnoresSingleSpike(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BadRTTHold = 5 * time.Second
	s := New(cfg)
	if err := s.Register("a", "vyandex"); err != nil {
		t.Fatal(err)
	}
	s.SetConnected("a", true)
	for _, rtt := range []time.Duration{60, 70, 65, 300} {
		s.ObserveRTT("a", rtt*time.Millisecond)
	}
	s.ObserveThroughput("a", 100000)
	snap := s.Rebalance()
	if len(snap.Active) != 1 {
		t.Fatalf("single spike should not remove channel; active=%d avg=%v", len(snap.Active), snap.Channels[0].RTT)
	}
	if snap.Channels[0].RTT >= 150*time.Millisecond {
		t.Fatalf("EWMA unexpectedly dominated by spike: %v", snap.Channels[0].RTT)
	}
}

func TestSustainedHighRTTBecomesReserve(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BadRTTHold = time.Second
	cfg.RecoveryHold = 0
	cfg.EmergencyMaxRTT = 180 * time.Millisecond
	s := New(cfg)
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	_ = s.Register("slow", "yandex")
	s.SetConnected("slow", true)
	for i := 0; i < 12; i++ {
		s.ObserveRTT("slow", 220*time.Millisecond)
	}
	_ = s.Rebalance()
	now = now.Add(2 * time.Second)
	s.ObserveRTT("slow", 220*time.Millisecond)
	snap := s.Rebalance()
	if len(snap.Active) != 0 || len(snap.Reserve) != 1 {
		t.Fatalf("sustained high RTT should be reserve: active=%d reserve=%d", len(snap.Active), len(snap.Reserve))
	}
}

func TestThroughputCanBeatModeratelyLowerRTT(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetActive = 1
	cfg.MinActive = 1
	cfg.RecoveryHold = 0
	s := New(cfg)
	for _, n := range []string{"lowlat", "fast"} {
		_ = s.Register(n, "vyandex")
		s.SetConnected(n, true)
	}
	for i := 0; i < 5; i++ {
		s.ObserveRTT("lowlat", 45*time.Millisecond)
		s.ObserveRTT("fast", 90*time.Millisecond)
	}
	s.ObserveThroughput("lowlat", 10_000)
	s.ObserveThroughput("fast", 200_000)
	snap := s.Rebalance()
	if len(snap.Active) != 1 || snap.Active[0].Name != "fast" {
		t.Fatalf("throughput should dominate within healthy RTT range: %#v", snap.Active)
	}
}

func TestHardPoolLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxChannels = 3
	s := New(cfg)
	for _, n := range []string{"a", "b", "c"} {
		if err := s.Register(n, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Register("d", "x"); err == nil {
		t.Fatal("expected max channel error")
	}
}

func TestPickStripeHonorsRTTSpread(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetActive = 4
	cfg.MinActive = 1
	cfg.MaxRTTSpread = 30 * time.Millisecond
	cfg.RecoveryHold = 0
	s := New(cfg)
	vals := map[string]time.Duration{
		"a": 50 * time.Millisecond,
		"b": 60 * time.Millisecond,
		"c": 68 * time.Millisecond,
		"d": 130 * time.Millisecond,
	}
	for n, rtt := range vals {
		_ = s.Register(n, "x")
		s.SetConnected(n, true)
		for i := 0; i < 5; i++ {
			s.ObserveRTT(n, rtt)
		}
		s.ObserveThroughput(n, 100_000)
	}
	s.Rebalance()
	group := s.PickStripe(42, 4)
	if len(group) < 2 {
		t.Fatalf("expected stripe group, got %d", len(group))
	}
	anchor := group[0].RTT
	for _, c := range group[1:] {
		if absDuration(c.RTT-anchor) > cfg.MaxRTTSpread {
			t.Fatalf("channel %s RTT %v too far from anchor %v", c.Name, c.RTT, anchor)
		}
	}
}

func TestEmergencyOnlyFillsMinimum(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetActive = 10
	cfg.MinActive = 2
	cfg.BadRTTHold = 0
	cfg.RecoveryHold = 0
	s := New(cfg)
	for i, rtt := range []time.Duration{200, 210, 220, 230} {
		n := string(rune('a' + i))
		_ = s.Register(n, "x")
		s.SetConnected(n, true)
		for j := 0; j < 4; j++ {
			s.ObserveRTT(n, rtt*time.Millisecond)
		}
		s.ObserveThroughput(n, float64(100_000-i*10_000))
	}
	snap := s.Rebalance()
	if len(snap.Active) != 2 {
		t.Fatalf("emergency pool should only satisfy MinActive, got %d", len(snap.Active))
	}
}

func TestPingPongUpdatesRTTEWMA(t *testing.T) {
	cfg := DefaultConfig()
	s := New(cfg)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	_ = s.Register("v1", "vyandex")
	s.SetConnected("v1", true)
	s.NotePingSent("v1")
	now = now.Add(80 * time.Millisecond)
	s.NotePong("v1")
	snap := s.Snapshot()
	if len(snap.Channels) != 1 || snap.Channels[0].RTT != 80*time.Millisecond {
		t.Fatalf("unexpected RTT after pong: %#v", snap.Channels)
	}
}

func TestObserveBytesBuildsThroughput(t *testing.T) {
	cfg := DefaultConfig()
	s := New(cfg)
	now := time.Unix(2000, 0)
	s.now = func() time.Time { return now }
	_ = s.Register("m1", "mailru")
	s.SetConnected("m1", true)
	s.ObserveBytes("m1", 100_000)
	now = now.Add(time.Second)
	s.ObserveBytes("m1", 100_000)
	snap := s.Snapshot()
	if snap.Channels[0].Throughput < 190_000 || snap.Channels[0].Throughput > 210_000 {
		t.Fatalf("unexpected throughput %.2f", snap.Channels[0].Throughput)
	}
}


func TestHealthyReserveDoesNotWaitRecoveryHold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TargetActive = 3
	cfg.MinActive = 1
	cfg.RecoveryHold = 15 * time.Second
	s := New(cfg)
	for _, n := range []string{"a", "b", "c"} {
		_ = s.Register(n, "x")
		s.SetConnected(n, true)
		for i := 0; i < 3; i++ {
			s.ObserveRTT(n, 60*time.Millisecond)
		}
	}
	snap := s.Rebalance()
	if len(snap.Active) != 3 {
		t.Fatalf("healthy reserve channels should activate immediately, got %d", len(snap.Active))
	}
}
