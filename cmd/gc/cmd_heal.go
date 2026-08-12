package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/molecule"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

// healGitTimeout bounds the fetch + rev-list throughput probe per rig.
const healGitTimeout = 30 * time.Second

// healStartTimeout bounds a rung-5 critical-session start.
const healStartTimeout = 2 * time.Minute

func newHealCmd(stdout, stderr io.Writer) *cobra.Command {
	var dryRun, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "heal",
		Short: "Run one self-healing throughput pass",
		Long: `Run one deterministic self-healing pass over the city's [heal] targets.

The pass measures landed commits on each target rig's mainline directly from
git, detects a stall (nothing landing while aged demand waits), and walks the
remediation ladder: release orphaned routed work, force-assign inverted
priorities, recover work held by dead or stuck sessions, file mainline-red
repair work, and restart critical sessions. Every detection and action is
recorded on the event bus (heal.stall_detected / heal.action / heal.capped);
per-subject cooldowns and a per-pass budget prevent thrash.

The controller runs this pass automatically when [heal] sets enabled = true.
Running "gc heal" directly performs one pass regardless of the enabled flag,
so an external scheduler (cron, launchd) can drive recovery even when the
controller itself is down.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if doHeal(dryRun, jsonOutput, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what the pass would do without mutating anything")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "JSON output")
	return cmd
}

func doHeal(dryRun, jsonOutput bool, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc heal: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath, configWarnWriter(jsonOutput, stderr))
	if err != nil {
		fmt.Fprintf(stderr, "gc heal: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if len(cfg.Heal.Targets) == 0 && len(cfg.Heal.CriticalSessions) == 0 {
		fmt.Fprintln(stderr, "gc heal: nothing to do — city.toml has no [heal] targets or critical_sessions") //nolint:errcheck
		return 1
	}
	store, code := openCityStore(stderr, "gc heal")
	if code != 0 {
		return code
	}
	sp, err := newSessionProviderForCity(cfg, cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc heal: constructing session provider: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	var rec events.Recorder
	if !dryRun {
		rec = openCityRecorderAt(cityPath, stderr)
	}
	deps := buildHealDeps(cfg, cityPath, store, buildStandaloneRigStores(cfg, cityPath, stderr), sp, rec, stderr, dryRun)
	report := runHealPass(deps)
	return emitHealReport(report, jsonOutput, stdout)
}

// buildHealDeps assembles the production effect wiring for one heal pass.
// Shared by the standalone `gc heal` command and the controller watchdog.
func buildHealDeps(
	cfg *config.City,
	cityPath string,
	cityStore beads.Store,
	rigStores map[string]beads.Store,
	sp runtime.Provider,
	rec events.Recorder,
	stderr io.Writer,
	dryRun bool,
) healDeps {
	sessStore := cliSessionStore(cityStore, cfg, cityPath)
	return healDeps{
		Cfg:       cfg,
		CityPath:  cityPath,
		Now:       time.Now,
		CityStore: cityStore,
		RigStores: rigStores,
		ListOpenSessions: func() ([]session.Info, error) {
			return session.NewStore(beads.SessionStore{Store: sessStore}).ListLabeledSessionInfosUnfiltered()
		},
		Observe: func(target string) (worker.LiveObservation, error) {
			return workerObserveSessionTargetWithConfig(cityPath, sessStore, sp, cfg, target)
		},
		Kill: func(target string) error {
			return workerKillSessionTargetWithConfig(cityPath, sessStore, sp, cfg, target)
		},
		Start:          healStartSessionSelfExec,
		Nudge:          healNudgeSession,
		LandedCommits:  healLandedCommits,
		RunCheck:       healRunCheck,
		AttachWorkflow: healAttachWorkflow(cfg),
		Rec:            rec,
		RecentActions: func(since time.Time) map[string]time.Time {
			return healRecentActions(rec, since)
		},
		Stderr: stderr,
		DryRun: dryRun,
	}
}

// healRecentActions reads the per-subject cooldown state back from the event
// log: subjects of heal.action events recorded at or after since. Returns nil
// when the recorder cannot list events; runHealPass treats that as an
// unavailable audit ledger and fails closed rather than bypassing cooldowns.
func healRecentActions(rec events.Recorder, since time.Time) map[string]time.Time {
	lister, ok := rec.(interface {
		List(events.Filter) ([]events.Event, error)
	})
	if !ok {
		return nil
	}
	evts, err := lister.List(events.Filter{Type: events.HealActionTaken, Since: since})
	if err != nil {
		return nil
	}
	recent := make(map[string]time.Time, len(evts))
	for _, e := range evts {
		subject := strings.TrimSpace(e.Subject)
		if subject == "" {
			continue
		}
		if last, ok := recent[subject]; !ok || e.Ts.After(last) {
			recent[subject] = e.Ts
		}
	}
	return recent
}

// healLandedCommits counts commits landed on the rig mainline within the
// window. It freshens the origin tracking ref when origin is configured and
// treats a fetch failure as an unreadable measurement: using a stale
// origin/<branch> ref could falsely declare a productive rig stalled. Rigs
// without an origin remote use their local branch instead.
func healLandedCommits(repoPath, branch string, since time.Time) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), healGitTimeout)
	defer cancel()
	g := git.New(repoPath)
	hasOrigin, err := g.HasRemote(ctx, "origin")
	if err != nil {
		return 0, fmt.Errorf("checking mainline remote: %w", err)
	}
	if hasOrigin {
		if err := g.FetchBranch(ctx, branch); err != nil {
			return 0, fmt.Errorf("fetching mainline %q: %w", branch, err)
		}
		count, err := g.CommitCountSince(ctx, "origin/"+branch, since)
		if err != nil {
			return 0, err
		}
		return count, nil
	}
	count, err := g.CommitCountSince(ctx, branch, since)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// healRunCheck executes a configured shell check command in dir. A nil
// return means the check passed (mainline green).
func healRunCheck(dir, command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), healCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return fmt.Errorf("check failed: %w: %s", err, msg)
	}
	return nil
}

// healAttachWorkflow instantiates the configured repair formula on a filed
// mainline-red bead, mirroring the GitHub PR monitor's attach path.
func healAttachWorkflow(cfg *config.City) func(store beads.Store, rigName, workflow string, bead beads.Bead) error {
	return func(store beads.Store, rigName, workflow string, bead beads.Bead) error {
		searchPaths := cfg.FormulaLayers.SearchPaths(strings.TrimSpace(rigName))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := molecule.CookOn(ctx, store, workflow, searchPaths, molecule.Options{
			ParentID:       bead.ID,
			IdempotencyKey: "heal-main-red-workflow:" + bead.ID,
		}); err != nil {
			return fmt.Errorf("instantiating repair workflow %q: %w", workflow, err)
		}
		return nil
	}
}

// healStartSessionSelfExec starts a configured session by re-invoking the gc
// binary (`gc session new <name> --no-attach`). Self-exec reuses the entire
// template-resolution and identity-locking start path — including its
// delegate-to-controller branch — without duplicating it here, and works when
// this process is a cron-driven `gc heal` with no controller alive.
func healStartSessionSelfExec(name string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving gc binary: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), healStartTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "session", "new", name, "--no-attach", "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return fmt.Errorf("starting session %q: %w: %s", name, err, msg)
	}
	return nil
}

// healNudgeSession delivers a best-effort wake message to a session through
// the standard nudge resolution path.
func healNudgeSession(target, message string) error {
	info, err := resolveNudgeTarget(target)
	if err != nil {
		return fmt.Errorf("resolving nudge target %q: %w", target, err)
	}
	if code := deliverSessionNudge(info, message, nudgeDeliveryWaitIdle, false, io.Discard, io.Discard); code != 0 {
		return fmt.Errorf("nudge delivery to %q failed", target)
	}
	return nil
}

// emitHealReport prints the pass report and returns the process exit code.
// A completed pass exits 0 even when individual actions errored — the errors
// are in the report and the event log; a scheduler must not retry-storm.
func emitHealReport(report healReport, jsonOutput bool, stdout io.Writer) int {
	if jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report) //nolint:errcheck // best-effort stdout
		return 0
	}
	for _, rig := range report.Rigs {
		fmt.Fprintf(stdout, "rig %s (%s): landed=%d demand_age=%s stalled=%v main_red=%v\n", //nolint:errcheck
			rig.Rig, rig.Branch, rig.LandedCommits, rig.OldestDemandAge.Truncate(time.Second), rig.Stalled, rig.MainRed)
	}
	for _, action := range report.Actions {
		switch {
		case action.Capped:
			fmt.Fprintf(stdout, "capped rung %d %s %s (%s)\n", action.Rung, action.Kind, action.Subject, action.Reason) //nolint:errcheck
		case report.DryRun:
			fmt.Fprintf(stdout, "would run rung %d %s %s: %s -> %s\n", action.Rung, action.Kind, action.Subject, action.Before, action.After) //nolint:errcheck
		default:
			fmt.Fprintf(stdout, "rung %d %s %s: %s -> %s\n", action.Rung, action.Kind, action.Subject, action.Before, action.After) //nolint:errcheck
		}
	}
	for _, msg := range report.Errors {
		fmt.Fprintf(stdout, "error: %s\n", msg) //nolint:errcheck
	}
	if len(report.Actions) == 0 && len(report.Errors) == 0 {
		fmt.Fprintln(stdout, "healthy: no remediation needed") //nolint:errcheck
	}
	return 0
}
