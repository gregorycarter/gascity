package doltdisk

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		CriticalBytes:     100,
		WarningBytes:      200,
		ResumeBytes:       300,
		ReserveBytes:      100,
		ProjectionHorizon: time.Hour,
		MaxSampleAge:      10 * time.Minute,
		GrowthWindow:      time.Hour,
		ThresholdSource:   "test",
	}
}

func TestBudgetStateTransitionsAreHysteretic(t *testing.T) {
	now := time.Unix(100, 0)
	free := int64(500)
	b := New(testConfig(), func(string) (ProbeResult, error) {
		return ProbeResult{AvailableBytes: free, Filesystem: "fs:test", SampledAt: now}, nil
	}, func() time.Time { return now })

	tests := []struct {
		name       string
		free       int64
		want       State
		transition bool
	}{
		{"healthy", 500, StateHealthy, true},
		{"warning", 199, StateWarning, true},
		{"warning remains below resume", 250, StateWarning, false},
		{"critical", 99, StateCritical, true},
		{"critical remains below resume", 299, StateCritical, false},
		{"resume", 300, StateHealthy, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			free = tt.free
			r := b.Sample("/data")
			if r.State != tt.want {
				t.Fatalf("state = %q, want %q (report=%+v)", r.State, tt.want, r)
			}
			if r.Transition != tt.transition {
				t.Fatalf("transition = %v, want %v", r.Transition, tt.transition)
			}
		})
	}
}

func TestBudgetProbeErrorAndStaleSampleAreUnknown(t *testing.T) {
	now := time.Unix(100, 0)
	probeErr := errors.New("permission denied")
	probeResult := func(string) (ProbeResult, error) {
		return ProbeResult{AvailableBytes: 999, Filesystem: "fs:test", SampledAt: now.Add(-time.Hour)}, probeErr
	}
	var probe func(string) (ProbeResult, error) = probeResult
	b := New(testConfig(), func(path string) (ProbeResult, error) {
		return probe(path)
	}, func() time.Time { return now })

	r := b.Sample("/data")
	if r.State != StateUnknown {
		t.Fatalf("error state = %q, want unknown", r.State)
	}
	if !strings.Contains(r.ProbeError, probeErr.Error()) {
		t.Fatalf("probe error = %q, want %q", r.ProbeError, probeErr)
	}

	probe = func(string) (ProbeResult, error) {
		return ProbeResult{AvailableBytes: 999, Filesystem: "fs:test", SampledAt: now.Add(-time.Hour)}, nil
	}
	r = b.Sample("/data")
	if r.State != StateUnknown || !strings.Contains(r.ProbeError, "stale") {
		t.Fatalf("stale report = %+v, want unknown stale error", r)
	}
}

func TestBudgetProjectsGrowthWithoutDatabaseLabels(t *testing.T) {
	now := time.Unix(100, 0)
	free := int64(500)
	b := New(testConfig(), func(string) (ProbeResult, error) {
		return ProbeResult{AvailableBytes: free, Filesystem: "fs:test", SampledAt: now}, nil
	}, func() time.Time { return now })

	first := b.Sample("/data")
	if first.GrowthBytesPerSecond != 0 {
		t.Fatalf("first growth rate = %v, want zero", first.GrowthBytesPerSecond)
	}
	now = now.Add(10 * time.Minute)
	free = 150
	r := b.Sample("/data")
	if r.GrowthBytesPerSecond <= 0 {
		t.Fatalf("growth rate = %v, want positive", r.GrowthBytesPerSecond)
	}
	if r.ProjectedReserveHorizon <= 0 || r.ProjectedReserveHorizon > time.Hour {
		t.Fatalf("projected reserve horizon = %v, want bounded positive horizon", r.ProjectedReserveHorizon)
	}
	if r.State != StateCritical {
		t.Fatalf("projected state = %q, want critical", r.State)
	}
	if strings.Contains(r.MetricLabels(), "database") {
		t.Fatalf("metric labels contain a database dimension: %q", r.MetricLabels())
	}
}

func TestNormalizeConfigUsesSafeOrdering(t *testing.T) {
	c := Config{CriticalBytes: 500, WarningBytes: 100, ResumeBytes: 50}
	normalized, err := NormalizeConfig(c)
	if err == nil {
		t.Fatal("NormalizeConfig returned nil error for inverted thresholds")
	}
	if normalized.CriticalBytes >= normalized.WarningBytes || normalized.WarningBytes >= normalized.ResumeBytes {
		t.Fatalf("normalized thresholds not ordered: %+v", normalized)
	}
}

func TestConfigFromEnvPrecedenceAndDisablement(t *testing.T) {
	values := map[string]string{
		"GC_DOLT_MIN_FREE_BYTES":    "0",
		"GC_DOLT_WARN_FREE_BYTES":   "2048",
		"GC_DOLT_RESUME_FREE_BYTES": "4096",
	}
	c := ConfigFromEnv(func(key string) string { return values[key] })
	if c.CriticalBytes != 0 {
		t.Fatalf("critical bytes = %d, want explicit disablement", c.CriticalBytes)
	}
	if c.WarningBytes != 2048 || c.ResumeBytes != 4096 {
		t.Fatalf("environment thresholds = %+v, want warning=2048 resume=4096", c)
	}

	values["GC_DOLT_WARN_FREE_BYTES"] = "not-a-number"
	c = ConfigFromEnv(func(key string) string { return values[key] })
	if c.WarningBytes != DefaultConfig().WarningBytes {
		t.Fatalf("invalid warning override = %d, want safe default %d", c.WarningBytes, DefaultConfig().WarningBytes)
	}
}
