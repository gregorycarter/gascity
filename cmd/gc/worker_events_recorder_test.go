package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	eventsexec "github.com/gastownhall/gascity/internal/events/exec"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/worker"
)

func TestWorkerEventsRecorderForCity(t *testing.T) {
	t.Setenv("GC_EVENTS", "")
	cityPath := t.TempDir()

	t.Run("empty cityPath disables recording", func(t *testing.T) {
		if rec := workerEventsRecorderForCity(nil, ""); rec != nil {
			t.Fatalf("want nil recorder without a city, got %T", rec)
		}
	})
	t.Run("default provider is a lazy file recorder", func(t *testing.T) {
		rec := workerEventsRecorderForCity(nil, cityPath)
		lazy, ok := rec.(*lazyEventsFileRecorder)
		if !ok {
			t.Fatalf("want *lazyEventsFileRecorder, got %T", rec)
		}
		wantPath := filepath.Join(cityPath, ".gc", "events.jsonl")
		if lazy.path != wantPath {
			t.Fatalf("recorder path = %q, want %q", lazy.path, wantPath)
		}
	})
	t.Run("config exec provider", func(t *testing.T) {
		cfg := &config.City{Events: config.EventsConfig{Provider: "exec:/bin/true"}}
		if _, ok := workerEventsRecorderForCity(cfg, cityPath).(*eventsexec.Provider); !ok {
			t.Fatalf("want exec provider")
		}
	})
	t.Run("GC_EVENTS fake overrides config", func(t *testing.T) {
		t.Setenv("GC_EVENTS", "fake")
		cfg := &config.City{Events: config.EventsConfig{Provider: "exec:/bin/true"}}
		if _, ok := workerEventsRecorderForCity(cfg, cityPath).(*events.Fake); !ok {
			t.Fatalf("want fake provider from GC_EVENTS")
		}
	})
	t.Run("fake without city still records", func(t *testing.T) {
		cfg := &config.City{Events: config.EventsConfig{Provider: "fake"}}
		if _, ok := workerEventsRecorderForCity(cfg, "").(*events.Fake); !ok {
			t.Fatalf("want fake provider without a city path")
		}
	})
}

// TestWorkerFactoryWithConfigThreadsEventsRecorder locks the regression behind
// ga-78r: CLI-built worker factories left FactoryConfig.Recorder unset, so no
// production session-lifecycle operation ever emitted a worker.operation event
// and `gc analyze reliability` grouped zero sessions.
func TestWorkerFactoryWithConfigThreadsEventsRecorder(t *testing.T) {
	t.Setenv("GC_EVENTS", "")
	cityPath := t.TempDir()
	sp := runtime.NewFake()

	f, err := workerFactoryWithConfig(cityPath, nil, sp, &config.City{})
	if err != nil {
		t.Fatalf("workerFactoryWithConfig: %v", err)
	}
	if f.Recorder() == nil {
		t.Fatalf("factory recorder is nil; worker.operation telemetry disabled")
	}

	noCity, err := workerFactoryWithConfig("", nil, sp, &config.City{})
	if err != nil {
		t.Fatalf("workerFactoryWithConfig(no city): %v", err)
	}
	if rec := noCity.Recorder(); rec != nil {
		t.Fatalf("want nil recorder without a city, got %T", rec)
	}
}

// TestWorkerFactoryOperationLandsInCityEventLog proves the wiring end to end:
// a lifecycle operation through a CLI-built worker handle must append a
// worker.operation event to the city's events.jsonl.
func TestWorkerFactoryOperationLandsInCityEventLog(t *testing.T) {
	t.Setenv("GC_EVENTS", "")
	cityPath := t.TempDir()
	sp := runtime.NewFake()

	f, err := workerFactoryWithConfig(cityPath, nil, sp, &config.City{})
	if err != nil {
		t.Fatalf("workerFactoryWithConfig: %v", err)
	}
	handle, err := f.RuntimeHandle("sess-1", "fake", "", nil)
	if err != nil {
		t.Fatalf("RuntimeHandle: %v", err)
	}
	// The op outcome does not matter — success and failure both record.
	_, _ = handle.Nudge(context.Background(), worker.NudgeRequest{Text: "wake up"})

	eventsPath := filepath.Join(cityPath, ".gc", "events.jsonl")
	got, err := events.ReadFiltered(eventsPath, events.Filter{Type: events.WorkerOperation})
	if err != nil {
		t.Fatalf("reading %s: %v", eventsPath, err)
	}
	if len(got) == 0 {
		t.Fatalf("no worker.operation events recorded in %s", eventsPath)
	}
	if got[0].Actor != "worker" {
		t.Fatalf("worker.operation actor = %q, want %q", got[0].Actor, "worker")
	}
}
