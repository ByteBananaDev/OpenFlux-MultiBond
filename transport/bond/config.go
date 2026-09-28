package bond

import "time"

// Config controls the adaptive transport pool. The defaults are intentionally
// conservative: the scheduler can track up to 256 physical carriers, but it
// keeps at most 196 in the active set and prefers links whose averaged RTT is
// <=120 ms. Links above 150 ms are kept as reserve unless emergency capacity
// is required.
type Config struct {
	MaxChannels     int
	TargetActive    int
	MinActive       int
	PreferredRTT    time.Duration
	MaxRTT          time.Duration
	EmergencyMaxRTT time.Duration
	MaxRTTSpread    time.Duration
	RTTAlpha        float64
	ThroughputAlpha float64
	LossAlpha       float64
	BadRTTHold      time.Duration
	RecoveryHold    time.Duration
	PromoteMargin   float64
	MinRTTSamples   int
}

func DefaultConfig() Config {
	return Config{
		MaxChannels:     256,
		TargetActive:    196,
		MinActive:       4,
		PreferredRTT:    120 * time.Millisecond,
		MaxRTT:          150 * time.Millisecond,
		EmergencyMaxRTT: 300 * time.Millisecond,
		MaxRTTSpread:    30 * time.Millisecond,
		RTTAlpha:        0.20,
		ThroughputAlpha: 0.25,
		LossAlpha:       0.20,
		BadRTTHold:      5 * time.Second,
		RecoveryHold:    15 * time.Second,
		PromoteMargin:   0.10,
		MinRTTSamples:   3,
	}
}

func (c Config) normalized() Config {
	if c.MaxChannels <= 0 || c.MaxChannels > 256 {
		c.MaxChannels = 256
	}
	if c.TargetActive <= 0 || c.TargetActive > c.MaxChannels {
		c.TargetActive = min(196, c.MaxChannels)
	}
	if c.MinActive <= 0 {
		c.MinActive = 1
	}
	if c.MinActive > c.TargetActive {
		c.MinActive = c.TargetActive
	}
	if c.PreferredRTT <= 0 {
		c.PreferredRTT = 120 * time.Millisecond
	}
	if c.MaxRTT <= c.PreferredRTT {
		c.MaxRTT = 150 * time.Millisecond
	}
	if c.EmergencyMaxRTT <= c.MaxRTT {
		c.EmergencyMaxRTT = 300 * time.Millisecond
	}
	if c.MaxRTTSpread <= 0 {
		c.MaxRTTSpread = 30 * time.Millisecond
	}
	if c.RTTAlpha <= 0 || c.RTTAlpha > 1 {
		c.RTTAlpha = 0.20
	}
	if c.ThroughputAlpha <= 0 || c.ThroughputAlpha > 1 {
		c.ThroughputAlpha = 0.25
	}
	if c.LossAlpha <= 0 || c.LossAlpha > 1 {
		c.LossAlpha = 0.20
	}
	if c.BadRTTHold < 0 {
		c.BadRTTHold = 0
	}
	if c.RecoveryHold < 0 {
		c.RecoveryHold = 0
	}
	if c.PromoteMargin < 0 {
		c.PromoteMargin = 0
	}
	if c.MinRTTSamples <= 0 {
		c.MinRTTSamples = 3
	}
	return c
}
