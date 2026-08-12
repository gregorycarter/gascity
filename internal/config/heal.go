package config

import (
	"fmt"
	"strings"
	"time"
)

// HealConfig declares the self-healing throughput loop ([heal] in city.toml).
//
// The loop is a deterministic, supervisor-level watchdog: it measures landed
// commits on each target rig's mainline (read from git directly — no failure
// mode can fake a landed commit), detects a stall (nothing landing while aged
// demand exists), and walks a fixed remediation ladder — release orphaned
// routed work, force-assign inverted priorities, recover work held by dead or
// stuck sessions, file mainline-red repair work, restart critical sessions.
// Every action is recorded on the event bus as the audit trail; the loop never
// notifies a human as a recovery mechanism.
//
// The loop is opt-in (enabled=false by default) and contains zero role
// knowledge: every address it acts on — queue assignees it must never touch,
// critical sessions it may restart, routes it files repair work to — comes
// from this section. The SDK supplies only the mechanics.
type HealConfig struct {
	// Enabled toggles the heal loop. Defaults to false (opt-in). The
	// orchestrator runs the loop as a periodic watchdog when enabled; `gc heal`
	// runs one pass regardless (so an external scheduler can drive it even
	// when the orchestrator is the thing that is broken).
	Enabled bool `toml:"enabled,omitempty"`
	// Interval is the cadence between orchestrator-driven heal passes as a
	// duration string. Defaults to 5m.
	Interval string `toml:"interval,omitempty" jsonschema:"default=5m"`
	// StallAfter is the throughput stall window as a duration string. A rig
	// is stalled when no commit landed on its mainline within this window
	// while actionable work older than the window exists. Defaults to 30m.
	StallAfter string `toml:"stall_after,omitempty" jsonschema:"default=30m"`
	// OrphanStaleAfter is the minimum age (since last update) before an
	// assigned-but-unclaimed routed bead is treated as orphaned and returned
	// to the pool, and before work held by a dead session is released.
	// Defaults to 20m.
	OrphanStaleAfter string `toml:"orphan_stale_after,omitempty" jsonschema:"default=20m"`
	// InversionAfter is the minimum unclaimed age before a ready bead at or
	// above InversionPriority counts as priority-inverted. Defaults to 15m.
	InversionAfter string `toml:"inversion_after,omitempty" jsonschema:"default=15m"`
	// InversionPriority is the highest (numerically largest) priority class
	// protected against inversion. Defaults to 0: only P0 beads are
	// force-assigned past pool ordering. Pointer so an unset section marshals
	// away: `omitempty` does not suppress a zero-valued int, and 0 is also a
	// meaningful explicit setting here.
	InversionPriority *int `toml:"inversion_priority,omitempty" jsonschema:"default=0"`
	// StuckAfter is the minimum age (since last bead update) before a live
	// session holding in_progress work is treated as stuck. Defaults to 2h.
	StuckAfter string `toml:"stuck_after,omitempty" jsonschema:"default=2h"`
	// ActionCooldown is the per-subject re-action floor. A bead or session
	// the loop already acted on within this window is skipped and the skip is
	// recorded loudly (heal.capped event) instead of thrashing. Defaults to 1h.
	ActionCooldown string `toml:"action_cooldown,omitempty" jsonschema:"default=1h"`
	// MaxActionsPerPass caps mutating actions in a single pass, bounding the
	// blast radius of any misdetection. Defaults to 5. Pointer for the same
	// marshal reason as InversionPriority.
	MaxActionsPerPass *int `toml:"max_actions_per_pass,omitempty" jsonschema:"default=5"`
	// CriticalSessions lists session targets (agent/session names) the loop
	// must keep running — coordinator-style singletons whose downtime is rig
	// downtime. A session in this list that is not running is started,
	// subject to the action cooldown. Empty disables the rung.
	CriticalSessions []string `toml:"critical_sessions,omitempty"`
	// Targets declares the rigs the loop watches ([[heal.target]]).
	Targets []HealTarget `toml:"target,omitempty"`
}

// HealTarget declares one rig watched by the heal loop.
type HealTarget struct {
	// Rig is the rig name (must match a [[rigs]] entry).
	Rig string `toml:"rig" jsonschema:"required"`
	// QueueAddresses lists assignees whose assigned-open beads are real scan
	// queues (merge queues and similar): rung 1 never releases their beads
	// back to the pool, and rung 3 never treats their holders as stuck.
	QueueAddresses []string `toml:"queue_addresses,omitempty"`
	// MainRedCheck is a shell command run in the rig repository that exits 0
	// when the mainline is green and non-zero when it is red (e.g. a `gh`
	// query against the latest mainline check run). Empty disables the
	// mainline-red rung for this target.
	MainRedCheck string `toml:"main_red_check,omitempty"`
	// MainRedRoute is the routed-work target (gc.routed_to value) for the
	// repair bead the loop files when MainRedCheck reports red. Required for
	// the repair bead to be claimable; empty disables filing.
	MainRedRoute string `toml:"main_red_route,omitempty"`
	// MainRedWorkflow is the formula stamped on the filed repair bead. Empty
	// files a plain routed task.
	MainRedWorkflow string `toml:"main_red_workflow,omitempty"`
}

// Default heal thresholds. Exposed as accessor fallbacks only; read the
// values through the *OrDefault methods.
const (
	defaultHealInterval          = 5 * time.Minute
	defaultHealStallAfter        = 30 * time.Minute
	defaultHealOrphanStaleAfter  = 20 * time.Minute
	defaultHealInversionAfter    = 15 * time.Minute
	defaultHealStuckAfter        = 2 * time.Hour
	defaultHealActionCooldown    = time.Hour
	defaultHealMaxActionsPerPass = 5
	defaultHealInversionPriority = 0
)

// IntervalOrDefault returns the parsed Interval, defaulting to 5m.
func (h HealConfig) IntervalOrDefault() time.Duration {
	return durationOr(h.Interval, defaultHealInterval)
}

// StallAfterOrDefault returns the parsed StallAfter, defaulting to 30m.
func (h HealConfig) StallAfterOrDefault() time.Duration {
	return durationOr(h.StallAfter, defaultHealStallAfter)
}

// OrphanStaleAfterOrDefault returns the parsed OrphanStaleAfter, defaulting
// to 20m.
func (h HealConfig) OrphanStaleAfterOrDefault() time.Duration {
	return durationOr(h.OrphanStaleAfter, defaultHealOrphanStaleAfter)
}

// InversionAfterOrDefault returns the parsed InversionAfter, defaulting to 15m.
func (h HealConfig) InversionAfterOrDefault() time.Duration {
	return durationOr(h.InversionAfter, defaultHealInversionAfter)
}

// StuckAfterOrDefault returns the parsed StuckAfter, defaulting to 2h.
func (h HealConfig) StuckAfterOrDefault() time.Duration {
	return durationOr(h.StuckAfter, defaultHealStuckAfter)
}

// ActionCooldownOrDefault returns the parsed ActionCooldown, defaulting to 1h.
func (h HealConfig) ActionCooldownOrDefault() time.Duration {
	return durationOr(h.ActionCooldown, defaultHealActionCooldown)
}

// MaxActionsPerPassOrDefault returns MaxActionsPerPass, defaulting to 5.
func (h HealConfig) MaxActionsPerPassOrDefault() int {
	if h.MaxActionsPerPass == nil || *h.MaxActionsPerPass <= 0 {
		return defaultHealMaxActionsPerPass
	}
	return *h.MaxActionsPerPass
}

// InversionPriorityOrDefault returns InversionPriority, defaulting to 0 —
// only P0 beads are force-assigned past pool ordering.
func (h HealConfig) InversionPriorityOrDefault() int {
	if h.InversionPriority == nil {
		return defaultHealInversionPriority
	}
	return *h.InversionPriority
}

// ValidateHealConfig checks [heal] declarations against the rest of the city
// config. Enabled-ness is not required for validation: a mistyped disabled
// section should still fail loudly rather than surprise on enable.
func ValidateHealConfig(cfg *City) error {
	if cfg == nil {
		return nil
	}
	h := cfg.Heal
	checkPositiveDuration := func(field, value string) error {
		if value == "" {
			return nil
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			// Parse errors remain a warning from ValidateDurations, matching
			// the rest of the city duration surface.
			return nil
		}
		if duration <= 0 {
			return fmt.Errorf("[heal]: %s must be a positive duration, got %q", field, value)
		}
		return nil
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"interval", h.Interval},
		{"stall_after", h.StallAfter},
		{"orphan_stale_after", h.OrphanStaleAfter},
		{"inversion_after", h.InversionAfter},
		{"stuck_after", h.StuckAfter},
		{"action_cooldown", h.ActionCooldown},
	} {
		if err := checkPositiveDuration(field.name, field.value); err != nil {
			return err
		}
	}
	if h.InversionPriority != nil && *h.InversionPriority < 0 {
		return fmt.Errorf("[heal]: inversion_priority must be >= 0, got %d", *h.InversionPriority)
	}
	if h.MaxActionsPerPass != nil && *h.MaxActionsPerPass <= 0 {
		return fmt.Errorf("[heal]: max_actions_per_pass must be > 0, got %d", *h.MaxActionsPerPass)
	}
	for i, name := range h.CriticalSessions {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("[heal]: critical_sessions[%d] is empty", i)
		}
	}
	rigs := make(map[string]bool, len(cfg.Rigs))
	for _, rig := range cfg.Rigs {
		rigs[rig.Name] = true
	}
	seen := make(map[string]int, len(h.Targets))
	for i, target := range h.Targets {
		ctx := fmt.Sprintf("heal.target[%d]", i)
		rig := strings.TrimSpace(target.Rig)
		if rig == "" {
			return fmt.Errorf("%s: rig is required", ctx)
		}
		if !rigs[rig] {
			return fmt.Errorf("%s: rig %q is not declared", ctx, rig)
		}
		if prev, ok := seen[rig]; ok {
			return fmt.Errorf("%s: rig %q already targeted by heal.target[%d]", ctx, rig, prev)
		}
		seen[rig] = i
		for j, addr := range target.QueueAddresses {
			if strings.TrimSpace(addr) == "" {
				return fmt.Errorf("%s: queue_addresses[%d] is empty", ctx, j)
			}
		}
		if strings.TrimSpace(target.MainRedRoute) == "" && strings.TrimSpace(target.MainRedWorkflow) != "" {
			return fmt.Errorf("%s: main_red_workflow requires main_red_route", ctx)
		}
	}
	return nil
}
