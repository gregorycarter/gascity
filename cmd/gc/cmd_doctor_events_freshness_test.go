package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/events"
)

// ga-78r smoke check: a telemetry stream that silently reads empty while the
// city is demonstrably running is worse than no telemetry, because it reads as
// "all clear". These tests pin the failure conditions.

func writeFreshnessEventLog(t *testing.T, cityPath string, ts time.Time) {
	t.Helper()
	rec, err := events.NewFileRecorder(filepath.Join(cityPath, ".gc", "events.jsonl"), os.Stderr)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	rec.Record(events.Event{Type: "bead.updated", Actor: "test", Ts: ts})
	if err := rec.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}
}

func freshnessCheckForTest(liveSessions int, activity time.Time, now time.Time) *eventsFreshnessCheck {
	return &eventsFreshnessCheck{
		liveSessionCount:   func() (int, error) { return liveSessions, nil },
		latestBeadActivity: func() (time.Time, error) { return activity, nil },
		now:                func() time.Time { return now },
	}
}

func TestEventsFreshnessSilentStreamWhileRunningFails(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// Newest event is an hour old; beads changed one minute ago; 3 live sessions.
	writeFreshnessEventLog(t, cityPath, now.Add(-time.Hour))
	check := freshnessCheckForTest(3, now.Add(-time.Minute), now)

	res := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if res.Status != doctor.StatusError {
		t.Fatalf("status = %v, want StatusError (silent stream while agents run); message=%s", res.Status, res.Message)
	}
	if res.Severity != doctor.SeverityBlocking {
		t.Fatalf("severity = %v, want blocking — a dead telemetry stream must fail loudly", res.Severity)
	}
}

func TestEventsFreshnessMissingLogWhileRunningFails(t *testing.T) {
	cityPath := t.TempDir() // no events.jsonl at all
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	check := freshnessCheckForTest(2, now.Add(-time.Minute), now)

	res := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if res.Status != doctor.StatusError {
		t.Fatalf("status = %v, want StatusError (no event log while agents run); message=%s", res.Status, res.Message)
	}
}

func TestEventsFreshnessFreshStreamPasses(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	writeFreshnessEventLog(t, cityPath, now.Add(-time.Minute))
	check := freshnessCheckForTest(3, now.Add(-time.Minute), now)

	res := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if res.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK; message=%s", res.Status, res.Message)
	}
}

func TestEventsFreshnessNoLiveSessionsPasses(t *testing.T) {
	cityPath := t.TempDir() // stale/no log is fine when nothing runs
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	check := freshnessCheckForTest(0, now.Add(-time.Minute), now)

	res := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if res.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK when no agent sessions run; message=%s", res.Status, res.Message)
	}
}

func TestEventsFreshnessQuietBeadsPasses(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// Sessions alive but idle: no bead changed inside the window, so a quiet
	// stream is legitimate (an agent thinking for an hour emits nothing).
	writeFreshnessEventLog(t, cityPath, now.Add(-2*time.Hour))
	check := freshnessCheckForTest(3, now.Add(-2*time.Hour), now)

	res := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if res.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK for a quiet-but-alive city; message=%s", res.Status, res.Message)
	}
}

func TestEventsFreshnessProbeFailureWarns(t *testing.T) {
	cityPath := t.TempDir()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	check := &eventsFreshnessCheck{
		liveSessionCount:   func() (int, error) { return 0, os.ErrPermission },
		latestBeadActivity: func() (time.Time, error) { return time.Time{}, nil },
		now:                func() time.Time { return now },
	}

	res := check.Run(&doctor.CheckContext{CityPath: cityPath})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning when the probe itself fails; message=%s", res.Status, res.Message)
	}
}
