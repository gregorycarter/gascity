package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

// healCaptureRecorder captures heal audit events for assertions.
type healCaptureRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *healCaptureRecorder) Record(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *healCaptureRecorder) byType(eventType string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.events {
		if e.Type == eventType {
			out = append(out, e)
		}
	}
	return out
}

// healTestEnv is the fully-faked world one heal pass runs against.
type healTestEnv struct {
	store    *beads.MemStore
	cfg      *config.City
	rec      *healCaptureRecorder
	now      time.Time
	sessions []session.Info
	// alive/attached/running index runtime observations by session target.
	alive    map[string]bool
	attached map[string]bool
	running  map[string]bool

	landed    int
	landedErr error
	checkErr  error

	killed  []string
	started []string
	nudged  []string
	recent  map[string]time.Time
}

func newHealTestEnv(t *testing.T) *healTestEnv {
	t.Helper()
	return &healTestEnv{
		store: beads.NewMemStore(),
		cfg: &config.City{
			Rigs:   []config.Rig{{Name: "demo", DefaultBranch: "main"}},
			Agents: []config.Agent{{Name: "demo/worker"}},
			Heal: config.HealConfig{
				Enabled: true,
				Targets: []config.HealTarget{{
					Rig:            "demo",
					QueueAddresses: []string{"demo/merge-queue"},
				}},
			},
		},
		rec: &healCaptureRecorder{},
		// The pass observes the world two hours from "wall now" so beads
		// created through MemStore (which stamps time.Now) age past every
		// default threshold.
		now:      time.Now().Add(2 * time.Hour),
		alive:    map[string]bool{},
		attached: map[string]bool{},
		running:  map[string]bool{},
		recent:   map[string]time.Time{},
	}
}

func (env *healTestEnv) deps() healDeps {
	return healDeps{
		Cfg:       env.cfg,
		CityPath:  "/city",
		Now:       func() time.Time { return env.now },
		CityStore: env.store,
		RigStores: map[string]beads.Store{"demo": env.store},
		ListOpenSessions: func() ([]session.Info, error) {
			return env.sessions, nil
		},
		Observe: func(target string) (worker.LiveObservation, error) {
			running, ok := env.running[target]
			if !ok {
				return worker.LiveObservation{}, fmt.Errorf("session %q not found", target)
			}
			return worker.LiveObservation{
				Running:  running,
				Alive:    env.alive[target],
				Attached: env.attached[target],
			}, nil
		},
		Kill: func(target string) error {
			env.killed = append(env.killed, target)
			return nil
		},
		Start: func(target string) error {
			env.started = append(env.started, target)
			return nil
		},
		Nudge: func(target, _ string) error {
			env.nudged = append(env.nudged, target)
			return nil
		},
		LandedCommits: func(_, _ string, _ time.Time) (int, error) {
			return env.landed, env.landedErr
		},
		RunCheck: func(_, _ string) error {
			return env.checkErr
		},
		Rec: env.rec,
		RecentActions: func(_ time.Time) map[string]time.Time {
			return env.recent
		},
		DryRun: false,
	}
}

// healTestRoute is the pool route every fixture bead is routed to; it
// matches the configured "demo/worker" agent template.
const healTestRoute = "demo/worker"

// createRouted creates an open bead routed to healTestRoute and applies
// mutations.
func (env *healTestEnv) createRouted(t *testing.T, mutate func(*beads.UpdateOpts)) beads.Bead {
	t.Helper()
	created, err := env.store.Create(beads.Bead{
		Title:    "work",
		Metadata: map[string]string{beadmeta.RoutedToMetadataKey: healTestRoute},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mutate != nil {
		var opts beads.UpdateOpts
		mutate(&opts)
		if err := env.store.Update(created.ID, opts); err != nil {
			t.Fatalf("Update(%s): %v", created.ID, err)
		}
	}
	got, err := env.store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", created.ID, err)
	}
	return got
}

func (env *healTestEnv) get(t *testing.T, id string) beads.Bead {
	t.Helper()
	got, err := env.store.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return got
}

// TestHealReleasesSweptQueueBlackhole replays the ggc-svb9 incident: the
// entire routed queue swept onto an assignee no claim probe scans. The claim
// path requires an empty assignee, so the queue LOOKS empty while every bead
// is stranded. One heal pass must detect the stall (assigned work counts as
// demand) and return the beads to the pool, with no human in the loop.
func TestHealReleasesSweptQueueBlackhole(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0 // nothing landing on main

	swept1 := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})
	swept2 := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	report := runHealPass(env.deps())

	if len(report.Rigs) != 1 || !report.Rigs[0].Stalled {
		t.Fatalf("rig not detected as stalled: %+v", report.Rigs)
	}
	for _, id := range []string{swept1.ID, swept2.ID} {
		got := env.get(t, id)
		if got.Assignee != "" || got.Status != "open" {
			t.Errorf("bead %s = status=%q assignee=%q, want open/unassigned", id, got.Status, got.Assignee)
		}
	}
	if got := len(env.rec.byType(events.HealStallDetected)); got != 1 {
		t.Errorf("stall events = %d, want 1", got)
	}
	if got := len(env.rec.byType(events.HealActionTaken)); got != 2 {
		t.Errorf("action events = %d, want 2", got)
	}

	// The released beads are now claimable through the routed ready tier.
	ready, err := env.store.Ready(beads.ReadyQuery{})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	claimable := 0
	for _, wb := range ready {
		if wb.Assignee == "" && wb.Metadata[beadmeta.RoutedToMetadataKey] == "demo/worker" {
			claimable++
		}
	}
	if claimable != 2 {
		t.Errorf("claimable routed beads after heal = %d, want 2", claimable)
	}
}

func TestHealLeavesQueueAddressesAndFreshAndHeldWorkAlone(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0

	queueOwned := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("demo/merge-queue")
	})
	held := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("somewhere")
		o.Labels = []string{"hold:mayor"}
	})
	// An aged orphan makes the rig stalled so rung 1 actually runs.
	orphan := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	runHealPass(env.deps())

	if got := env.get(t, queueOwned.ID); got.Assignee != "demo/merge-queue" {
		t.Errorf("queue-owned bead released: assignee=%q", got.Assignee)
	}
	if got := env.get(t, held.ID); got.Assignee != "somewhere" {
		t.Errorf("held bead released: assignee=%q", got.Assignee)
	}
	if got := env.get(t, orphan.ID); got.Assignee != "" {
		t.Errorf("orphan not released: assignee=%q", got.Assignee)
	}
}

func TestHealFreshAssignmentNotReleased(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0
	// Pass runs at wall-now + 2h; this bead is stale. A second bead is made
	// "fresh" by running the pass at wall-now + 1m instead.
	fresh := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})
	env.now = time.Now().Add(time.Minute)

	report := runHealPass(env.deps())

	if got := env.get(t, fresh.ID); got.Assignee != "coordinator-address" {
		t.Errorf("fresh assignment released: assignee=%q", got.Assignee)
	}
	// Demand exists (assigned routed work) but is younger than the window,
	// so the rig must not read as stalled.
	if len(report.Rigs) != 1 || report.Rigs[0].Stalled {
		t.Errorf("rig stalled despite young demand: %+v", report.Rigs)
	}
}

func TestHealNotStalledWhenCommitsLand(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 3 // merges flowing

	orphan := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	report := runHealPass(env.deps())

	if len(report.Rigs) != 1 || report.Rigs[0].Stalled {
		t.Fatalf("rig stalled while commits land: %+v", report.Rigs)
	}
	if got := env.get(t, orphan.ID); got.Assignee != "coordinator-address" {
		t.Errorf("rung 1 ran while not stalled: assignee=%q", got.Assignee)
	}
	if got := len(env.rec.byType(events.HealActionTaken)); got != 0 {
		t.Errorf("action events while healthy = %d, want 0", got)
	}
}

func TestHealMeasurementFailureFailsSafe(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0
	env.landedErr = fmt.Errorf("git exploded")

	orphan := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	report := runHealPass(env.deps())

	if len(report.Rigs) != 1 || report.Rigs[0].Stalled {
		t.Fatalf("rig stalled on measurement error: %+v", report.Rigs)
	}
	if got := env.get(t, orphan.ID); got.Assignee != "coordinator-address" {
		t.Errorf("remediation ran on measurement error: assignee=%q", got.Assignee)
	}
	if len(report.Errors) == 0 {
		t.Error("measurement error not recorded in report")
	}
}

func TestHealForceAssignsInvertedPriorityToIdleWorker(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0

	p0 := env.createRouted(t, func(o *beads.UpdateOpts) {
		zero := 0
		o.Priority = &zero
	})
	// Lower-priority in_progress work makes the stall demand and the
	// inversion visible.
	env.createRouted(t, func(o *beads.UpdateOpts) {
		two := 2
		o.Priority = &two
		o.Status = strPtr("in_progress")
		o.Assignee = strPtr("busy-worker")
	})

	env.sessions = []session.Info{
		{ID: "sess-idle", SessionNameMetadata: "worker-idle", Template: "demo/worker", PoolManaged: true},
	}
	env.running["worker-idle"] = true
	env.alive["worker-idle"] = true

	report := runHealPass(env.deps())

	got := env.get(t, p0.ID)
	if got.Assignee != "sess-idle" {
		t.Fatalf("P0 assignee = %q, want sess-idle (report: %+v)", got.Assignee, report.Actions)
	}
	if len(env.nudged) != 1 || env.nudged[0] != "worker-idle" {
		t.Errorf("nudged = %v, want [worker-idle]", env.nudged)
	}
}

func TestHealInversionWithoutIdleWorkerIsRecordedNotForced(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0

	p0 := env.createRouted(t, func(o *beads.UpdateOpts) {
		zero := 0
		o.Priority = &zero
	})
	// The only worker is busy with in_progress work.
	env.createRouted(t, func(o *beads.UpdateOpts) {
		two := 2
		o.Priority = &two
		o.Status = strPtr("in_progress")
		o.Assignee = strPtr("sess-busy")
	})
	env.sessions = []session.Info{
		{ID: "sess-busy", SessionNameMetadata: "worker-busy", Template: "demo/worker", PoolManaged: true},
	}
	env.running["worker-busy"] = true
	env.alive["worker-busy"] = true

	runHealPass(env.deps())

	if got := env.get(t, p0.ID); got.Assignee != "" {
		t.Errorf("P0 force-assigned to busy worker: assignee=%q", got.Assignee)
	}
	capped := env.rec.byType(events.HealActionCapped)
	found := false
	for _, e := range capped {
		if strings.Contains(e.Message, "no-idle-worker") {
			found = true
		}
	}
	if !found {
		t.Errorf("no-idle-worker suppression not recorded loudly: %+v", capped)
	}
}

func TestHealReleasesWorkHeldByDeadSession(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0

	dead := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Status = strPtr("in_progress")
		o.Assignee = strPtr("ghost-session")
	})

	runHealPass(env.deps())

	got := env.get(t, dead.ID)
	if got.Status != "open" || got.Assignee != "" {
		t.Errorf("dead-session bead = status=%q assignee=%q, want open/unassigned", got.Status, got.Assignee)
	}
}

func TestHealReopensUnassignedInProgressLimbo(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0

	limbo := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Status = strPtr("in_progress")
	})

	runHealPass(env.deps())

	got := env.get(t, limbo.ID)
	if got.Status != "open" {
		t.Errorf("limbo bead status = %q, want open", got.Status)
	}
}

func TestHealRestartsStuckPoolWorkerAndReleasesWork(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0
	env.now = time.Now().Add(3 * time.Hour) // past the 2h stuck threshold

	stuck := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Status = strPtr("in_progress")
		o.Assignee = strPtr("sess-stuck")
	})
	env.sessions = []session.Info{
		{ID: "sess-stuck", SessionNameMetadata: "worker-stuck", Template: "demo/worker", PoolManaged: true},
	}
	env.running["worker-stuck"] = true
	env.alive["worker-stuck"] = true

	runHealPass(env.deps())

	if len(env.killed) != 1 || env.killed[0] != "worker-stuck" {
		t.Fatalf("killed = %v, want [worker-stuck]", env.killed)
	}
	got := env.get(t, stuck.ID)
	if got.Status != "open" || got.Assignee != "" {
		t.Errorf("stuck bead = status=%q assignee=%q, want open/unassigned", got.Status, got.Assignee)
	}
}

func TestHealStuckNamedOrAttachedSessionIsRecordedNotKilled(t *testing.T) {
	for name, mutate := range map[string]func(env *healTestEnv){
		"named session": func(env *healTestEnv) {
			env.sessions[0].PoolManaged = false
		},
		"attached session": func(env *healTestEnv) {
			env.attached["worker-stuck"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := newHealTestEnv(t)
			env.landed = 0
			env.now = time.Now().Add(3 * time.Hour)

			stuck := env.createRouted(t, func(o *beads.UpdateOpts) {
				o.Status = strPtr("in_progress")
				o.Assignee = strPtr("sess-stuck")
			})
			env.sessions = []session.Info{
				{ID: "sess-stuck", SessionNameMetadata: "worker-stuck", Template: "demo/worker", PoolManaged: true},
			}
			env.running["worker-stuck"] = true
			env.alive["worker-stuck"] = true
			mutate(env)

			runHealPass(env.deps())

			if len(env.killed) != 0 {
				t.Errorf("killed = %v, want none", env.killed)
			}
			got := env.get(t, stuck.ID)
			if got.Status != "in_progress" || got.Assignee != "sess-stuck" {
				t.Errorf("bead mutated: status=%q assignee=%q", got.Status, got.Assignee)
			}
			recorded := false
			for _, e := range env.rec.byType(events.HealActionTaken) {
				if strings.Contains(e.Message, "stuck-recorded") {
					recorded = true
				}
			}
			if !recorded {
				t.Error("stuck holder not recorded loudly")
			}
		})
	}
}

func TestHealMainRedFilesOneRoutedRepairBead(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 5 // merges flowing: red mainline is a DISTINCT signal
	env.checkErr = fmt.Errorf("exit 1")
	env.cfg.Heal.Targets[0].MainRedCheck = "check-main"
	env.cfg.Heal.Targets[0].MainRedRoute = "demo/worker"

	report := runHealPass(env.deps())

	if len(report.Rigs) != 1 || !report.Rigs[0].MainRed {
		t.Fatalf("main red not detected: %+v", report.Rigs)
	}
	if report.Rigs[0].Stalled {
		t.Fatalf("red mainline misread as stall: %+v", report.Rigs)
	}
	repairs, err := env.store.List(beads.ListQuery{Metadata: map[string]string{healReasonMetadataKey: healMainRedReason}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repairs) != 1 {
		t.Fatalf("repair beads = %d, want 1", len(repairs))
	}
	repair := repairs[0]
	if repair.Metadata[beadmeta.RoutedToMetadataKey] != "demo/worker" {
		t.Errorf("repair route = %q, want demo/worker", repair.Metadata[beadmeta.RoutedToMetadataKey])
	}
	if repair.Priority == nil || *repair.Priority != 0 {
		t.Errorf("repair priority = %v, want 0", repair.Priority)
	}

	// Second pass: the open repair bead dedupes — no duplicate filed.
	runHealPass(env.deps())
	repairs, err = env.store.List(beads.ListQuery{Metadata: map[string]string{healReasonMetadataKey: healMainRedReason}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repairs) != 1 {
		t.Errorf("repair beads after second pass = %d, want 1 (dedup failed)", len(repairs))
	}
}

func TestHealRestartsCriticalSessionWithCooldown(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 5
	env.cfg.Heal.CriticalSessions = []string{"coordinator"}
	// "coordinator" absent from env.running: Observe errors — down.

	runHealPass(env.deps())
	if len(env.started) != 1 || env.started[0] != "coordinator" {
		t.Fatalf("started = %v, want [coordinator]", env.started)
	}

	// Within cooldown the restart is suppressed and recorded loudly.
	env.started = nil
	env.recent = map[string]time.Time{"session:coordinator": env.now.Add(-time.Minute)}
	runHealPass(env.deps())
	if len(env.started) != 0 {
		t.Errorf("started within cooldown = %v, want none", env.started)
	}
	if got := len(env.rec.byType(events.HealActionCapped)); got == 0 {
		t.Error("cooldown suppression not recorded loudly")
	}

	// A healthy critical session (runtime present AND agent process alive)
	// is left alone.
	env.started = nil
	env.recent = map[string]time.Time{}
	env.running["coordinator"] = true
	env.alive["coordinator"] = true
	runHealPass(env.deps())
	if len(env.started) != 0 {
		t.Errorf("started while running = %v, want none", env.started)
	}
}

// TestHealRestartsCriticalSessionWithDeadProcess covers the crashed-agent
// shape: the runtime session survives (Running=true) but the agent process
// inside is dead (Alive=false). The rung must clear the dead runtime and
// start fresh — gating on Running alone would leave the coordinator down.
func TestHealRestartsCriticalSessionWithDeadProcess(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 5
	env.cfg.Heal.CriticalSessions = []string{"coordinator"}
	env.running["coordinator"] = true // runtime shell survives
	// alive stays false: the agent process crashed.

	runHealPass(env.deps())

	if len(env.killed) != 1 || env.killed[0] != "coordinator" {
		t.Errorf("killed = %v, want [coordinator] (dead runtime cleared before restart)", env.killed)
	}
	if len(env.started) != 1 || env.started[0] != "coordinator" {
		t.Errorf("started = %v, want [coordinator]", env.started)
	}
}

func TestHealBudgetCapsActionsPerPass(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0
	env.cfg.Heal.MaxActionsPerPass = 1

	first := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})
	second := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	runHealPass(env.deps())

	released := 0
	for _, id := range []string{first.ID, second.ID} {
		if env.get(t, id).Assignee == "" {
			released++
		}
	}
	if released != 1 {
		t.Errorf("released = %d, want exactly 1 (budget)", released)
	}
	budgetCapped := false
	for _, e := range env.rec.byType(events.HealActionCapped) {
		if strings.Contains(e.Message, "budget") {
			budgetCapped = true
		}
	}
	if !budgetCapped {
		t.Error("budget suppression not recorded loudly")
	}
}

func TestHealSubjectCooldownPreventsThrash(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0

	orphan := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})
	env.recent = map[string]time.Time{"bead:" + orphan.ID: env.now.Add(-10 * time.Minute)}

	runHealPass(env.deps())

	if got := env.get(t, orphan.ID); got.Assignee != "coordinator-address" {
		t.Errorf("bead released within cooldown: assignee=%q", got.Assignee)
	}
	if got := len(env.rec.byType(events.HealActionCapped)); got != 1 {
		t.Errorf("capped events = %d, want 1", got)
	}
}

func TestHealDryRunMutatesNothingAndRecordsNoEvents(t *testing.T) {
	env := newHealTestEnv(t)
	env.landed = 0
	env.cfg.Heal.CriticalSessions = []string{"coordinator"}
	env.cfg.Heal.Targets[0].MainRedCheck = "check-main"
	env.cfg.Heal.Targets[0].MainRedRoute = "demo/worker"
	env.checkErr = fmt.Errorf("exit 1")

	orphan := env.createRouted(t, func(o *beads.UpdateOpts) {
		o.Assignee = strPtr("coordinator-address")
	})

	deps := env.deps()
	deps.DryRun = true
	report := runHealPass(deps)

	if got := env.get(t, orphan.ID); got.Assignee != "coordinator-address" {
		t.Errorf("dry-run mutated bead: assignee=%q", got.Assignee)
	}
	if len(env.started) != 0 || len(env.killed) != 0 || len(env.nudged) != 0 {
		t.Errorf("dry-run performed session ops: started=%v killed=%v nudged=%v", env.started, env.killed, env.nudged)
	}
	if len(env.rec.events) != 0 {
		t.Errorf("dry-run recorded %d events, want 0", len(env.rec.events))
	}
	if len(report.Actions) == 0 {
		t.Error("dry-run reported no actions")
	}
	if !report.DryRun {
		t.Error("report.DryRun = false")
	}
}

func TestHealRigRepoPath(t *testing.T) {
	cases := []struct {
		name string
		rig  config.Rig
		want string
	}{
		{"default layout", config.Rig{Name: "demo"}, filepath.Join("/city", "rigs", "demo")},
		{"absolute path", config.Rig{Name: "demo", Path: "/repos/demo"}, "/repos/demo"},
		{"relative path", config.Rig{Name: "demo", Path: "checkouts/demo"}, filepath.Join("/city", "checkouts", "demo")},
	}
	for _, tc := range cases {
		if got := healRigRepoPath("/city", tc.rig); got != tc.want {
			t.Errorf("%s: healRigRepoPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}
