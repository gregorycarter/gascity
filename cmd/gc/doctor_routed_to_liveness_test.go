package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// TestRoutedToLivenessCheck covers the shared ga-ovk/ga-2en silent-black-hole
// check: an open, unheld, unblocked, non-epic bead whose gc.routed_to is
// empty (rejection-blanked route) or names a suspended/nonexistent agent or
// pool (route to suspended pool, route into suspended rig) is work nothing
// will ever pick up. The check must flag all three modes across city and rig
// stores, leave live routes/held/blocked/epic/non-open beads alone, stay
// advisory, and never offer --fix (there is no canonical target to restore).
func TestRoutedToLivenessCheck(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	frozenDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "worker"}, // live city agent
			// Suspended singleton agent (max_active_sessions=1 pins it to a
			// single canonical identity, so it is an agent, not a pool).
			{Name: "sleeper", Suspended: true, MaxActiveSessions: intPtr(1)},
			{Name: "digger", Dir: "frozen"}, // agent in suspended rig
			// Live pool in a live rig; slot-suffixed routes must resolve to it.
			{Name: "polecat", Dir: "repo", MaxActiveSessions: intPtr(2)},
			// Suspended pool: routing to its base identity is a black hole.
			{Name: "stalled", Dir: "repo", MaxActiveSessions: intPtr(2), Suspended: true},
		},
		NamedSessions: []config.NamedSession{
			{Name: "dashboard", Template: "worker"},
		},
		Rigs: []config.Rig{
			{Name: "repo", Path: rigDir},
			{Name: "frozen", Path: frozenDir, Suspended: true},
		},
	}

	cityStore := beads.NewMemStoreFrom(0, []beads.Bead{
		// Mode 1: rejection-blanked route — gc.routed_to missing entirely.
		{ID: "L-1", Title: "orphan", Type: "task", Status: "open"},
		// Mode 2: route names no configured agent or pool.
		{
			ID: "L-2", Title: "ghost", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "ghost"},
		},
		// Mode 3a: route to a suspended agent.
		{
			ID: "L-3", Title: "asleep", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "sleeper"},
		},
		// Mode 3b: route into a suspended rig.
		{
			ID: "L-4", Title: "frozen", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "frozen/digger"},
		},
		// Mode 3c: route to a suspended pool's base identity.
		{
			ID: "L-5", Title: "stalled", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "repo/stalled"},
		},
		// Live routes — must not be flagged.
		{
			ID: "L-6", Title: "fine", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		},
		{
			ID: "L-7", Title: "pool slot", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "repo/polecat-2"},
		},
		{
			ID: "L-8", Title: "named session", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "dashboard"},
		},
		// Whitespace around a live route must not trip the check.
		{
			ID: "L-13", Title: "padded", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": " repo/polecat "},
		},
		// Exclusions — must not be flagged even with an empty route.
		{ID: "L-9", Title: "epic", Type: "epic", Status: "open"},
		{ID: "L-10", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"}},
		{ID: "L-11", Title: "claimed", Type: "task", Status: "in_progress"},
		{ID: "L-12", Title: "blocked", Type: "task", Status: "open", IsBlocked: boolPtr(true)},
	}, nil)
	rigStore := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "RL-1", Title: "rig orphan", Type: "task", Status: "open"},
	}, nil)
	frozenStore := beads.NewMemStoreFrom(0, []beads.Bead{
		// Lives in the suspended rig's own store: out of scan scope, must
		// never be reported (the factory would also fail if it were).
		{ID: "FL-1", Title: "unreachable", Type: "task", Status: "open"},
	}, nil)
	stores := map[string]beads.Store{
		cityDir:   routedToLivenessBlockedStore{Store: cityStore},
		rigDir:    routedToLivenessBlockedStore{Store: rigStore},
		frozenDir: routedToLivenessBlockedStore{Store: frozenStore},
	}
	factory := func(path string) (beads.Store, error) {
		store, ok := stores[path]
		if !ok {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	}

	check := newRoutedToLivenessCheck(cfg, cityDir, factory)

	if check.CanFix() {
		t.Fatal("CanFix() = true, want false (no canonical target to restore)")
	}
	if check.WarmupEligible() {
		t.Fatal("WarmupEligible() = true, want false (store-scanning check)")
	}

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Run status = %v, want warning: %#v", res.Status, res)
	}
	if res.Severity != doctor.SeverityAdvisory {
		t.Fatalf("severity = %v, want SeverityAdvisory (must never gate)", res.Severity)
	}
	details := strings.Join(res.Details, "\n")
	for _, want := range []string{"L-1", "L-2", "L-3", "L-4", "L-5", "RL-1"} {
		if !strings.Contains(details, want) {
			t.Fatalf("details missing %q:\n%s", want, details)
		}
	}
	for _, notWant := range []string{"L-6", "L-7", "L-8", "L-9", "L-10", "L-11", "L-12", "L-13", "FL-1"} {
		if strings.Contains(details, notWant) {
			t.Fatalf("details should not mention %q:\n%s", notWant, details)
		}
	}
	// Per-bead details must say why each mode is a black hole.
	for _, want := range []string{"empty", "unknown", "suspended agent", "suspended rig", "suspended pool"} {
		if !strings.Contains(details, want) {
			t.Fatalf("details missing reason %q:\n%s", want, details)
		}
	}
	// The blocked projection was available (L-12 carried IsBlocked), so the
	// unavailable-projection note must not appear.
	if strings.Contains(details, "unavailable") {
		t.Fatalf("details should not note an unavailable blocked projection:\n%s", details)
	}
}

// TestRoutedToLivenessCheckCleanStore confirms a store whose open beads all
// route to live targets reports OK.
func TestRoutedToLivenessCheckCleanStore(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{Agents: []config.Agent{{Name: "worker"}}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{
			ID: "L-1", Title: "fine", Type: "task", Status: "open",
			Metadata: map[string]string{"gc.routed_to": "worker"},
		},
		{ID: "L-2", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:external"}},
	}, nil)
	check := newRoutedToLivenessCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		if path != cityDir {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	})
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run status = %v, want OK: %#v", res.Status, res)
	}
	if res.Severity != doctor.SeverityAdvisory {
		t.Fatalf("severity = %v, want SeverityAdvisory even when OK", res.Severity)
	}
}

// TestRoutedToLivenessCheckSkippedScopes confirms store open/list failures
// are reported as skipped scopes, not silently dropped.
func TestRoutedToLivenessCheckSkippedScopes(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	cfg := &config.City{Rigs: []config.Rig{{Name: "repo", Path: rigDir}}}
	check := newRoutedToLivenessCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		if path == rigDir {
			return nil, errors.New("permission denied")
		}
		return routedToLivenessListErrorStore{Store: beads.NewMemStore()}, nil
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Run status = %v, want warning: %#v", res.Status, res)
	}
	details := strings.Join(res.Details, "\n")
	if !strings.Contains(details, "rig repo skipped") || !strings.Contains(details, "permission denied") {
		t.Fatalf("details missing rig open failure:\n%s", details)
	}
	if !strings.Contains(details, "city skipped") || !strings.Contains(details, "listing failed") {
		t.Fatalf("details missing city list failure:\n%s", details)
	}
}

// TestRoutedToLivenessCheckBlockedProjectionUnavailable confirms that when
// the store does not provide the IsBlocked projection, the check notes it
// instead of silently treating every bead as unblocked.
func TestRoutedToLivenessCheckBlockedProjectionUnavailable(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{Agents: []config.Agent{{Name: "worker"}}}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "L-1", Title: "orphan", Type: "task", Status: "open"},
	}, nil)
	check := newRoutedToLivenessCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		if path != cityDir {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	})
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Run status = %v, want warning: %#v", res.Status, res)
	}
	details := strings.Join(res.Details, "\n")
	if !strings.Contains(details, "L-1") {
		t.Fatalf("details missing flagged bead L-1:\n%s", details)
	}
	if !strings.Contains(details, "unavailable") {
		t.Fatalf("details should note the unavailable blocked projection:\n%s", details)
	}
}

type routedToLivenessListErrorStore struct {
	beads.Store
}

func (s routedToLivenessListErrorStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, errors.New("listing failed")
}

// routedToLivenessBlockedStore stamps the IsBlocked ready projection (false
// where unset) on List results, mirroring bd-backed stores.
type routedToLivenessBlockedStore struct {
	beads.Store
}

func (s routedToLivenessBlockedStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	items, err := s.Store.List(q)
	for i := range items {
		if items[i].IsBlocked == nil {
			items[i].IsBlocked = boolPtr(false)
		}
	}
	return items, err
}
