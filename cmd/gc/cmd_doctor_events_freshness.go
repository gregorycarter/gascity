package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

// eventsFreshnessWindow is how long the city event log may stay silent while
// agent sessions are live AND beads are changing before the check fails. Bead
// writes flow through hooks that emit bead.* events, so bead activity inside
// the window guarantees the stream should have entries inside it too; the
// generous margin absorbs hook latency and clock skew without masking a dead
// stream for long.
const eventsFreshnessWindow = 15 * time.Minute

// eventsFreshnessCheck fails loudly when the city event stream goes silent
// while agents are demonstrably running (ga-78r). "Demonstrably running" needs
// both legs: live agent sessions (the WHO) and recent bead activity (the WHAT)
// — sessions alone can be legitimately quiet for hours, but beads changing
// without events landing means emission or persistence is dead. A telemetry
// stream that silently reads empty is worse than one that is absent, because
// it reads as "all clear".
type eventsFreshnessCheck struct {
	liveSessionCount   func() (int, error)
	latestBeadActivity func() (time.Time, error)
	now                func() time.Time
}

// newEventsFreshnessCheck wires the production probes: live sessions from the
// runtime provider over the configured agents, bead activity from the city
// bead store's open population (session-registry beads are updated every
// reconciler pass, so an active city always has fresh UpdatedAt stamps).
func newEventsFreshnessCheck(cfg *config.City, cityName string, sp runtime.Provider, newStore func(string) (beads.Store, error), cityPath string) *eventsFreshnessCheck {
	return &eventsFreshnessCheck{
		now: time.Now,
		liveSessionCount: func() (int, error) {
			live := 0
			for _, a := range cfg.Agents {
				if a.Suspended {
					continue
				}
				if sp.IsRunning(agent.SessionNameFor(cityName, a.QualifiedName(), cfg.Workspace.SessionTemplate)) {
					live++
				}
			}
			return live, nil
		},
		latestBeadActivity: func() (time.Time, error) {
			store, err := newStore(cityPath)
			if err != nil {
				return time.Time{}, err
			}
			open, err := store.ListOpen("open")
			if err != nil {
				return time.Time{}, err
			}
			var latest time.Time
			for _, b := range open {
				if b.UpdatedAt.After(latest) {
					latest = b.UpdatedAt
				}
			}
			return latest, nil
		},
	}
}

// Name implements doctor.Check.
func (c *eventsFreshnessCheck) Name() string { return "events-freshness" }

// CanFix implements doctor.Check; the failure needs investigation, not a
// mechanical repair.
func (c *eventsFreshnessCheck) CanFix() bool { return false }

// Fix implements doctor.Check.
func (c *eventsFreshnessCheck) Fix(_ *doctor.CheckContext) error { return nil }

// WarmupEligible implements doctor.Check.
func (c *eventsFreshnessCheck) WarmupEligible() bool { return false }

// Run implements doctor.Check.
func (c *eventsFreshnessCheck) Run(ctx *doctor.CheckContext) *doctor.CheckResult {
	r := &doctor.CheckResult{Name: c.Name()}
	now := c.now()

	live, err := c.liveSessionCount()
	if err != nil {
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("event freshness unknown: probing live sessions: %v", err)
		return r
	}
	if live == 0 {
		r.Status = doctor.StatusOK
		r.Message = "no live agent sessions; event freshness not applicable"
		return r
	}

	activity, err := c.latestBeadActivity()
	if err != nil {
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("event freshness unknown: probing bead activity: %v", err)
		return r
	}
	if activity.IsZero() || now.Sub(activity) > eventsFreshnessWindow {
		r.Status = doctor.StatusOK
		r.Message = fmt.Sprintf("%d live session(s), no bead activity in %s; a quiet stream is legitimate", live, eventsFreshnessWindow)
		return r
	}

	eventsPath := filepath.Join(ctx.CityPath, ".gc", "events.jsonl")
	tail, err := events.ReadFilteredTail(eventsPath, events.Filter{}, 1)
	if err != nil {
		r.Status = doctor.StatusError
		r.Message = fmt.Sprintf("event stream unreadable while %d agent session(s) run: %v", live, err)
		r.FixHint = "telemetry is silent while the city works; check events.jsonl permissions and the controller's event recorder"
		return r
	}
	if len(tail) == 0 {
		r.Status = doctor.StatusError
		r.Message = fmt.Sprintf("event stream is empty while %d agent session(s) run and beads changed %s ago — event emission is dead", live, humanizedAge(now.Sub(activity)))
		r.FixHint = "every bead write should emit an event; check the controller's event recorder wiring and events.jsonl writability"
		return r
	}
	age := now.Sub(tail[0].Ts)
	if age > eventsFreshnessWindow {
		r.Status = doctor.StatusError
		r.Message = fmt.Sprintf(
			"event stream silent for %s while %d agent session(s) run and beads changed %s ago — event emission or persistence is dead",
			humanizedAge(age), live, humanizedAge(now.Sub(activity)))
		r.FixHint = "a silently-empty telemetry stream reads as 'all clear'; check the controller's event recorder and events.jsonl (ga-78r)"
		return r
	}

	r.Status = doctor.StatusOK
	r.Message = fmt.Sprintf("event stream fresh (newest event %s old, %d live session(s))", humanizedAge(age), live)
	return r
}

// humanizedAge renders a duration for check messages without sub-second noise.
func humanizedAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}
