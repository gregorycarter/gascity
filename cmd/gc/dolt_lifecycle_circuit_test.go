package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/doltdisk"
)

func configureManagedDoltCircuitTestCity(t *testing.T) string {
	t.Helper()
	city := t.TempDir()
	dataDir := city + "/.beads/dolt"
	stateDir := city + "/.gc/runtime/packs/dolt"
	for key, value := range map[string]string{
		"GC_PACK_STATE_DIR":   stateDir,
		"GC_DOLT_DATA_DIR":    dataDir,
		"GC_DOLT_LOG_FILE":    stateDir + "/dolt.log",
		"GC_DOLT_STATE_FILE":  stateDir + "/dolt-provider-state.json",
		"GC_DOLT_PID_FILE":    stateDir + "/dolt.pid",
		"GC_DOLT_LOCK_FILE":   stateDir + "/dolt.lock",
		"GC_DOLT_CONFIG_FILE": stateDir + "/dolt-config.yaml",
	} {
		t.Setenv(key, value)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return city
}

func TestManagedDoltCircuitClassifiesFatalEvidence(t *testing.T) {
	tests := []struct {
		name string
		err  error
		log  string
		disk doltdisk.State
		want managedDoltCircuitReason
	}{
		{name: "journal enospc error", err: errors.New("write journal: no space left on device"), want: managedDoltCircuitENOSPC},
		{name: "journal enospc log", log: "fatal: hq journal flush failed: ENOSPC", want: managedDoltCircuitENOSPC},
		{name: "critical disk", disk: doltdisk.StateCritical, want: managedDoltCircuitDiskCritical},
		{name: "unknown disk", disk: doltdisk.StateUnknown, want: managedDoltCircuitDiskUnknown},
		{name: "sql unavailable", err: errors.New("dolt server is not query-ready"), want: managedDoltCircuitSQLUnresponsive},
		{name: "unknown crash", err: errors.New("dolt server exited with status 1"), want: managedDoltCircuitUnknownCrash},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyManagedDoltCircuitFailure(tt.err, tt.log, tt.disk); got != tt.want {
				t.Fatalf("classifyManagedDoltCircuitFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagedDoltCircuitFailureStateIsDurableAndBounded(t *testing.T) {
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	cfg := managedDoltCircuitConfig{
		RetryWindow: 10 * time.Minute,
		MaxAttempts: 3,
		BaseBackoff: time.Second,
		MaxBackoff:  4 * time.Second,
	}
	state := managedDoltCircuitState{}
	for i := 0; i < cfg.MaxAttempts-1; i++ {
		state = recordManagedDoltCircuitFailure(state, managedDoltCircuitUnknownCrash, now.Add(time.Duration(i)*time.Second), cfg)
		if state.Open {
			t.Fatalf("attempt %d unexpectedly opened circuit", i+1)
		}
	}
	state = recordManagedDoltCircuitFailure(state, managedDoltCircuitUnknownCrash, now.Add(2*time.Second), cfg)
	if !state.Open {
		t.Fatal("retry ceiling did not open circuit")
	}
	if state.Reason != managedDoltCircuitRetryExhausted {
		t.Fatalf("reason = %q, want %q", state.Reason, managedDoltCircuitRetryExhausted)
	}
	if state.Attempts != cfg.MaxAttempts {
		t.Fatalf("attempts = %d, want %d", state.Attempts, cfg.MaxAttempts)
	}
	if state.OpenedAt.IsZero() || state.LastAttemptAt.IsZero() || state.WindowStartedAt.IsZero() {
		t.Fatalf("durable timestamps missing: %+v", state)
	}
	if state.NextAttemptAt.IsZero() {
		t.Fatalf("retry backoff timestamp missing: %+v", state)
	}

	state = recordManagedDoltCircuitFailure(state, managedDoltCircuitENOSPC, now.Add(3*time.Second), cfg)
	if state.Reason != managedDoltCircuitENOSPC || !state.Open {
		t.Fatalf("ENOSPC must remain immediately open: %+v", state)
	}
}

func TestManagedDoltCircuitBackoffIsBoundedWithJitter(t *testing.T) {
	cfg := managedDoltCircuitConfig{BaseBackoff: time.Second, MaxBackoff: 5 * time.Second, JitterFraction: 0.25}
	for attempt := 1; attempt <= 12; attempt++ {
		got := managedDoltCircuitBackoff(attempt, 0.0, cfg)
		if got < 0 || got > cfg.MaxBackoff {
			t.Fatalf("low jitter attempt %d = %s, outside [0,%s]", attempt, got, cfg.MaxBackoff)
		}
		got = managedDoltCircuitBackoff(attempt, 1.0, cfg)
		if got < 0 || got > cfg.MaxBackoff {
			t.Fatalf("high jitter attempt %d = %s, outside [0,%s]", attempt, got, cfg.MaxBackoff)
		}
	}
}

func TestManagedDoltCircuitResumeRequiresStabilityAndChecks(t *testing.T) {
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	cfg := managedDoltCircuitConfig{ResumeStability: time.Minute}
	state := managedDoltCircuitState{Open: true, Reason: managedDoltCircuitENOSPC, OpenedAt: now.Add(-10 * time.Minute)}
	report := doltdisk.Report{State: doltdisk.StateHealthy, AvailableBytes: 10 << 30, ResumeBytes: 4 << 30}

	allowed, next := managedDoltCircuitResume(state, report, false, true, now, cfg)
	if allowed || next.HeadroomSince.IsZero() {
		t.Fatalf("first headroom sample must start stability window: allowed=%v state=%+v", allowed, next)
	}
	allowed, next = managedDoltCircuitResume(next, report, true, true, now.Add(30*time.Second), cfg)
	if allowed {
		t.Fatal("stability window must not be bypassed")
	}
	allowed, next = managedDoltCircuitResume(next, report, true, true, now.Add(61*time.Second), cfg)
	if !allowed || next.Open {
		t.Fatalf("healthy headroom and checks should close circuit: allowed=%v state=%+v", allowed, next)
	}

	state = managedDoltCircuitState{Open: true, Reason: managedDoltCircuitENOSPC, OpenedAt: now.Add(-10 * time.Minute)}
	allowed, _ = managedDoltCircuitResume(state, report, true, false, now.Add(2*time.Minute), cfg)
	if allowed {
		t.Fatal("failed integrity check must keep circuit open")
	}
}

func TestManagedDoltCircuitReasonIsExplicitlyObservable(t *testing.T) {
	state := managedDoltCircuitState{Open: true, Reason: managedDoltCircuitDiskUnknown}
	fields := managedDoltCircuitFields(state)
	joined := strings.Join(fields, "\n")
	for _, want := range []string{"circuit_open\ttrue", "circuit_reason\tdisk_unknown"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fields %q missing %q", joined, want)
		}
	}
}

func TestManagedDoltCircuitBlocksRepeatedENOSPCRecoveryWithoutStarting(t *testing.T) {
	city := configureManagedDoltCircuitTestCity(t)
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	if err := managedDoltCircuitRecordFailure(city, errors.New("journal flush: no space left on device"), "", doltdisk.StateCritical, 0, now); err != nil {
		t.Fatalf("record circuit failure: %v", err)
	}
	starts := 0
	ops := managedDoltRecoveryOps{
		queryProbe: func(string, string, string) error { return errors.New("connection refused") },
		healthCheck: func(string, string, string) (managedDoltSQLHealthReport, error) {
			return managedDoltSQLHealthReport{}, errors.New("must not health-check while circuit is open")
		},
		stop: func(string, string) (managedDoltStopReport, error) {
			return managedDoltStopReport{}, errors.New("must not stop while circuit is open")
		},
		preflightCleanup: func(string) error { return errors.New("must not preflight while circuit is open") },
		start: func(string, string, string, string, string, time.Duration) (managedDoltStartReport, error) {
			starts++
			return managedDoltStartReport{}, errors.New("must not start while circuit is open")
		},
		publish:       func(string) error { return nil },
		failedCleanup: func(_ string, _ int, _ int, cause error) error { return cause },
		diskReport: func(string) (doltdisk.Report, error) {
			return doltdisk.Report{State: doltdisk.StateCritical, ResumeBytes: 4 << 30, AvailableBytes: 1 << 20}, nil
		},
		integrityCheck: func(string) (bool, error) { return true, nil },
		now:            func() time.Time { return now },
	}
	for i := 0; i < 3; i++ {
		if _, err := recoverManagedDoltProcessWithOps(city, "127.0.0.1", "3306", "root", "warning", time.Second, ops); err == nil || !strings.Contains(err.Error(), "circuit open") {
			t.Fatalf("recovery %d error = %v, want circuit-open refusal", i+1, err)
		}
	}
	if starts != 0 {
		t.Fatalf("ENOSPC recovery launched %d replacements", starts)
	}
	state, err := readManagedDoltCircuitState(city)
	if err != nil {
		t.Fatalf("read circuit state: %v", err)
	}
	if state.Attempts != 1 || state.Reason != managedDoltCircuitENOSPC {
		t.Fatalf("repeated circuit checks changed durable cause: %+v", state)
	}
}

func TestManagedDoltCircuitResumesExactlyOnceAfterStableHeadroom(t *testing.T) {
	city := configureManagedDoltCircuitTestCity(t)
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	if err := managedDoltCircuitRecordFailure(city, errors.New("journal flush: no space left on device"), "", doltdisk.StateCritical, 0, now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("record circuit failure: %v", err)
	}
	starts := 0
	probes := 0
	ops := managedDoltRecoveryOps{
		queryProbe: func(string, string, string) error {
			probes++
			if probes == 1 {
				return nil // resume gate SQL check
			}
			return errors.New("connection refused") // replacement is needed
		},
		healthCheck: func(string, string, string) (managedDoltSQLHealthReport, error) {
			return managedDoltSQLHealthReport{QueryReady: true, ReadOnly: "false"}, nil
		},
		stop:             func(string, string) (managedDoltStopReport, error) { return managedDoltStopReport{}, nil },
		preflightCleanup: func(string) error { return nil },
		start: func(string, string, string, string, string, time.Duration) (managedDoltStartReport, error) {
			starts++
			return managedDoltStartReport{Ready: true, PID: 9001, Port: 3306}, nil
		},
		publish:       func(string) error { return nil },
		failedCleanup: func(_ string, _ int, _ int, cause error) error { return cause },
		diskReport: func(string) (doltdisk.Report, error) {
			return doltdisk.Report{State: doltdisk.StateHealthy, ResumeBytes: 4 << 30, AvailableBytes: 10 << 30}, nil
		},
		integrityCheck: func(string) (bool, error) { return true, nil },
		now:            func() time.Time { return now },
	}
	if _, err := recoverManagedDoltProcessWithOps(city, "127.0.0.1", "3306", "root", "warning", time.Second, ops); err == nil {
		t.Fatal("first healthy sample bypassed stability gate")
	}
	now = now.Add(managedDoltCircuitDefaultResumeWindow + time.Second)
	if _, err := recoverManagedDoltProcessWithOps(city, "127.0.0.1", "3306", "root", "warning", time.Second, ops); err != nil {
		t.Fatalf("stable headroom recovery failed: %v", err)
	}
	if starts != 1 {
		t.Fatalf("stable headroom launched %d replacements, want exactly one", starts)
	}
	state, err := readManagedDoltCircuitState(city)
	if err != nil {
		t.Fatalf("read circuit state: %v", err)
	}
	if state.Open || state.Reason != managedDoltCircuitCleanExit {
		t.Fatalf("circuit remained open after recovery: %+v", state)
	}
}

func TestManagedDoltSingleOwnerRejectsConcurrentStart(t *testing.T) {
	city := configureManagedDoltCircuitTestCity(t)
	lockFile, _, err := openManagedDoltLifecycleLock(city)
	if err != nil {
		t.Fatalf("open lifecycle lock: %v", err)
	}
	locked, err := tryManagedDoltLifecycleLock(lockFile)
	if err != nil || !locked {
		t.Fatalf("acquire lifecycle lock: locked=%v err=%v", locked, err)
	}
	defer releaseManagedDoltLifecycleLock(lockFile)

	starts := 0
	original := managedDoltStartSQLServerFn
	t.Cleanup(func() { managedDoltStartSQLServerFn = original })
	managedDoltStartSQLServerFn = func(string, string, string, *os.File) (managedDoltStartedProcess, error) {
		starts++
		return managedDoltStartedProcess{}, errors.New("must not start while another owner holds the lock")
	}
	if _, err := startManagedDoltProcessWithOptions(city, "127.0.0.1", "3306", "root", "warning", -1, time.Second, false); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("concurrent start error = %v, want ownership refusal", err)
	}
	if starts != 0 {
		t.Fatalf("concurrent start invoked child %d times", starts)
	}
}
