package main

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/config"
)

// healWatchdogMinInterval floors the configured heal interval so a mistyped
// tiny value cannot turn every controller tick into a heal pass.
const healWatchdogMinInterval = time.Minute

// shouldRunHealWatchdog reports whether a heal pass is due: the loop is
// enabled and the configured interval has elapsed since the last pass.
func shouldRunHealWatchdog(cfg *config.City, last, now time.Time) bool {
	if cfg == nil || !cfg.Heal.Enabled {
		return false
	}
	if len(cfg.Heal.Targets) == 0 && len(cfg.Heal.CriticalSessions) == 0 {
		return false
	}
	interval := cfg.Heal.IntervalOrDefault()
	if interval < healWatchdogMinInterval {
		interval = healWatchdogMinInterval
	}
	return now.Sub(last) >= interval
}

// runHealWatchdog runs the periodic self-healing pass from the controller
// tick. The pass itself runs on a goroutine (it shells to git and probes
// sessions, which must not stall the tick); an in-flight guard keeps at most
// one pass alive, and panics are contained to the pass.
func (cr *CityRuntime) runHealWatchdog(now time.Time) {
	if !shouldRunHealWatchdog(cr.cfg, cr.healWatchdogLast, now) {
		return
	}
	if !cr.healWatchdogInFlight.CompareAndSwap(false, true) {
		return
	}
	cr.healWatchdogLast = now
	cfg := cr.cfg
	cityPath := cr.cityPath
	cityStore := cr.cityBeadStore()
	rigStores := cr.rigBeadStores()
	sp := cr.sp
	rec := cr.rec
	stderr := cr.stderr
	logPrefix := cr.logPrefix
	if cityStore == nil {
		cr.healWatchdogInFlight.Store(false)
		return
	}
	go func() {
		defer cr.healWatchdogInFlight.Store(false)
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stderr, "%s: heal watchdog panic: %v\n", logPrefix, r) //nolint:errcheck
			}
		}()
		deps := buildHealDeps(cfg, cityPath, cityStore, rigStores, sp, rec, stderr, false)
		report := runHealPass(deps)
		for _, action := range report.Actions {
			if action.Capped {
				fmt.Fprintf(stderr, "%s: heal capped rung %d %s %s (%s)\n", logPrefix, action.Rung, action.Kind, action.Subject, action.Reason) //nolint:errcheck
				continue
			}
			fmt.Fprintf(stderr, "%s: heal rung %d %s %s: %s -> %s\n", logPrefix, action.Rung, action.Kind, action.Subject, action.Before, action.After) //nolint:errcheck
		}
		for _, msg := range report.Errors {
			fmt.Fprintf(stderr, "%s: heal error: %s\n", logPrefix, msg) //nolint:errcheck
		}
	}()
}
