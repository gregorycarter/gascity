// Package doltdisk measures the filesystem containing a managed Dolt data
// directory and classifies its write headroom without mutating the store.
package doltdisk

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// State is the externally meaningful disk headroom classification.
type State string

const (
	StateUnknown  State = "unknown"
	StateHealthy  State = "healthy"
	StateWarning  State = "warning"
	StateCritical State = "critical"
)

// Config controls thresholds, hysteresis, and bounded growth projection.
type Config struct {
	CriticalBytes     int64
	WarningBytes      int64
	ResumeBytes       int64
	ReserveBytes      int64
	ProjectionHorizon time.Duration
	MaxSampleAge      time.Duration
	GrowthWindow      time.Duration
	ThresholdSource   string
}

// DefaultConfig returns conservative defaults. The reserve and projection
// policy catches a growing store before it reaches the instantaneous floor.
func DefaultConfig() Config {
	return Config{
		CriticalBytes:     500 << 20,
		WarningBytes:      2 << 30,
		ResumeBytes:       4 << 30,
		ReserveBytes:      2 << 30,
		ProjectionHorizon: 24 * time.Hour,
		MaxSampleAge:      5 * time.Minute,
		GrowthWindow:      6 * time.Hour,
		ThresholdSource:   "default",
	}
}

// NormalizeConfig validates a policy and fills omitted optional values. An
// invalid policy returns safe defaults together with an explanatory error.
func NormalizeConfig(c Config) (Config, error) {
	d := DefaultConfig()
	if c.ThresholdSource == "" {
		c.ThresholdSource = d.ThresholdSource
	}
	if c.CriticalBytes == 0 {
		return c, nil // explicit zero is the documented emergency disablement
	}
	if c.WarningBytes == 0 {
		c.WarningBytes = d.WarningBytes
	}
	if c.ResumeBytes == 0 {
		c.ResumeBytes = d.ResumeBytes
	}
	if c.ReserveBytes == 0 {
		c.ReserveBytes = d.ReserveBytes
	}
	if c.ProjectionHorizon == 0 {
		c.ProjectionHorizon = d.ProjectionHorizon
	}
	if c.MaxSampleAge == 0 {
		c.MaxSampleAge = d.MaxSampleAge
	}
	if c.GrowthWindow == 0 {
		c.GrowthWindow = d.GrowthWindow
	}
	if c.CriticalBytes < 0 || c.WarningBytes <= c.CriticalBytes || c.ResumeBytes < c.WarningBytes || c.ReserveBytes < c.CriticalBytes || c.ProjectionHorizon < 0 || c.MaxSampleAge < 0 || c.GrowthWindow < 0 {
		return d, fmt.Errorf("invalid disk budget thresholds or sampling policy")
	}
	return c, nil
}

// ProbeResult is a single filesystem capacity observation.
type ProbeResult struct {
	AvailableBytes int64
	Filesystem     string
	SampledAt      time.Time
}

// ProbeFunc is the injectable filesystem probe used by Budget and tests.
type ProbeFunc func(path string) (ProbeResult, error)

// Report is the non-mutating health snapshot returned after each sample.
type Report struct {
	State                   State
	PreviousState           State
	Transition              bool
	AvailableBytes          int64
	CriticalBytes           int64
	WarningBytes            int64
	ResumeBytes             int64
	ReserveBytes            int64
	ThresholdSource         string
	Filesystem              string
	SampledAt               time.Time
	ProbeAge                time.Duration
	ProbeError              string
	GrowthBytesPerSecond    float64
	GrowthWindow            time.Duration
	ProjectedReserveHorizon time.Duration
}

// MetricLabels returns the bounded label set suitable for a metric. It never
// includes a database name or path, which would create unbounded cardinality.
func (r Report) MetricLabels() string {
	return "state=" + string(r.State) + ",filesystem=" + r.Filesystem
}

type sample struct {
	ProbeResult
}

// Budget retains bounded observation history and hysteresis state.
type Budget struct {
	cfg     Config
	probe   ProbeFunc
	now     func() time.Time
	state   State
	samples []sample
}

// New constructs a disk budget evaluator. Nil probe/clock values use safe
// defaults; callers may supply them to isolate filesystem and time effects.
func New(cfg Config, probe ProbeFunc, now func() time.Time) *Budget {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		normalized = DefaultConfig()
	}
	if probe == nil {
		probe = Probe
	}
	if now == nil {
		now = time.Now
	}
	return &Budget{cfg: normalized, probe: probe, now: now, state: StateUnknown}
}

// Sample probes path and updates the deterministic hysteresis state.
func (b *Budget) Sample(path string) Report {
	now := b.now()
	report := Report{
		State:           StateUnknown,
		PreviousState:   b.state,
		CriticalBytes:   b.cfg.CriticalBytes,
		WarningBytes:    b.cfg.WarningBytes,
		ResumeBytes:     b.cfg.ResumeBytes,
		ReserveBytes:    b.cfg.ReserveBytes,
		ThresholdSource: b.cfg.ThresholdSource,
		GrowthWindow:    b.cfg.GrowthWindow,
	}
	if b.cfg.CriticalBytes == 0 {
		report.State = StateHealthy
		report.PreviousState = b.state
		report.Transition = b.state != report.State
		b.state = report.State
		return report
	}
	observation, err := b.probe(path)
	if err != nil {
		report.ProbeError = err.Error()
		return b.finish(report)
	}
	if observation.SampledAt.IsZero() {
		observation.SampledAt = now
	}
	report.AvailableBytes = observation.AvailableBytes
	report.Filesystem = observation.Filesystem
	report.SampledAt = observation.SampledAt
	report.ProbeAge = nonNegativeDuration(now.Sub(observation.SampledAt))
	if b.cfg.MaxSampleAge > 0 && report.ProbeAge > b.cfg.MaxSampleAge {
		report.ProbeError = fmt.Sprintf("stale disk probe sample: age %s exceeds %s", report.ProbeAge, b.cfg.MaxSampleAge)
		return b.finish(report)
	}
	if len(b.samples) > 0 && observation.SampledAt.Before(b.samples[len(b.samples)-1].SampledAt) {
		// A clock or probe reset must not produce a negative/overstated rate.
		b.samples = nil
	}
	b.samples = append(b.samples, sample{ProbeResult: observation})
	b.trimSamples(observation.SampledAt)
	report.GrowthBytesPerSecond = b.growthRate()
	if report.GrowthBytesPerSecond > 0 && b.cfg.ReserveBytes > 0 && observation.AvailableBytes > b.cfg.ReserveBytes {
		seconds := float64(observation.AvailableBytes-b.cfg.ReserveBytes) / report.GrowthBytesPerSecond
		report.ProjectedReserveHorizon = durationFromSeconds(seconds)
	}
	raw := b.rawState(observation.AvailableBytes, report.GrowthBytesPerSecond, report.ProjectedReserveHorizon)
	report.State = b.applyHysteresis(raw, observation.AvailableBytes)
	return b.finish(report)
}

func (b *Budget) finish(report Report) Report {
	if report.State == "" {
		report.State = StateUnknown
	}
	report.Transition = report.State != b.state
	report.PreviousState = b.state
	b.state = report.State
	return report
}

func (b *Budget) rawState(free int64, rate float64, horizon time.Duration) State {
	if free < b.cfg.CriticalBytes {
		return StateCritical
	}
	if b.cfg.ProjectionHorizon > 0 && horizon > 0 && horizon <= b.cfg.ProjectionHorizon {
		return StateCritical
	}
	if free < b.cfg.WarningBytes {
		return StateWarning
	}
	if rate > 0 && b.cfg.ReserveBytes > 0 && free > b.cfg.ReserveBytes {
		projected := float64(free) - rate*b.cfg.ProjectionHorizon.Seconds()
		if projected < float64(b.cfg.WarningBytes) {
			return StateWarning
		}
	}
	return StateHealthy
}

func (b *Budget) applyHysteresis(raw State, free int64) State {
	switch b.state {
	case StateCritical:
		if raw != StateCritical && free >= b.cfg.ResumeBytes {
			return StateHealthy
		}
		return StateCritical
	case StateWarning:
		if raw == StateCritical {
			return StateCritical
		}
		if free >= b.cfg.ResumeBytes && raw == StateHealthy {
			return StateHealthy
		}
		return StateWarning
	default:
		return raw
	}
}

func (b *Budget) trimSamples(latest time.Time) {
	if b.cfg.GrowthWindow <= 0 {
		b.samples = b.samples[len(b.samples)-1:]
		return
	}
	cutoff := latest.Add(-b.cfg.GrowthWindow)
	keep := sort.Search(len(b.samples), func(i int) bool { return !b.samples[i].SampledAt.Before(cutoff) })
	if keep > 0 {
		b.samples = append([]sample(nil), b.samples[keep:]...)
	}
}

func (b *Budget) growthRate() float64 {
	if len(b.samples) < 2 {
		return 0
	}
	first, last := b.samples[0], b.samples[len(b.samples)-1]
	seconds := last.SampledAt.Sub(first.SampledAt).Seconds()
	if seconds <= 0 || first.AvailableBytes <= last.AvailableBytes {
		return 0
	}
	return float64(first.AvailableBytes-last.AvailableBytes) / seconds
}

func durationFromSeconds(seconds float64) time.Duration {
	if seconds <= 0 || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func nonNegativeDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// ConfigFromEnv resolves the supported environment overrides over defaults.
// Invalid values are ignored and the safe default remains active.
func ConfigFromEnv(getenv func(string) string) Config {
	c := DefaultConfig()
	if getenv == nil {
		return c
	}
	for _, item := range []struct {
		name string
		dest *int64
	}{
		{"GC_DOLT_MIN_FREE_BYTES", &c.CriticalBytes},
		{"GC_DOLT_WARN_FREE_BYTES", &c.WarningBytes},
		{"GC_DOLT_RESUME_FREE_BYTES", &c.ResumeBytes},
		{"GC_DOLT_RESERVE_BYTES", &c.ReserveBytes},
	} {
		if raw := strings.TrimSpace(getenv(item.name)); raw != "" {
			var value int64
			if _, err := fmt.Sscan(raw, &value); err == nil && value >= 0 {
				*item.dest = value
				c.ThresholdSource = "environment"
			}
		}
	}
	for _, item := range []struct {
		name string
		dest *time.Duration
	}{
		{"GC_DOLT_GROWTH_WINDOW", &c.GrowthWindow},
		{"GC_DOLT_PROJECTION_HORIZON", &c.ProjectionHorizon},
		{"GC_DOLT_SAMPLE_MAX_AGE", &c.MaxSampleAge},
	} {
		if raw := strings.TrimSpace(getenv(item.name)); raw != "" {
			if value, err := time.ParseDuration(raw); err == nil && value >= 0 {
				*item.dest = value
				c.ThresholdSource = "environment"
			}
		}
	}
	if normalized, err := NormalizeConfig(c); err == nil {
		return normalized
	}
	return DefaultConfig()
}
