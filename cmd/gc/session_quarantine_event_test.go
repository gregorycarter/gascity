package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/events"
)

// ga-78r: `gc analyze reliability` reserves a Quarantined column, but no
// production path ever emitted session.quarantined. These tests pin emission at
// the three quarantine decision sites: wake-failure crash loops, churn cycles,
// and provider rate-limit backoff.

func listQuarantinedEvents(t *testing.T, rec *events.Fake) []events.Event {
	t.Helper()
	all, err := rec.List(events.Filter{Type: events.SessionQuarantined})
	if err != nil {
		t.Fatalf("listing session.quarantined events: %v", err)
	}
	return all
}

func TestRecordWakeFailureEmitsSessionQuarantined(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	store := newTestStore()
	rec := events.NewFake()
	session := makeBead("b1", map[string]string{
		"wake_attempts": strconv.Itoa(defaultMaxWakeAttempts - 1),
	})

	recordWakeFailure(seedSessionInfo(session), sessionFrontDoor(store), clk, "rig/worker-1", rec)

	got := listQuarantinedEvents(t, rec)
	if len(got) != 1 {
		t.Fatalf("session.quarantined events = %d, want 1", len(got))
	}
	if got[0].Subject != "rig/worker-1" {
		t.Fatalf("subject = %q, want rig/worker-1", got[0].Subject)
	}
	if got[0].Actor != "gc" {
		t.Fatalf("actor = %q, want gc", got[0].Actor)
	}
	if got[0].Message == "" {
		t.Fatal("message should name the quarantine reason")
	}
}

func TestRecordWakeFailureBelowThresholdEmitsNoQuarantine(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	store := newTestStore()
	rec := events.NewFake()
	session := makeBead("b1", map[string]string{"wake_attempts": "1"})

	recordWakeFailure(seedSessionInfo(session), sessionFrontDoor(store), clk, "rig/worker-1", rec)

	if got := listQuarantinedEvents(t, rec); len(got) != 0 {
		t.Fatalf("session.quarantined events = %d, want 0 below the threshold", len(got))
	}
}

func TestRecordWakeFailureToleratesNilRecorder(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	store := newTestStore()
	session := makeBead("b1", map[string]string{
		"wake_attempts": strconv.Itoa(defaultMaxWakeAttempts - 1),
	})

	// Must not panic; the quarantine write still lands.
	recordWakeFailure(seedSessionInfo(session), sessionFrontDoor(store), clk, "rig/worker-1", nil)
	syncBeadFromStore(&session, store)
	if session.Metadata["quarantined_until"] == "" {
		t.Fatal("quarantine metadata should persist with a nil recorder")
	}
}

func TestRecordChurnEmitsSessionQuarantined(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	store := newTestStore()
	rec := events.NewFake()
	session := makeBead("b1", map[string]string{
		"churn_count": strconv.Itoa(defaultMaxChurnCycles - 1),
	})

	recordChurn(seedSessionInfo(session), sessionFrontDoor(store), clk, "rig/worker-2", rec)

	got := listQuarantinedEvents(t, rec)
	if len(got) != 1 {
		t.Fatalf("session.quarantined events = %d, want 1", len(got))
	}
	if got[0].Subject != "rig/worker-2" {
		t.Fatalf("subject = %q, want rig/worker-2", got[0].Subject)
	}
}

func TestRecordRateLimitQuarantineEmitsSessionQuarantined(t *testing.T) {
	clk := &clock.Fake{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	store := newTestStore()
	rec := events.NewFake()
	session := makeBead("b1", nil)

	if _, err := recordRateLimitQuarantine(seedSessionInfo(session), sessionFrontDoor(store), clk, "rig/worker-3", rec); err != nil {
		t.Fatalf("recordRateLimitQuarantine: %v", err)
	}

	got := listQuarantinedEvents(t, rec)
	if len(got) != 1 {
		t.Fatalf("session.quarantined events = %d, want 1", len(got))
	}
	if got[0].Subject != "rig/worker-3" {
		t.Fatalf("subject = %q, want rig/worker-3", got[0].Subject)
	}
}
