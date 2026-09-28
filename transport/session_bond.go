package transport

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"openflux/transport/bond"
	"openflux/transport/control"
	"openflux/utils"
)

// sessionBondState is intentionally kept outside Session so the MultiBond
// integration stays small and easy to rebase on top of upstream OpenFlux.
type sessionBondState struct {
	scheduler   *bond.Scheduler
	mu          sync.Mutex
	probeCursor int
}

var sessionBondStates sync.Map // map[*Session]*sessionBondState

// EnableBond turns on adaptive multi-carrier scheduling for this Session.
// It must be called before Start. Existing bootstrap transports are imported
// automatically, and transports added later are registered by the session.
func (s *Session) EnableBond(cfg bond.Config) error {
	if s == nil {
		return errors.New("session: nil session")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("session: enable bond before Start")
	}
	links := make([]*transportLink, 0, len(s.links))
	for _, name := range s.order {
		links = append(links, s.links[name])
	}
	s.mu.Unlock()

	st := &sessionBondState{scheduler: bond.New(cfg)}
	for _, l := range links {
		if err := st.scheduler.Register(l.name, fmt.Sprintf("%T", l.raw)); err != nil {
			return err
		}
	}
	sessionBondStates.Store(s, st)
	return nil
}

func (s *Session) bondState() *sessionBondState {
	if v, ok := sessionBondStates.Load(s); ok {
		return v.(*sessionBondState)
	}
	return nil
}

func (s *Session) stopBond() {
	sessionBondStates.Delete(s)
}

func (s *Session) bondRegister(name, kind string) error {
	st := s.bondState()
	if st == nil {
		return nil
	}
	return st.scheduler.Register(name, kind)
}

func (s *Session) bondRemove(name string) {
	if st := s.bondState(); st != nil {
		st.scheduler.Remove(name)
	}
}

func (s *Session) bondSetConnected(name string, connected bool) {
	if st := s.bondState(); st != nil {
		st.scheduler.SetConnected(name, connected)
	}
}

func (s *Session) bondNotePingSent(name string) {
	if st := s.bondState(); st != nil {
		st.scheduler.NotePingSent(name)
	}
}

func (s *Session) bondNotePong(name string) {
	if st := s.bondState(); st != nil {
		st.scheduler.NotePong(name)
	}
}

func (s *Session) bondObserveBytes(name string, n int) {
	if st := s.bondState(); st != nil {
		st.scheduler.ObserveBytes(name, n)
	}
}

func (s *Session) bondObserveSendResult(name string, err error) {
	if st := s.bondState(); st != nil {
		if err != nil {
			st.scheduler.ObserveLoss(name, 1)
			return
		}
		st.scheduler.ObserveLoss(name, 0)
	}
}

// bondPickFlow returns nil when MultiBond is disabled or when its active set
// does not currently contain a carrier that is live in this Session.
func (s *Session) bondPickFlow(flowHash uint64, live []*transportLink) *transportLink {
	st := s.bondState()
	if st == nil {
		return nil
	}
	ch, ok := st.scheduler.PickFlow(flowHash)
	if !ok {
		return nil
	}
	for _, l := range live {
		if l.name == ch.Name {
			return l
		}
	}
	return nil
}

// startBondLoop periodically refreshes link state, rebalances the active pool,
// and probes a bounded number of carriers. Eight probes per second means even
// a 256-channel pool is sampled about every 32 seconds without flooding a
// document transport with keepalive traffic.
func (s *Session) startBondLoop() {
	if s.bondState() == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.done:
				sessionBondStates.Delete(s)
				return
			case <-tick.C:
				s.bondRefresh()
				s.bondProbe(8)
			}
		}
	}()
}

func (s *Session) bondRefresh() {
	st := s.bondState()
	if st == nil {
		return
	}

	type sample struct {
		name       string
		kind       string
		connected  bool
		reconnects uint64
	}

	s.mu.Lock()
	live := s.liveLinksLocked()
	liveSet := make(map[string]bool, len(live))
	for _, l := range live {
		liveSet[l.name] = true
	}
	samples := make([]sample, 0, len(s.links))
	for _, name := range s.order {
		l := s.links[name]
		stats := l.raw.Stats()
		samples = append(samples, sample{
			name:       name,
			kind:       fmt.Sprintf("%T", l.raw),
			connected:  liveSet[name],
			reconnects: stats.Reconnects,
		})
	}
	s.mu.Unlock()

	for _, sm := range samples {
		if err := st.scheduler.Register(sm.name, sm.kind); err != nil {
			utils.Debugf("[BOND] register %q: %v", sm.name, err)
			continue
		}
		st.scheduler.SetConnected(sm.name, sm.connected)
		st.scheduler.SetReconnects(sm.name, sm.reconnects)
	}
	snap := st.scheduler.Rebalance()
	utils.Debugf("[BOND] pool total=%d active=%d reserve=%d failed=%d",
		len(snap.Channels), len(snap.Active), len(snap.Reserve), len(snap.Failed))
}

func (s *Session) bondProbe(limit int) {
	if limit <= 0 {
		return
	}
	st := s.bondState()
	if st == nil {
		return
	}

	s.mu.Lock()
	if !s.ready || s.stopped {
		s.mu.Unlock()
		return
	}
	live := s.liveLinksLocked()
	s.mu.Unlock()
	if len(live) == 0 {
		return
	}
	if limit > len(live) {
		limit = len(live)
	}

	st.mu.Lock()
	start := st.probeCursor % len(live)
	st.probeCursor = (start + limit) % len(live)
	st.mu.Unlock()

	for i := 0; i < limit; i++ {
		l := live[(start+i)%len(live)]
		s.bondNotePingSent(l.name)
		go func(link *transportLink) {
			if err := s.sendControlVia(link, control.SubtypeLinkPing, nil); err != nil {
				utils.Debugf("[BOND] ping via %q: %v", link.name, err)
			}
		}(l)
	}
}

// BondSnapshot exposes current adaptive-pool state for diagnostics and a
// future UI/API. The bool is false when MultiBond is disabled.
func (s *Session) BondSnapshot() (bond.Snapshot, bool) {
	st := s.bondState()
	if st == nil {
		return bond.Snapshot{}, false
	}
	return st.scheduler.Snapshot(), true
}
