package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/doltdisk"
	"github.com/gastownhall/gascity/internal/fsys"
)

// managedDoltCircuitReason identifies the durable cause that prevents an
// automatic replacement. The values are intentionally stable: operators and
// diagnostics may persist or aggregate them without parsing log text.
type managedDoltCircuitReason string

const (
	managedDoltCircuitENOSPC          managedDoltCircuitReason = "enospc_journal"
	managedDoltCircuitDiskCritical    managedDoltCircuitReason = "disk_critical"
	managedDoltCircuitDiskUnknown     managedDoltCircuitReason = "disk_unknown"
	managedDoltCircuitSQLUnresponsive managedDoltCircuitReason = "sql_unresponsive"
	managedDoltCircuitCleanExit       managedDoltCircuitReason = "clean_exit"
	managedDoltCircuitUnknownCrash    managedDoltCircuitReason = "unknown_crash"
	managedDoltCircuitRetryExhausted  managedDoltCircuitReason = "retry_exhausted"
)

// managedDoltCircuitState is persisted beside the provider state. It is
// lifecycle evidence, not a process-status file: the process table and SQL
// probe remain authoritative for liveness.
type managedDoltCircuitState struct {
	Open            bool                     `json:"open"`
	Reason          managedDoltCircuitReason `json:"reason,omitempty"`
	OpenedAt        time.Time                `json:"opened_at,omitempty"`
	LastTransition  time.Time                `json:"last_transition_at,omitempty"`
	LastAttemptAt   time.Time                `json:"last_attempt_at,omitempty"`
	WindowStartedAt time.Time                `json:"window_started_at,omitempty"`
	HeadroomSince   time.Time                `json:"headroom_since,omitempty"`
	NextAttemptAt   time.Time                `json:"next_attempt_at,omitempty"`
	Attempts        int                      `json:"attempts"`
	LastPID         int                      `json:"last_pid,omitempty"`
	LastError       string                   `json:"last_error,omitempty"`
}

// managedDoltCircuitConfig bounds automated recovery and its resume gate.
type managedDoltCircuitConfig struct {
	RetryWindow     time.Duration
	MaxAttempts     int
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration
	JitterFraction  float64
	ResumeStability time.Duration
}

const (
	managedDoltCircuitStateFileName       = "dolt-lifecycle-circuit.json"
	managedDoltCircuitRetryWindowEnv      = "GC_DOLT_CIRCUIT_RETRY_WINDOW_SECONDS"
	managedDoltCircuitMaxAttemptsEnv      = "GC_DOLT_CIRCUIT_MAX_ATTEMPTS"
	managedDoltCircuitResumeStabilityEnv  = "GC_DOLT_CIRCUIT_RESUME_STABILITY_SECONDS"
	managedDoltCircuitDefaultRetryWindow  = 10 * time.Minute
	managedDoltCircuitDefaultMaxAttempts  = 5
	managedDoltCircuitDefaultBaseBackoff  = 2 * time.Second
	managedDoltCircuitDefaultMaxBackoff   = 2 * time.Minute
	managedDoltCircuitDefaultJitter       = 0.25
	managedDoltCircuitDefaultResumeWindow = 2 * time.Minute
)

func defaultManagedDoltCircuitConfig() managedDoltCircuitConfig {
	return managedDoltCircuitConfig{
		RetryWindow:     managedDoltCircuitDurationFromEnvSeconds(managedDoltCircuitRetryWindowEnv, managedDoltCircuitDefaultRetryWindow),
		MaxAttempts:     managedDoltCircuitIntFromEnv(managedDoltCircuitMaxAttemptsEnv, managedDoltCircuitDefaultMaxAttempts),
		BaseBackoff:     managedDoltCircuitDefaultBaseBackoff,
		MaxBackoff:      managedDoltCircuitDefaultMaxBackoff,
		JitterFraction:  managedDoltCircuitDefaultJitter,
		ResumeStability: managedDoltCircuitDurationFromEnvSeconds(managedDoltCircuitResumeStabilityEnv, managedDoltCircuitDefaultResumeWindow),
	}
}

func managedDoltCircuitDurationFromEnvSeconds(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func managedDoltCircuitIntFromEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func managedDoltCircuitPath(cityPath string) string {
	return filepath.Join(filepath.Dir(providerManagedDoltStatePath(cityPath)), managedDoltCircuitStateFileName)
}

func readManagedDoltCircuitState(cityPath string) (managedDoltCircuitState, error) {
	data, err := os.ReadFile(managedDoltCircuitPath(cityPath))
	if err != nil {
		return managedDoltCircuitState{}, err
	}
	var state managedDoltCircuitState
	if err := json.Unmarshal(data, &state); err != nil {
		return managedDoltCircuitState{}, fmt.Errorf("decode managed dolt circuit state: %w", err)
	}
	return state, nil
}

func writeManagedDoltCircuitState(cityPath string, state managedDoltCircuitState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode managed dolt circuit state: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(managedDoltCircuitPath(cityPath)), 0o755); err != nil {
		return err
	}
	return fsys.WriteFileAtomic(fsys.OSFS{}, managedDoltCircuitPath(cityPath), data, 0o644)
}

func classifyManagedDoltCircuitFailure(err error, logText string, diskState doltdisk.State) managedDoltCircuitReason {
	combined := strings.ToLower(strings.TrimSpace(logText))
	if err != nil {
		combined += "\n" + strings.ToLower(err.Error())
	}
	if strings.Contains(combined, "no space left on device") || strings.Contains(combined, "enospc") ||
		(strings.Contains(combined, "journal") && (strings.Contains(combined, "flush") || strings.Contains(combined, "write"))) {
		return managedDoltCircuitENOSPC
	}
	switch diskState {
	case doltdisk.StateCritical:
		return managedDoltCircuitDiskCritical
	case doltdisk.StateUnknown:
		if strings.Contains(combined, "disk state unknown") || strings.Contains(combined, "below the floor") ||
			(err == nil && strings.TrimSpace(logText) == "") {
			if strings.Contains(combined, "below the floor") {
				return managedDoltCircuitDiskCritical
			}
			return managedDoltCircuitDiskUnknown
		}
	}
	if err == nil && strings.Contains(combined, "clean") && strings.Contains(combined, "exit") {
		return managedDoltCircuitCleanExit
	}
	if strings.Contains(combined, "not query-ready") || strings.Contains(combined, "query unavailable") ||
		strings.Contains(combined, "connection refused") || strings.Contains(combined, "timeout") ||
		strings.Contains(combined, "unresponsive") {
		return managedDoltCircuitSQLUnresponsive
	}
	return managedDoltCircuitUnknownCrash
}

func recordManagedDoltCircuitFailure(state managedDoltCircuitState, reason managedDoltCircuitReason, now time.Time, cfg managedDoltCircuitConfig) managedDoltCircuitState {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = managedDoltCircuitDefaultMaxAttempts
	}
	if cfg.RetryWindow <= 0 {
		cfg.RetryWindow = managedDoltCircuitDefaultRetryWindow
	}
	if state.WindowStartedAt.IsZero() || now.Sub(state.WindowStartedAt) >= cfg.RetryWindow {
		state.WindowStartedAt = now
		state.Attempts = 0
	}
	state.Attempts++
	state.LastAttemptAt = now
	state.LastTransition = now
	state.Reason = reason
	state.HeadroomSince = time.Time{}
	state.NextAttemptAt = now.Add(managedDoltCircuitBackoff(state.Attempts, managedDoltCircuitJitter(now, state.Attempts), cfg))
	if reason == managedDoltCircuitCleanExit {
		state.Open = false
		state.Attempts = 0
		state.NextAttemptAt = time.Time{}
		return state
	}
	state.Open = reason == managedDoltCircuitENOSPC || reason == managedDoltCircuitDiskCritical ||
		reason == managedDoltCircuitDiskUnknown || state.Attempts >= cfg.MaxAttempts
	if state.Open {
		if state.OpenedAt.IsZero() {
			state.OpenedAt = now
		}
		if state.Attempts >= cfg.MaxAttempts && reason != managedDoltCircuitENOSPC &&
			reason != managedDoltCircuitDiskCritical && reason != managedDoltCircuitDiskUnknown {
			state.Reason = managedDoltCircuitRetryExhausted
		}
	}
	return state
}

func managedDoltCircuitJitter(now time.Time, attempt int) float64 {
	seed := uint64(now.UnixNano()) ^ uint64(attempt*1103515245)
	return float64(seed%1000) / 999
}

func managedDoltCircuitBackoff(attempt int, jitter float64, cfg managedDoltCircuitConfig) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = managedDoltCircuitDefaultBaseBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = managedDoltCircuitDefaultMaxBackoff
	}
	backoff := float64(cfg.BaseBackoff)
	for i := 1; i < attempt && backoff < float64(cfg.MaxBackoff); i++ {
		backoff = math.Min(backoff*2, float64(cfg.MaxBackoff))
	}
	if cfg.JitterFraction > 0 {
		jitter = math.Max(0, math.Min(1, jitter))
		backoff *= 1 + ((2*jitter)-1)*cfg.JitterFraction
	}
	return time.Duration(math.Max(0, math.Min(backoff, float64(cfg.MaxBackoff))))
}

func managedDoltCircuitResume(state managedDoltCircuitState, report doltdisk.Report, sqlOK, integrityOK bool, now time.Time, cfg managedDoltCircuitConfig) (bool, managedDoltCircuitState) {
	if !state.Open {
		return true, state
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if report.State != doltdisk.StateHealthy || report.AvailableBytes < report.ResumeBytes {
		state.HeadroomSince = time.Time{}
		return false, state
	}
	if state.HeadroomSince.IsZero() {
		state.HeadroomSince = now
		return false, state
	}
	if !sqlOK || !integrityOK || cfg.ResumeStability <= 0 || now.Sub(state.HeadroomSince) < cfg.ResumeStability {
		return false, state
	}
	state.Open = false
	state.Reason = ""
	state.Attempts = 0
	state.LastTransition = now
	state.HeadroomSince = time.Time{}
	state.NextAttemptAt = time.Time{}
	return true, state
}

func managedDoltCircuitFields(state managedDoltCircuitState) []string {
	return []string{
		"circuit_open\t" + strconv.FormatBool(state.Open),
		"circuit_reason\t" + string(state.Reason),
		"circuit_attempts\t" + strconv.Itoa(state.Attempts),
		"circuit_opened_at\t" + formatManagedDoltCircuitTime(state.OpenedAt),
		"circuit_last_attempt_at\t" + formatManagedDoltCircuitTime(state.LastAttemptAt),
		"circuit_headroom_since\t" + formatManagedDoltCircuitTime(state.HeadroomSince),
		"circuit_next_attempt_at\t" + formatManagedDoltCircuitTime(state.NextAttemptAt),
	}
}

func formatManagedDoltCircuitTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func managedDoltCircuitDiskReport(dataDir string) (doltdisk.Report, error) {
	cfg := doltDiskConfig()
	cfg.ThresholdSource = "managed-recovery"
	budget := doltdisk.New(cfg, func(path string) (doltdisk.ProbeResult, error) {
		free, err := doltContainerFreeBytesFunc(path)
		if err != nil {
			return doltdisk.ProbeResult{}, err
		}
		return doltdisk.ProbeResult{AvailableBytes: free, Filesystem: path, SampledAt: time.Now().UTC()}, nil
	}, time.Now)
	report := budget.Sample(dataDir)
	if report.State == doltdisk.StateUnknown && report.ProbeError == "" {
		return report, errors.New("managed dolt disk probe returned unknown state")
	}
	return report, nil
}

func managedDoltCircuitRecordFailure(cityPath string, err error, logText string, diskState doltdisk.State, pid int, now time.Time) error {
	if err == nil {
		return nil
	}
	reason := classifyManagedDoltCircuitFailure(err, logText, diskState)
	state, readErr := readManagedDoltCircuitState(cityPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read managed dolt circuit state after failure: %w", readErr)
	}
	state = recordManagedDoltCircuitFailure(state, reason, now, defaultManagedDoltCircuitConfig())
	state.LastError = truncateManagedDoltCircuitError(err.Error())
	state.LastPID = pid
	return writeManagedDoltCircuitState(cityPath, state)
}

func (ops managedDoltRecoveryOps) nowOrTime() time.Time {
	if ops.now != nil {
		return ops.now()
	}
	return time.Now().UTC()
}

func managedDoltCircuitBeforeRecovery(cityPath, host, port, user string, ops managedDoltRecoveryOps) error {
	state, err := readManagedDoltCircuitState(cityPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read managed dolt circuit state: %w", err)
	}
	now := ops.nowOrTime()
	if !state.Open {
		if state.Attempts > 0 && state.Reason != "" && state.Reason != managedDoltCircuitCleanExit &&
			!state.NextAttemptAt.IsZero() && now.Before(state.NextAttemptAt) {
			return fmt.Errorf("managed Dolt recovery retry backoff active: reason=%s attempts=%d retry_at=%s", state.Reason, state.Attempts, formatManagedDoltCircuitTime(state.NextAttemptAt))
		}
		return nil
	}
	if ops.diskReport == nil {
		return fmt.Errorf("managed Dolt recovery circuit open: reason=%s attempts=%d", state.Reason, state.Attempts)
	}
	report, err := ops.diskReport(cityPath)
	if err != nil {
		return fmt.Errorf("managed Dolt recovery circuit open: reason=%s attempts=%d; disk check failed: %w", state.Reason, state.Attempts, err)
	}

	// SQL is a bounded, read-only liveness check. A dead process is expected
	// while recovering, so an absent managed/port-holder PID is an acceptable
	// SQL result; a live but unresponsive process is not.
	sqlOK := false
	if ops.queryProbe != nil {
		sqlOK = ops.queryProbe(host, port, user) == nil
	}
	if !sqlOK {
		if info, inspectErr := inspectManagedDoltProcess(cityPath, port); inspectErr == nil {
			sqlOK = info.ManagedPID <= 0 && info.PortHolderPID <= 0
		}
	}
	integrityOK := true
	if ops.integrityCheck != nil {
		integrityOK, err = ops.integrityCheck(cityPath)
		if err != nil {
			return fmt.Errorf("managed Dolt recovery circuit open: reason=%s attempts=%d; integrity check failed: %w", state.Reason, state.Attempts, err)
		}
	}
	allowed, next := managedDoltCircuitResume(state, report, sqlOK, integrityOK, now, defaultManagedDoltCircuitConfig())
	if err := writeManagedDoltCircuitState(cityPath, next); err != nil {
		return fmt.Errorf("persist managed Dolt recovery circuit: %w", err)
	}
	if !allowed {
		return fmt.Errorf("managed Dolt recovery circuit open: reason=%s attempts=%d headroom_since=%s", next.Reason, next.Attempts, formatManagedDoltCircuitTime(next.HeadroomSince))
	}
	return nil
}

func managedDoltCircuitMarkHealthy(cityPath string, pid int, now time.Time) error {
	state, err := readManagedDoltCircuitState(cityPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	state = recordManagedDoltCircuitFailure(state, managedDoltCircuitCleanExit, now, defaultManagedDoltCircuitConfig())
	state.LastPID = pid
	state.LastError = ""
	return writeManagedDoltCircuitState(cityPath, state)
}

func managedDoltCircuitIntegrityCheck(cityPath string) (bool, error) {
	layout, err := resolveManagedDoltRuntimeLayout(cityPath)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(layout.DataDir); err != nil {
		return false, err
	}
	info, err := inspectManagedDoltProcess(cityPath, "")
	if err != nil {
		return false, err
	}
	if info.RuntimeIdentityMismatch || info.ManagedDeletedInodes || info.PortHolderDeletedInodes {
		return false, nil
	}
	if info.PortHolderOwnership == managedDoltOwnershipForeign {
		return false, nil
	}
	return true, nil
}

func truncateManagedDoltCircuitError(value string) string {
	value = strings.TrimSpace(value)
	const maxErrorBytes = 512
	if len(value) <= maxErrorBytes {
		return value
	}
	return value[:maxErrorBytes] + "…"
}
