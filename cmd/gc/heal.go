package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

// The heal loop: a deterministic, supervisor-level self-healing pass.
//
// DETECT: landed commits on each target rig's mainline are read from git
// directly — the one signal no orchestration failure mode can fake. A rig is
// stalled when nothing landed within the stall window while actionable demand
// at least as old as the window was waiting. "Mainline is red" is a DISTINCT
// signal probed independently (a red mainline must never mask a freeze).
//
// DIAGNOSE + REMEDIATE: a fixed ladder, each rung independently safe to run
// unattended, walked only while the rig is stalled (rungs 1-3) or on its own
// independent signal (rungs 4-5):
//
//  1. Orphaned routed work — open beads carrying a pool route but parked on
//     an assignee, aged past the orphan threshold. Pool claim probes require
//     an empty assignee, so these are invisible to every consumer; release
//     them back to the pool. Configured queue addresses (merge queues) are
//     never touched.
//  2. Priority inversion — a ready P0 (configurable class) unclaimed past the
//     inversion threshold while an idle pool worker exists. Pool ordering is
//     oldest-first, so a fresh P0 waits behind stale P1/P2; force-assign it
//     to an idle worker and nudge.
//  3. Dead/stuck sessions — in_progress work whose assignee has no live
//     session is released back to the pool; a live-but-stuck holder (no bead
//     update past the stuck threshold) is restarted when it is a pool worker
//     and not human-attached, and its work released. Unassigned in_progress
//     routed work (unclaimable limbo) is reopened.
//  4. Mainline red — the configured check command reports red: file one
//     routed P0 repair bead (deduped on heal.reason=main-red while open) and
//     optionally attach the configured repair workflow.
//  5. Critical session down — a configured must-run session (coordinator
//     class) is not running: start it.
//
// VERIFY: the next pass re-measures landed commits; remediation stops the
// moment merges resume because the stall gate no longer opens. Per-subject
// action cooldowns and a per-pass action budget prevent thrash: a rung that
// keeps firing without merges resuming stops mutating and records loudly
// (heal.capped) instead.
//
// The loop never notifies a human as a recovery mechanism — the event bus
// records (heal.stall_detected / heal.action / heal.capped) ARE the audit
// trail. It holds zero role knowledge: every address it protects, restarts,
// or routes to comes from [heal] config. It runs without any agent: the
// controller drives it as a watchdog, and `gc heal` runs one pass standalone
// so an external scheduler covers the case where the controller itself is
// what is broken.

// healActorName identifies heal-loop writes in events and bead audit fields.
const healActorName = "gc-heal"

// healMainRedReason is the dedupe marker stamped on auto-filed mainline-red
// repair beads (metadata key beadmeta.HealReasonMetadataKey).
const healMainRedReason = "main-red"

// healCheckTimeout bounds the configured main_red_check command.
const healCheckTimeout = 60 * time.Second

// healDeps carries every effect the heal pass performs, so the pass itself
// stays deterministic and fully fakeable in tests. Production wiring lives in
// cmd_heal.go (standalone) and heal_watchdog.go (controller).
type healDeps struct {
	Cfg      *config.City
	CityPath string
	Now      func() time.Time

	CityStore beads.Store
	RigStores map[string]beads.Store

	// ListOpenSessions returns the open (non-closed) session bead records.
	ListOpenSessions func() ([]session.Info, error)
	// Observe probes the live runtime state of one session target.
	Observe func(target string) (worker.LiveObservation, error)
	// Kill terminates a session's runtime (rung 3 stuck restart, and rung 5
	// clearing a dead runtime before a critical-session restart).
	Kill func(target string) error
	// Start ensures a configured session target is running (rung 5).
	Start func(target string) error
	// Nudge delivers a best-effort wake message to a session.
	Nudge func(target, message string) error

	// LandedCommits counts commits landed on the rig mainline since a time.
	LandedCommits func(repoPath, branch string, since time.Time) (int, error)
	// RunCheck executes a configured shell check in dir; nil means green.
	RunCheck func(dir, command string) error
	// AttachWorkflow instantiates a formula on a filed repair bead.
	AttachWorkflow func(store beads.Store, rigName, workflow string, bead beads.Bead) error

	// Rec receives the audit events. Never nil in production; dry-run passes
	// do not record.
	Rec events.Recorder
	// RecentActions returns subject -> last heal.action time since a cutoff,
	// implementing the per-subject cooldown from the durable event log.
	RecentActions func(since time.Time) map[string]time.Time

	Stderr io.Writer
	DryRun bool
}

// healAction records one remediation (or suppressed remediation) for the
// pass report.
type healAction struct {
	Rung    int    `json:"rung"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Rig     string `json:"rig,omitempty"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
	Capped  bool   `json:"capped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// healRigReport is the per-rig detection summary for one pass.
type healRigReport struct {
	Rig                string        `json:"rig"`
	Branch             string        `json:"branch"`
	LandedCommits      int           `json:"landed_commits"`
	OldestDemandAge    time.Duration `json:"-"`
	OldestDemandAgeSec int64         `json:"oldest_demand_age_s"`
	Stalled            bool          `json:"stalled"`
	MainRed            bool          `json:"main_red"`
}

// healReport is the full result of one heal pass.
type healReport struct {
	Rigs    []healRigReport `json:"rigs"`
	Actions []healAction    `json:"actions"`
	Errors  []string        `json:"errors,omitempty"`
	DryRun  bool            `json:"dry_run,omitempty"`
}

// healPass carries the per-pass working state (budget, cooldowns, report).
type healPass struct {
	deps     healDeps
	now      time.Time
	cooldown time.Duration
	recent   map[string]time.Time
	budget   int
	acted    int
	seen     map[string]bool
	// cooldownReadable is false when the durable event ledger cannot supply
	// prior actions. Mutating without that state would bypass the anti-thrash
	// cooldown, so non-dry-run remediation fails closed in that state.
	cooldownReadable bool
	report           healReport
}

// runHealPass executes one deterministic heal pass and returns its report.
// Read failures are recorded and fail safe: a rung whose evidence cannot be
// read performs no action.
func runHealPass(deps healDeps) healReport {
	pass := &healPass{
		deps:     deps,
		now:      deps.Now(),
		cooldown: deps.Cfg.Heal.ActionCooldownOrDefault(),
		budget:   deps.Cfg.Heal.MaxActionsPerPassOrDefault(),
		seen:     map[string]bool{},
	}
	pass.report.DryRun = deps.DryRun
	pass.cooldownReadable = deps.DryRun
	if deps.RecentActions != nil {
		pass.recent = deps.RecentActions(pass.now.Add(-pass.cooldown))
		if pass.recent != nil {
			pass.cooldownReadable = true
		} else if !deps.DryRun {
			pass.cooldownReadable = false
		}
	}
	if !pass.cooldownReadable {
		pass.errf("heal: audit ledger unavailable; refusing remediation without cooldown state")
	}

	sessions, sessionsReadable := pass.openSessions()
	identityIndex := healSessionIdentityIndex(sessions)
	busy, busyReadable := pass.busyIdentities()

	for _, target := range deps.Cfg.Heal.Targets {
		rig := findRigByName(target.Rig, deps.Cfg.Rigs)
		if rig == nil {
			pass.errf("heal: target rig %q not found in config", target.Rig)
			continue
		}
		pass.healTargetRig(target, *rig, sessions, sessionsReadable, identityIndex, busy, busyReadable)
	}

	pass.healCriticalSessions()
	return pass.report
}

// healTargetRig runs detection and the stall-gated rungs for one rig.
func (p *healPass) healTargetRig(target config.HealTarget, rig config.Rig, sessions []session.Info, sessionsReadable bool, identityIndex map[string]session.Info, busy map[string]bool, busyReadable bool) {
	store := p.rigStore(target.Rig)
	if store == nil {
		p.errf("heal: no bead store for rig %q", target.Rig)
		return
	}
	branch := strings.TrimSpace(rig.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	window := p.deps.Cfg.Heal.StallAfterOrDefault()
	repo := healRigRepoPath(p.deps.CityPath, rig)

	rigReport := healRigReport{Rig: target.Rig, Branch: branch}
	landed, landedErr := p.deps.LandedCommits(repo, branch, p.now.Add(-window))
	if landedErr != nil {
		p.errf("heal: measuring landed commits for rig %q: %v", target.Rig, landedErr)
	}
	rigReport.LandedCommits = landed

	demandAge := p.oldestDemandAge(store)
	rigReport.OldestDemandAge = demandAge
	rigReport.OldestDemandAgeSec = int64(demandAge / time.Second)

	// Measurement failure fails safe: an unreadable mainline must not open
	// the remediation gate.
	rigReport.Stalled = landedErr == nil && landed == 0 && demandAge >= window

	// Rung 4 runs on its own signal: red-mainline and nothing-landing are
	// distinct detections, and a red mainline must never mask a freeze.
	if strings.TrimSpace(target.MainRedCheck) != "" {
		red := p.deps.RunCheck(repo, target.MainRedCheck) != nil
		rigReport.MainRed = red
		if red {
			p.healMainRed(target, store, branch)
		}
	}

	if rigReport.Stalled {
		p.emitStall(rigReport, window)
		if sessionsReadable {
			p.healOrphanedRoutedWork(target, store)
			if busyReadable {
				p.healPriorityInversion(target, store, sessions, busy)
			}
			p.healDeadOrStuckSessions(target, store, identityIndex)
		}
	}
	p.report.Rigs = append(p.report.Rigs, rigReport)
}

// --- Rung 1: orphaned routed work ---------------------------------------

// healOrphanedRoutedWork releases open, routed, assigned beads no consumer
// can ever claim (claim probes require an empty assignee) back to the pool.
// Configured queue addresses are real scan queues and are never released.
func (p *healPass) healOrphanedRoutedWork(target config.HealTarget, store beads.Store) {
	staleAfter := p.deps.Cfg.Heal.OrphanStaleAfterOrDefault()
	open, err := store.List(beads.ListQuery{Status: "open"})
	if err != nil {
		p.errf("heal: rung 1 list open beads for rig %q: %v", target.Rig, err)
		return
	}
	beads.SortBeads(open, beads.SortCreatedAsc)
	for _, wb := range open {
		assignee := strings.TrimSpace(wb.Assignee)
		if assignee == "" || wb.Ephemeral {
			continue
		}
		if routedToOrLegacyWorkflowTarget(wb) == "" {
			continue
		}
		if healIsQueueAddress(target, assignee) || healBeadHeld(wb) {
			continue
		}
		if p.now.Sub(healBeadFreshness(wb)) < staleAfter {
			continue
		}
		subject := "bead:" + wb.ID
		if !p.allow(1, "orphan-release", subject, target.Rig) {
			continue
		}
		action := healAction{
			Rung:    1,
			Kind:    "orphan-release",
			Subject: subject,
			Rig:     target.Rig,
			Before:  fmt.Sprintf("status=%s assignee=%s", wb.Status, assignee),
			After:   "status=open assignee=",
		}
		if p.mutate(func() error {
			return workAssignmentForStore(beads.WorkStore{Store: store}).ReleaseWorkBead(wb, "")
		}, &action) {
			p.record(action)
		}
	}
}

// --- Rung 2: priority inversion ------------------------------------------

// healPriorityInversion force-assigns aged, unclaimed beads at or above the
// protected priority class to an idle pool worker on their route. Pool claim
// ordering is oldest-first, so without this a fresh P0 waits behind every
// stale lower-priority bead.
func (p *healPass) healPriorityInversion(target config.HealTarget, store beads.Store, sessions []session.Info, busy map[string]bool) {
	inversionAfter := p.deps.Cfg.Heal.InversionAfterOrDefault()
	maxPriority := p.deps.Cfg.Heal.InversionPriorityOrDefault()
	ready, err := store.Ready(beads.ReadyQuery{})
	if err != nil {
		p.errf("heal: rung 2 ready query for rig %q: %v", target.Rig, err)
		return
	}
	beads.SortBeads(ready, beads.SortCreatedAsc)
	for _, wb := range ready {
		if strings.TrimSpace(wb.Assignee) != "" || wb.Ephemeral || wb.Priority == nil || *wb.Priority > maxPriority {
			continue
		}
		route := routedToOrLegacyWorkflowTarget(wb)
		if route == "" || healBeadHeld(wb) {
			continue
		}
		if p.now.Sub(healBeadFreshness(wb)) < inversionAfter {
			continue
		}
		subject := "bead:" + wb.ID
		idle := p.idleWorkerForRoute(route, sessions, busy)
		if idle == nil {
			// Remediation needs an idle worker; preempting in-flight work is
			// never safe. Record the suppression loudly.
			p.capped(2, "force-assign", subject, target.Rig, "no-idle-worker")
			continue
		}
		if !p.allow(2, "force-assign", subject, target.Rig) {
			continue
		}
		action := healAction{
			Rung:    2,
			Kind:    "force-assign",
			Subject: subject,
			Rig:     target.Rig,
			Before:  fmt.Sprintf("priority=%d assignee= route=%s", *wb.Priority, route),
			After:   "assignee=" + idle.ID,
		}
		if p.mutate(func() error {
			return workAssignmentForStore(beads.WorkStore{Store: store}).ReassignWorkBead(wb, idle.ID)
		}, &action) {
			p.record(action)
			busy[idle.ID] = true
			if p.deps.Nudge != nil && !p.deps.DryRun {
				nudgeTarget := healSessionTarget(*idle)
				if err := p.deps.Nudge(nudgeTarget, fmt.Sprintf("Priority work %s was assigned to you; run `gc hook --claim` to pick it up.", wb.ID)); err != nil {
					p.errf("heal: nudging %s after force-assign of %s: %v", nudgeTarget, wb.ID, err)
				}
			}
		}
	}
}

// idleWorkerForRoute returns the first live, unattached, pool-managed open
// session on the route's template with no in_progress work, or nil.
func (p *healPass) idleWorkerForRoute(route string, sessions []session.Info, busy map[string]bool) *session.Info {
	agentCfg := findAgentByTemplate(p.deps.Cfg, route)
	if agentCfg == nil || !agentCfg.SupportsGenericEphemeralSessions() {
		return nil
	}
	for i := range sessions {
		info := sessions[i]
		if info.Closed || !info.PoolManaged || info.Template != route {
			continue
		}
		if busy[info.ID] || healAnyIdentityBusy(info, busy) {
			continue
		}
		obs, err := p.observe(healSessionTarget(info))
		if err != nil || !obs.Alive || obs.Attached {
			continue
		}
		return &info
	}
	return nil
}

// --- Rung 3: dead or stuck sessions --------------------------------------

// healDeadOrStuckSessions recovers in_progress work held by sessions that no
// longer exist, are dead, or have made no observable progress past the stuck
// threshold. Queue addresses are excluded entirely: their holders may be
// mid-merge and must never be interrupted.
func (p *healPass) healDeadOrStuckSessions(target config.HealTarget, store beads.Store, identityIndex map[string]session.Info) {
	staleAfter := p.deps.Cfg.Heal.OrphanStaleAfterOrDefault()
	stuckAfter := p.deps.Cfg.Heal.StuckAfterOrDefault()
	inProgress, err := store.List(beads.ListQuery{Status: "in_progress"})
	if err != nil {
		p.errf("heal: rung 3 list in_progress beads for rig %q: %v", target.Rig, err)
		return
	}
	beads.SortBeads(inProgress, beads.SortCreatedAsc)
	for _, wb := range inProgress {
		if wb.Ephemeral || healBeadHeld(wb) {
			continue
		}
		assignee := strings.TrimSpace(wb.Assignee)
		if healIsQueueAddress(target, assignee) {
			continue
		}
		age := p.now.Sub(healBeadFreshness(wb))
		if age < staleAfter {
			continue
		}
		subject := "bead:" + wb.ID

		if assignee == "" {
			// Unclaimable limbo: in_progress with no owner is invisible to
			// every claim probe until reopened.
			if routedToOrLegacyWorkflowTarget(wb) == "" {
				continue
			}
			if !p.allow(3, "limbo-reopen", subject, target.Rig) {
				continue
			}
			action := healAction{
				Rung: 3, Kind: "limbo-reopen", Subject: subject, Rig: target.Rig,
				Before: "status=in_progress assignee=",
				After:  "status=open assignee=",
			}
			if p.mutate(func() error {
				return workAssignmentForStore(beads.WorkStore{Store: store}).ReleaseWorkBead(wb, "")
			}, &action) {
				p.record(action)
			}
			continue
		}

		info, known := identityIndex[assignee]
		alive := false
		attached := false
		if known {
			obs, err := p.observe(healSessionTarget(info))
			if err != nil {
				// Probe failure is not evidence of death; do nothing.
				p.errf("heal: rung 3 observing session %q for bead %s: %v", healSessionTarget(info), wb.ID, err)
				continue
			}
			alive = obs.Alive
			attached = obs.Attached
		}

		if !known || !alive {
			if !p.allow(3, "dead-session-release", subject, target.Rig) {
				continue
			}
			action := healAction{
				Rung: 3, Kind: "dead-session-release", Subject: subject, Rig: target.Rig,
				Before: "status=in_progress assignee=" + assignee,
				After:  "status=open assignee=",
			}
			if p.mutate(func() error {
				return workAssignmentForStore(beads.WorkStore{Store: store}).ReleaseWorkBead(wb, "")
			}, &action) {
				p.record(action)
			}
			continue
		}

		if age < stuckAfter {
			continue
		}
		if !info.PoolManaged || attached {
			// Not safe to restart a named or human-attached session; record
			// the stuck holder loudly and leave it alone.
			if p.allow(3, "stuck-recorded", subject, target.Rig) {
				action := healAction{
					Rung: 3, Kind: "stuck-recorded", Subject: subject, Rig: target.Rig,
					Before: fmt.Sprintf("status=in_progress assignee=%s age=%s", assignee, age.Truncate(time.Second)),
					After:  "unchanged (holder not restartable)",
				}
				p.record(action)
			}
			continue
		}
		if !p.allow(3, "stuck-restart", subject, target.Rig) {
			continue
		}
		action := healAction{
			Rung: 3, Kind: "stuck-restart", Subject: subject, Rig: target.Rig,
			Before: fmt.Sprintf("status=in_progress assignee=%s age=%s", assignee, age.Truncate(time.Second)),
			After:  "session killed; status=open assignee=",
		}
		if p.mutate(func() error {
			if err := p.deps.Kill(healSessionTarget(info)); err != nil {
				return fmt.Errorf("killing stuck session %q: %w", healSessionTarget(info), err)
			}
			return workAssignmentForStore(beads.WorkStore{Store: store}).ReleaseWorkBead(wb, "")
		}, &action) {
			p.record(action)
		}
	}
}

// --- Rung 4: mainline red -------------------------------------------------

// healMainRed files one routed repair bead for a red mainline, deduped on an
// open heal-filed bead, and attaches the configured repair workflow.
func (p *healPass) healMainRed(target config.HealTarget, store beads.Store, branch string) {
	route := strings.TrimSpace(target.MainRedRoute)
	if route == "" {
		return
	}
	existing, err := store.List(beads.ListQuery{Metadata: map[string]string{beadmeta.HealReasonMetadataKey: healMainRedReason}, IncludeClosed: false})
	if err != nil {
		p.errf("heal: rung 4 dedupe query for rig %q: %v", target.Rig, err)
		return
	}
	for _, b := range existing {
		if b.Status != "closed" {
			return // an open repair bead already tracks this
		}
	}
	subject := "mainred:" + target.Rig
	if !p.allow(4, "file-repair-bead", subject, target.Rig) {
		return
	}
	priority := 0
	action := healAction{
		Rung: 4, Kind: "file-repair-bead", Subject: subject, Rig: target.Rig,
		Before: fmt.Sprintf("mainline %s red, no open repair bead", branch),
	}
	if p.mutate(func() error {
		created, err := store.Create(beads.Bead{
			Title:    fmt.Sprintf("Mainline red: fix %s on rig %s", branch, target.Rig),
			Type:     "task",
			Priority: &priority,
			Description: fmt.Sprintf(
				"Auto-filed by the heal loop: the configured mainline check reported %s red on rig %s.\n\n"+
					"A red mainline freezes production delivery (CD skips every deploy). Diagnose the failing\n"+
					"check on %s, fix it on a branch, and submit through the normal merge flow.",
				branch, target.Rig, branch),
			Metadata: map[string]string{
				beadmeta.RoutedToMetadataKey:   route,
				beadmeta.HealReasonMetadataKey: healMainRedReason,
				"source":                       healActorName,
			},
		})
		if err != nil {
			return err
		}
		action.After = "filed " + created.ID + " routed to " + route
		if workflow := strings.TrimSpace(target.MainRedWorkflow); workflow != "" && p.deps.AttachWorkflow != nil {
			if err := p.deps.AttachWorkflow(store, target.Rig, workflow, created); err != nil {
				// Non-fatal: the bead is created and routed either way.
				p.errf("heal: attaching workflow %q to %s: %v", workflow, created.ID, err)
			}
		}
		return nil
	}, &action) {
		if action.After == "" {
			action.After = "repair bead filed, routed to " + route
		}
		p.record(action)
	}
}

// --- Rung 5: critical sessions --------------------------------------------

// healCriticalSessions starts configured must-run sessions that are not
// running. The coordinator class is a single point of failure whose downtime
// is rig downtime, so this rung runs on its own signal, stalled or not.
func (p *healPass) healCriticalSessions() {
	for _, name := range p.deps.Cfg.Heal.CriticalSessions {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		obs, err := p.observe(name)
		// Down means not (Running && Alive): a crashed agent process inside a
		// surviving runtime session reports Running=true, Alive=false, and is
		// exactly the coordinator-down shape this rung exists for.
		if err == nil && obs.Running && obs.Alive {
			continue
		}
		subject := "session:" + name
		if !p.allow(5, "critical-restart", subject, "") {
			continue
		}
		before := "not running"
		deadRuntime := err == nil && obs.Running && !obs.Alive
		if err != nil {
			before = "unobservable: " + err.Error()
		} else if deadRuntime {
			before = "runtime present but agent process dead"
		}
		action := healAction{
			Rung: 5, Kind: "critical-restart", Subject: subject,
			Before: before, After: "start requested",
		}
		if p.mutate(func() error {
			if deadRuntime {
				// The dead runtime holds the session identity; clear it so
				// the start path can recreate the session.
				if killErr := p.deps.Kill(name); killErr != nil {
					return fmt.Errorf("killing dead runtime for %q before restart: %w", name, killErr)
				}
			}
			return p.deps.Start(name)
		}, &action) {
			p.record(action)
		}
	}
}

// --- shared pass mechanics ------------------------------------------------

// allow applies the per-subject cooldown, the once-per-pass set, and the
// per-pass action budget. A denial for cooldown or budget is recorded loudly
// as heal.capped.
func (p *healPass) allow(rung int, kind, subject, rig string) bool {
	if p.seen[subject] {
		return false
	}
	p.seen[subject] = true
	if !p.cooldownReadable {
		p.capped(rung, kind, subject, rig, "audit-unavailable")
		return false
	}
	if last, ok := p.recent[subject]; ok && p.now.Sub(last) < p.cooldown {
		p.capped(rung, kind, subject, rig, "cooldown")
		return false
	}
	if p.acted >= p.budget {
		p.capped(rung, kind, subject, rig, "budget")
		return false
	}
	return true
}

// mutate runs fn unless dry-run, charges the action budget, and records a
// failed mutation as a pass error. Returns whether the action should be
// reported as taken.
func (p *healPass) mutate(fn func() error, action *healAction) bool {
	p.acted++
	if p.deps.DryRun {
		p.report.Actions = append(p.report.Actions, *action)
		return false
	}
	if err := fn(); err != nil {
		p.errf("heal: %s %s: %v", action.Kind, action.Subject, err)
		return false
	}
	return true
}

// record reports a completed action and emits its audit event.
func (p *healPass) record(action healAction) {
	p.report.Actions = append(p.report.Actions, action)
	if p.deps.Rec == nil {
		return
	}
	payload, _ := json.Marshal(events.HealActionPayload{
		Rung:   action.Rung,
		Kind:   action.Kind,
		Rig:    action.Rig,
		Before: action.Before,
		After:  action.After,
	})
	// Subject carries the prefixed form ("bead:<id>" / "session:<name>") so
	// the cooldown read-back (RecentActions) round-trips it exactly.
	p.deps.Rec.Record(events.Event{
		Type:    events.HealActionTaken,
		Actor:   healActorName,
		Subject: action.Subject,
		Message: fmt.Sprintf("heal rung %d %s: %s -> %s", action.Rung, action.Kind, action.Before, action.After),
		Payload: payload,
	})
}

// capped reports a suppressed action and emits the loud suppression record.
func (p *healPass) capped(rung int, kind, subject, rig, reason string) {
	action := healAction{Rung: rung, Kind: kind, Subject: subject, Rig: rig, Capped: true, Reason: reason}
	p.report.Actions = append(p.report.Actions, action)
	if p.deps.Rec == nil || p.deps.DryRun {
		return
	}
	payload, _ := json.Marshal(events.HealActionCappedPayload{
		Rung: rung, Kind: kind, Rig: rig, Reason: reason,
	})
	p.deps.Rec.Record(events.Event{
		Type:    events.HealActionCapped,
		Actor:   healActorName,
		Subject: subject,
		Message: fmt.Sprintf("heal rung %d %s suppressed (%s)", rung, kind, reason),
		Payload: payload,
	})
}

// emitStall records the stall detection event for one rig.
func (p *healPass) emitStall(rigReport healRigReport, window time.Duration) {
	if p.deps.Rec == nil || p.deps.DryRun {
		return
	}
	payload, _ := json.Marshal(events.HealStallDetectedPayload{
		Rig:                rigReport.Rig,
		Branch:             rigReport.Branch,
		WindowSeconds:      int64(window / time.Second),
		LandedCommits:      rigReport.LandedCommits,
		OldestDemandAgeSec: rigReport.OldestDemandAgeSec,
	})
	p.deps.Rec.Record(events.Event{
		Type:    events.HealStallDetected,
		Actor:   healActorName,
		Subject: rigReport.Rig,
		Message: fmt.Sprintf("rig %s stalled: 0 commits landed on %s in %s with demand waiting %s", rigReport.Rig, rigReport.Branch, window, rigReport.OldestDemandAge.Truncate(time.Second)),
		Payload: payload,
	})
}

// oldestDemandAge returns the age of the oldest outstanding work signal in
// the store: a ready unassigned routed bead (waiting for claim), an open
// assigned routed bead (a rung-1 orphan candidate — outstanding work parked
// where no claim probe can see it; the ggc-svb9 sweep made the queue LOOK
// empty precisely by assigning everything away, so assigned work must count
// as demand or the stall gate never opens on that failure), or any
// in_progress bead. Zero when no demand exists (a quiet rig is not stalled).
func (p *healPass) oldestDemandAge(store beads.Store) time.Duration {
	var oldest time.Duration
	consider := func(wb beads.Bead) {
		if wb.Ephemeral || healBeadHeld(wb) {
			return
		}
		if age := p.now.Sub(healBeadFreshness(wb)); age > oldest {
			oldest = age
		}
	}
	ready, err := store.Ready(beads.ReadyQuery{})
	if err != nil {
		p.errf("heal: demand ready query: %v", err)
	} else {
		for _, wb := range ready {
			if strings.TrimSpace(wb.Assignee) != "" || routedToOrLegacyWorkflowTarget(wb) == "" {
				continue
			}
			consider(wb)
		}
	}
	open, err := store.List(beads.ListQuery{Status: "open"})
	if err != nil {
		p.errf("heal: demand open query: %v", err)
	} else {
		for _, wb := range open {
			if strings.TrimSpace(wb.Assignee) == "" || routedToOrLegacyWorkflowTarget(wb) == "" {
				continue
			}
			consider(wb)
		}
	}
	inProgress, err := store.List(beads.ListQuery{Status: "in_progress"})
	if err != nil {
		p.errf("heal: demand in_progress query: %v", err)
	} else {
		for _, wb := range inProgress {
			consider(wb)
		}
	}
	return oldest
}

// busyIdentities returns every assignee identity currently holding
// in_progress work across the city store and all target rig stores.
func (p *healPass) busyIdentities() (map[string]bool, bool) {
	busy := map[string]bool{}
	complete := true
	scan := func(store beads.Store) {
		if store == nil {
			complete = false
			return
		}
		inProgress, err := store.List(beads.ListQuery{Status: "in_progress"})
		if err != nil {
			p.errf("heal: busy-identity scan: %v", err)
			complete = false
			return
		}
		for _, wb := range inProgress {
			if assignee := strings.TrimSpace(wb.Assignee); assignee != "" {
				busy[assignee] = true
			}
		}
	}
	scan(p.deps.CityStore)
	seen := map[beads.Store]bool{p.deps.CityStore: true}
	for _, target := range p.deps.Cfg.Heal.Targets {
		store := p.rigStore(target.Rig)
		if store == nil || seen[store] {
			continue
		}
		seen[store] = true
		scan(store)
	}
	return busy, complete
}

// openSessions loads the open session records. Its boolean result reports
// whether the inventory is complete enough for session-dependent remediation.
func (p *healPass) openSessions() ([]session.Info, bool) {
	if p.deps.ListOpenSessions == nil {
		p.errf("heal: session lister unavailable")
		return nil, false
	}
	sessions, err := p.deps.ListOpenSessions()
	if err != nil {
		p.errf("heal: listing sessions: %v", err)
		return nil, false
	}
	open := sessions[:0:0]
	for _, info := range sessions {
		if info.Closed {
			continue
		}
		open = append(open, info)
	}
	sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	return open, true
}

// observe wraps the Observe dep with a nil guard.
func (p *healPass) observe(target string) (worker.LiveObservation, error) {
	if p.deps.Observe == nil {
		return worker.LiveObservation{}, fmt.Errorf("no session observer configured")
	}
	return p.deps.Observe(target)
}

// rigStore returns the bead store scoped to rig. A missing target store is not
// interchangeable with the city store: healing it through that fallback could
// mutate unrelated city-scoped work, so callers fail closed instead.
func (p *healPass) rigStore(rigName string) beads.Store {
	return p.deps.RigStores[rigName]
}

func (p *healPass) errf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	p.report.Errors = append(p.report.Errors, msg)
	if p.deps.Stderr != nil {
		fmt.Fprintln(p.deps.Stderr, msg) //nolint:errcheck // best-effort diagnostics
	}
}

// --- pure helpers ---------------------------------------------------------

// healSessionIdentityIndex maps every assignee identity of every open session
// to its record, mirroring how work beads reference their holders.
func healSessionIdentityIndex(sessions []session.Info) map[string]session.Info {
	index := make(map[string]session.Info, len(sessions)*3)
	for _, info := range sessions {
		for _, id := range sessionBeadAssigneeIdentitiesInfo(info) {
			if id = strings.TrimSpace(id); id != "" {
				index[id] = info
			}
		}
	}
	return index
}

// healAnyIdentityBusy reports whether any of the session's assignee
// identities holds in_progress work.
func healAnyIdentityBusy(info session.Info, busy map[string]bool) bool {
	for _, id := range sessionBeadAssigneeIdentitiesInfo(info) {
		if busy[strings.TrimSpace(id)] {
			return true
		}
	}
	return false
}

// healSessionTarget returns the addressable target for a session record:
// the resolved runtime session name when known, falling back to the raw
// session_name metadata and finally the session bead ID.
func healSessionTarget(info session.Info) string {
	if name := strings.TrimSpace(info.SessionName); name != "" {
		return name
	}
	if name := strings.TrimSpace(info.SessionNameMetadata); name != "" {
		return name
	}
	return info.ID
}

// healIsQueueAddress reports whether assignee is a configured queue address
// for this target (a real scan queue the loop must never disturb).
func healIsQueueAddress(target config.HealTarget, assignee string) bool {
	for _, addr := range target.QueueAddresses {
		if strings.TrimSpace(addr) == assignee {
			return true
		}
	}
	return false
}

// healBeadHeld reports whether the bead carries a canonical dispatch hold
// label (deliberately paused work the loop must not move).
func healBeadHeld(wb beads.Bead) bool {
	for _, label := range beadmeta.DispatchHoldLabels {
		if beadLabelsContain(wb.Labels, label) {
			return true
		}
	}
	return false
}

// healBeadFreshness returns the bead's last-touched time (UpdatedAt, falling
// back to CreatedAt for legacy beads).
func healBeadFreshness(wb beads.Bead) time.Time {
	if !wb.UpdatedAt.IsZero() {
		return wb.UpdatedAt
	}
	return wb.CreatedAt
}

// healRigRepoPath resolves the rig repository path: the configured rig path
// (relative paths resolve against the city root), defaulting to
// <city>/rigs/<name>.
func healRigRepoPath(cityPath string, rig config.Rig) string {
	p := strings.TrimSpace(rig.Path)
	if p == "" {
		return filepath.Join(cityPath, "rigs", rig.Name)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cityPath, p)
	}
	return filepath.Clean(p)
}
