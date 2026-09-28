package bond

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"sort"
	"sync"
	"time"
)

type State uint8

const (
	StateReserve State = iota
	StateActive
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateFailed:
		return "failed"
	default:
		return "reserve"
	}
}

type Channel struct {
	Name       string
	Kind       string
	Connected  bool
	State      State
	RTT        time.Duration
	RTTSamples int
	Throughput float64
	Loss       float64
	Reconnects uint64
	Score      float64

	overLimitSince  time.Time
	underLimitSince time.Time
	pingSent        time.Time
	rateWindowStart time.Time
	rateWindowBytes uint64
}

type Snapshot struct {
	Channels []Channel
	Active   []Channel
	Reserve  []Channel
	Failed   []Channel
}

type Scheduler struct {
	mu       sync.RWMutex
	cfg      Config
	channels map[string]*Channel
	now      func() time.Time
}

func New(cfg Config) *Scheduler {
	return &Scheduler{
		cfg:      cfg.normalized(),
		channels: make(map[string]*Channel),
		now:      time.Now,
	}
}

func (s *Scheduler) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Scheduler) Register(name, kind string) error {
	if name == "" {
		return errors.New("bond: empty channel name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[name]; ok {
		return nil
	}
	if len(s.channels) >= s.cfg.MaxChannels {
		return errors.New("bond: maximum channel count reached")
	}
	s.channels[name] = &Channel{Name: name, Kind: kind, State: StateReserve}
	return nil
}

func (s *Scheduler) Remove(name string) {
	s.mu.Lock()
	delete(s.channels, name)
	s.mu.Unlock()
}

func (s *Scheduler) SetConnected(name string, connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ensureLocked(name)
	c.Connected = connected
	if !connected {
		c.State = StateFailed
		c.overLimitSince = time.Time{}
		c.underLimitSince = time.Time{}
	} else if c.State == StateFailed {
		c.State = StateReserve
	}
}

func (s *Scheduler) NotePingSent(name string) {
	s.mu.Lock()
	c := s.ensureLocked(name)
	c.pingSent = s.now()
	s.mu.Unlock()
}

func (s *Scheduler) NotePong(name string) {
	s.mu.Lock()
	c := s.ensureLocked(name)
	if c.pingSent.IsZero() {
		s.mu.Unlock()
		return
	}
	sample := s.now().Sub(c.pingSent)
	c.pingSent = time.Time{}
	if sample <= 0 {
		s.mu.Unlock()
		return
	}
	if c.RTTSamples == 0 {
		c.RTT = sample
	} else {
		c.RTT = ewmaDuration(c.RTT, sample, s.cfg.RTTAlpha)
	}
	c.RTTSamples++
	now := s.now()
	if c.RTT > s.cfg.MaxRTT {
		c.underLimitSince = time.Time{}
		if c.overLimitSince.IsZero() {
			c.overLimitSince = now
		}
	} else {
		c.overLimitSince = time.Time{}
		if c.underLimitSince.IsZero() {
			c.underLimitSince = now
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) ObserveBytes(name string, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ensureLocked(name)
	now := s.now()
	if c.rateWindowStart.IsZero() {
		c.rateWindowStart = now
	}
	c.rateWindowBytes += uint64(n)
	elapsed := now.Sub(c.rateWindowStart)
	if elapsed < time.Second {
		return
	}
	rate := float64(c.rateWindowBytes) / elapsed.Seconds()
	if c.Throughput == 0 {
		c.Throughput = rate
	} else {
		c.Throughput = ewma(c.Throughput, rate, s.cfg.ThroughputAlpha)
	}
	c.rateWindowBytes = 0
	c.rateWindowStart = now
}

func (s *Scheduler) ObserveRTT(name string, sample time.Duration) {
	if sample <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ensureLocked(name)
	if c.RTTSamples == 0 {
		c.RTT = sample
	} else {
		c.RTT = ewmaDuration(c.RTT, sample, s.cfg.RTTAlpha)
	}
	c.RTTSamples++
	now := s.now()
	if c.RTT > s.cfg.MaxRTT {
		c.underLimitSince = time.Time{}
		if c.overLimitSince.IsZero() {
			c.overLimitSince = now
		}
	} else {
		c.overLimitSince = time.Time{}
		if c.underLimitSince.IsZero() {
			c.underLimitSince = now
		}
	}
}

func (s *Scheduler) ObserveThroughput(name string, bytesPerSecond float64) {
	if bytesPerSecond < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ensureLocked(name)
	if c.Throughput == 0 {
		c.Throughput = bytesPerSecond
	} else {
		c.Throughput = ewma(c.Throughput, bytesPerSecond, s.cfg.ThroughputAlpha)
	}
}

func (s *Scheduler) ObserveLoss(name string, loss float64) {
	loss = clamp(loss, 0, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ensureLocked(name)
	if c.Loss == 0 {
		c.Loss = loss
	} else {
		c.Loss = ewma(c.Loss, loss, s.cfg.LossAlpha)
	}
}

func (s *Scheduler) SetReconnects(name string, reconnects uint64) {
	s.mu.Lock()
	c := s.ensureLocked(name)
	c.Reconnects = reconnects
	s.mu.Unlock()
}

func (s *Scheduler) Rebalance() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()

	maxThroughput := 0.0
	for _, c := range s.channels {
		if c.Connected && c.Throughput > maxThroughput {
			maxThroughput = c.Throughput
		}
	}

	eligible := make([]*Channel, 0, len(s.channels))
	emergency := make([]*Channel, 0)
	for _, c := range s.channels {
		if !c.Connected {
			c.State = StateFailed
			c.Score = 0
			continue
		}
		c.Score = scoreChannel(c, s.cfg, maxThroughput)
		if normalEligible(c, s.cfg, now) {
			eligible = append(eligible, c)
		} else if c.RTTSamples < s.cfg.MinRTTSamples || c.RTT <= s.cfg.EmergencyMaxRTT {
			emergency = append(emergency, c)
		} else {
			c.State = StateReserve
		}
	}

	sortChannels(eligible)
	sortChannels(emergency)
	selected := make(map[string]bool, s.cfg.TargetActive)
	for _, c := range eligible {
		if len(selected) >= s.cfg.TargetActive {
			break
		}
		selected[c.Name] = true
	}
	for _, c := range emergency {
		if len(selected) >= s.cfg.MinActive || len(selected) >= s.cfg.TargetActive {
			break
		}
		selected[c.Name] = true
	}

	for _, c := range s.channels {
		if !c.Connected {
			c.State = StateFailed
		} else if selected[c.Name] {
			c.State = StateActive
		} else {
			c.State = StateReserve
		}
	}
	return snapshotLocked(s.channels)
}

func normalEligible(c *Channel, cfg Config, now time.Time) bool {
	if !c.Connected {
		return false
	}
	if c.RTTSamples < cfg.MinRTTSamples {
		return true
	}
	if c.RTT > cfg.MaxRTT {
		return c.overLimitSince.IsZero() || now.Sub(c.overLimitSince) < cfg.BadRTTHold
	}
	if !c.underLimitSince.IsZero() && cfg.RecoveryHold > 0 && c.State == StateReserve {
		return now.Sub(c.underLimitSince) >= cfg.RecoveryHold
	}
	return true
}

func scoreChannel(c *Channel, cfg Config, maxThroughput float64) float64 {
	lat := latencyFactor(c.RTT, c.RTTSamples, cfg)
	thr := 0.5
	if maxThroughput > 0 && c.Throughput > 0 {
		thr = math.Sqrt(clamp(c.Throughput/maxThroughput, 0, 1))
	}
	loss := 1 - clamp(c.Loss, 0, 1)
	stability := 1 / (1 + 0.05*float64(c.Reconnects))
	return clamp(0.50*thr+0.30*lat+0.15*loss+0.05*stability, 0.001, 1)
}

func latencyFactor(rtt time.Duration, samples int, cfg Config) float64 {
	if samples == 0 || rtt <= 0 {
		return 0.75
	}
	ms := float64(rtt) / float64(time.Millisecond)
	switch {
	case ms <= 80:
		return 1.00
	case ms <= 100:
		return 0.90
	case rtt <= cfg.PreferredRTT:
		return 0.75
	case rtt <= cfg.MaxRTT:
		span := float64(cfg.MaxRTT - cfg.PreferredRTT)
		if span <= 0 {
			return 0.25
		}
		x := float64(rtt-cfg.PreferredRTT) / span
		return 0.75 - 0.50*clamp(x, 0, 1)
	default:
		return 0.05
	}
}

func (s *Scheduler) PickFlow(flowHash uint64) (Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Channel
	bestRank := math.Inf(1)
	for _, c := range s.channels {
		if c.State != StateActive || !c.Connected {
			continue
		}
		u := hashUnit(flowHash, c.Name)
		rank := -math.Log(u) / math.Max(c.Score, 0.001)
		if rank < bestRank {
			bestRank = rank
			best = c
		}
	}
	if best == nil {
		return Channel{}, false
	}
	return cloneChannel(best), true
}

func (s *Scheduler) PickStripe(flowHash uint64, n int) []Channel {
	if n <= 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	candidates := make([]*Channel, 0)
	for _, c := range s.channels {
		if c.State == StateActive && c.Connected {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return rendezvousRank(flowHash, candidates[i]) < rendezvousRank(flowHash, candidates[j])
	})
	anchor := candidates[0]
	out := []Channel{cloneChannel(anchor)}
	for _, c := range candidates[1:] {
		if len(out) >= n {
			break
		}
		if anchor.RTTSamples > 0 && c.RTTSamples > 0 && absDuration(c.RTT-anchor.RTT) > s.cfg.MaxRTTSpread {
			continue
		}
		out = append(out, cloneChannel(c))
	}
	return out
}

func (s *Scheduler) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshotLocked(s.channels)
}

func (s *Scheduler) ensureLocked(name string) *Channel {
	if c := s.channels[name]; c != nil {
		return c
	}
	c := &Channel{Name: name, State: StateReserve}
	if len(s.channels) < s.cfg.MaxChannels {
		s.channels[name] = c
	}
	return c
}

func snapshotLocked(m map[string]*Channel) Snapshot {
	out := Snapshot{Channels: make([]Channel, 0, len(m))}
	for _, c := range m {
		cc := cloneChannel(c)
		out.Channels = append(out.Channels, cc)
		switch c.State {
		case StateActive:
			out.Active = append(out.Active, cc)
		case StateFailed:
			out.Failed = append(out.Failed, cc)
		default:
			out.Reserve = append(out.Reserve, cc)
		}
	}
	sort.Slice(out.Channels, func(i, j int) bool { return out.Channels[i].Name < out.Channels[j].Name })
	sortChannelsValue(out.Active)
	sortChannelsValue(out.Reserve)
	sortChannelsValue(out.Failed)
	return out
}

func sortChannels(ch []*Channel) {
	sort.SliceStable(ch, func(i, j int) bool {
		if ch[i].Score == ch[j].Score {
			return ch[i].Name < ch[j].Name
		}
		return ch[i].Score > ch[j].Score
	})
}

func sortChannelsValue(ch []Channel) {
	sort.SliceStable(ch, func(i, j int) bool {
		if ch[i].Score == ch[j].Score {
			return ch[i].Name < ch[j].Name
		}
		return ch[i].Score > ch[j].Score
	})
}

func cloneChannel(c *Channel) Channel {
	if c == nil {
		return Channel{}
	}
	return *c
}

func rendezvousRank(seed uint64, c *Channel) float64 {
	u := hashUnit(seed, c.Name)
	return -math.Log(u) / math.Max(c.Score, 0.001)
}

func hashUnit(seed uint64, name string) float64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], seed)
	_, _ = h.Write(b[:])
	_, _ = h.Write([]byte(name))
	v := h.Sum64()
	return (float64(v) + 1) / (float64(^uint64(0)) + 1)
}

func ewma(old, sample, alpha float64) float64 {
	return alpha*sample + (1-alpha)*old
}

func ewmaDuration(old, sample time.Duration, alpha float64) time.Duration {
	return time.Duration(ewma(float64(old), float64(sample), alpha))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absDuration(v time.Duration) time.Duration {
	if v < 0 {
		return -v
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
