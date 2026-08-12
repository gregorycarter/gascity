package main

import (
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
)

func TestShouldRunHealWatchdog(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	enabled := &config.City{Heal: config.HealConfig{
		Enabled: true,
		Targets: []config.HealTarget{{Rig: "demo"}},
	}}

	if shouldRunHealWatchdog(nil, time.Time{}, now) {
		t.Error("nil config: watchdog ran")
	}
	disabled := &config.City{Heal: config.HealConfig{Targets: []config.HealTarget{{Rig: "demo"}}}}
	if shouldRunHealWatchdog(disabled, time.Time{}, now) {
		t.Error("disabled config: watchdog ran")
	}
	unconfigured := &config.City{Heal: config.HealConfig{Enabled: true}}
	if shouldRunHealWatchdog(unconfigured, time.Time{}, now) {
		t.Error("enabled but no targets/critical sessions: watchdog ran")
	}
	if !shouldRunHealWatchdog(enabled, time.Time{}, now) {
		t.Error("first run: watchdog did not run")
	}
	if shouldRunHealWatchdog(enabled, now.Add(-time.Minute), now) {
		t.Error("within default 5m interval: watchdog ran")
	}
	if !shouldRunHealWatchdog(enabled, now.Add(-6*time.Minute), now) {
		t.Error("past interval: watchdog did not run")
	}

	// A sub-minute configured interval is floored to a minute.
	fast := &config.City{Heal: config.HealConfig{
		Enabled:  true,
		Interval: "1s",
		Targets:  []config.HealTarget{{Rig: "demo"}},
	}}
	if shouldRunHealWatchdog(fast, now.Add(-30*time.Second), now) {
		t.Error("sub-minute interval not floored")
	}
	if !shouldRunHealWatchdog(fast, now.Add(-2*time.Minute), now) {
		t.Error("floored interval: watchdog did not run")
	}

	// Critical sessions alone are a valid configuration.
	criticalOnly := &config.City{Heal: config.HealConfig{
		Enabled:          true,
		CriticalSessions: []string{"coordinator"},
	}}
	if !shouldRunHealWatchdog(criticalOnly, time.Time{}, now) {
		t.Error("critical-sessions-only config: watchdog did not run")
	}
}

func TestRunHealWatchdogDoesNotAdvanceScheduleWhilePassIsInFlight(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	last := now.Add(-6 * time.Minute)
	cr := &CityRuntime{cfg: &config.City{Heal: config.HealConfig{
		Enabled: true,
		Targets: []config.HealTarget{{Rig: "demo"}},
	}}, healWatchdogLast: last}
	cr.healWatchdogInFlight.Store(true)

	cr.runHealWatchdog(now)

	if got := cr.healWatchdogLast; !got.Equal(last) {
		t.Fatalf("healWatchdogLast = %s, want unchanged %s while pass is in flight", got, last)
	}
}

// TestHealRecentActionsRoundTripsSubjects proves the cooldown read-back sees
// exactly the subjects the pass records: what record() writes to the event
// log is what RecentActions keys the cooldown on.
func TestHealRecentActionsRoundTripsSubjects(t *testing.T) {
	rec, err := events.NewFileRecorder(filepath.Join(t.TempDir(), "events.jsonl"), io.Discard)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	defer rec.Close() //nolint:errcheck

	env := newHealTestEnv(t)
	env.landed = 0
	// The recorder stamps event timestamps from the wall clock, so this test
	// keeps pass-now at wall time (not the fixture's future-shifted clock)
	// and shrinks the staleness thresholds instead.
	env.now = time.Now().Add(time.Second)
	env.cfg.Heal.StallAfter = "1ms"
	env.cfg.Heal.OrphanStaleAfter = "1ms"
	orphan := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	deps := env.deps()
	deps.Rec = rec
	deps.RecentActions = func(since time.Time) map[string]time.Time {
		return healRecentActions(rec, since)
	}
	runHealPass(deps)

	if got := env.get(t, orphan.ID); got.Assignee != "" {
		t.Fatalf("orphan not released on first pass: assignee=%q", got.Assignee)
	}

	recent := healRecentActions(rec, time.Now().Add(-time.Hour))
	if _, ok := recent["bead:"+orphan.ID]; !ok {
		t.Fatalf("recorded action subject does not round-trip: got %v, want key %q", recent, "bead:"+orphan.ID)
	}

	// Re-orphan the bead: the second pass must be suppressed by the cooldown
	// read back from the durable event log, not by in-memory state.
	if err := env.store.Update(orphan.ID, beads.UpdateOpts{Assignee: strPtr("coordinator-address")}); err != nil {
		t.Fatalf("re-orphan: %v", err)
	}
	runHealPass(deps)
	if got := env.get(t, orphan.ID); got.Assignee != "coordinator-address" {
		t.Errorf("second pass released within cooldown: assignee=%q", got.Assignee)
	}
}
