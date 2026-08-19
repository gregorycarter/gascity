package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/suspensionstate"
)

// routedToLivenessCheck detects the three silent-black-hole routing modes
// behind ga-ovk and ga-2en: an open, unheld, unblocked, non-epic bead whose
// gc.routed_to metadata is empty (a rejection-blanked or never-stamped
// route), names no configured agent or pool, or names a suspended agent/pool
// or an agent inside a suspended rig. In every mode the bead is work nothing
// will ever pick up, and nothing surfaces that. The check is report-only
// (SeverityAdvisory) and cannot --fix: there is no canonical target to
// restore, so re-routing is an operator decision.
type routedToLivenessCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
}

func newRoutedToLivenessCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *routedToLivenessCheck {
	return &routedToLivenessCheck{cfg: cfg, cityPath: cityPath, newStore: newStore}
}

func (c *routedToLivenessCheck) Name() string { return "routed-to-liveness" }

func (c *routedToLivenessCheck) CanFix() bool { return false }

func (c *routedToLivenessCheck) Fix(_ *doctor.CheckContext) error { return nil }

// routeTable maps every route identity the city's config can serve
// (agentutil.RoutedToIdentity for agents, QualifiedName for named sessions)
// to a black-hole reason; an empty reason means the target is live. When the
// same identity is registered more than once, a live registration wins over
// a suspended duplicate.
func (c *routedToLivenessCheck) routeTable(suspState suspensionstate.State) map[string]string {
	targets := map[string]string{}
	if c.cfg == nil {
		return targets
	}
	rigSuspendedReason := func(dir string) string {
		for _, rig := range c.cfg.Rigs {
			if rig.Name != dir {
				continue
			}
			if suspensionstate.EffectiveRigSuspended(suspState, rig.Name, rig.EffectiveSuspendedOnStart()) {
				return fmt.Sprintf("suspended rig %q", dir)
			}
			return ""
		}
		return ""
	}
	add := func(identity, reason string) {
		identity = strings.TrimSpace(identity)
		if identity == "" {
			return
		}
		if existing, ok := targets[identity]; ok && existing == "" {
			return
		}
		targets[identity] = reason
	}
	for i := range c.cfg.Agents {
		agent := c.cfg.Agents[i]
		reason := ""
		switch {
		case agent.Suspended && agentutil.IsMultiSessionAgent(&agent):
			reason = "suspended pool"
		case agent.Suspended:
			reason = "suspended agent"
		default:
			reason = rigSuspendedReason(strings.TrimSpace(agent.Dir))
		}
		add(agentutil.RoutedToIdentity(&agent), reason)
	}
	for i := range c.cfg.NamedSessions {
		session := c.cfg.NamedSessions[i]
		add(session.QualifiedName(), rigSuspendedReason(strings.TrimSpace(session.Dir)))
	}
	return targets
}

// routedToLivenessCandidate reports whether an open bead is routable work
// whose route should be checked: open status, no hold:<value> label, not an
// epic, and not dependency-blocked. Blocked state comes from the store's
// IsBlocked ready projection; a nil projection is treated as unblocked and
// surfaced via the note collect returns.
func routedToLivenessCandidate(b beads.Bead) bool {
	if b.Status != "open" || b.Type == "epic" {
		return false
	}
	for _, l := range b.Labels {
		if strings.HasPrefix(l, "hold:") {
			return false
		}
	}
	return b.IsBlocked == nil || !*b.IsBlocked
}

// collect scans every in-scope bead store (the city plus every non-suspended
// rig, mirroring v2-routed-to-namespace) for black-hole-routed beads. It also
// reports whether any candidate bead lacked the IsBlocked projection, in
// which case a dep-blocked bead may have been flagged.
func (c *routedToLivenessCheck) collect() (findings []string, skipped []string, blockedProjectionMissing bool) {
	suspState, _ := loadSuspensionState(fsys.OSFS{}, c.cityPath)
	targets := c.routeTable(suspState)
	scopes := []struct{ label, path string }{{"city", c.cityPath}}
	if c.cfg != nil {
		for _, rig := range c.cfg.Rigs {
			if suspensionstate.EffectiveRigSuspended(suspState, rig.Name, rig.EffectiveSuspendedOnStart()) || strings.TrimSpace(rig.Path) == "" {
				continue
			}
			scopes = append(scopes, struct{ label, path string }{"rig " + rig.Name, rig.Path})
		}
	}
	for _, sc := range scopes {
		if c.newStore == nil || strings.TrimSpace(sc.path) == "" {
			continue
		}
		store, err := c.newStore(sc.path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s skipped: opening bead store: %v", sc.label, err))
			continue
		}
		// No targeted query expresses "empty OR unknown OR suspended route",
		// so this is a broad scan; AllowScan is required for it
		// (internal/beads/query.go).
		items, err := store.List(beads.ListQuery{AllowScan: true})
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s skipped: listing beads: %v", sc.label, err))
			continue
		}
		for _, b := range items {
			if !routedToLivenessCandidate(b) {
				continue
			}
			if b.IsBlocked == nil {
				blockedProjectionMissing = true
			}
			route := strings.TrimSpace(b.Metadata[beadmeta.RoutedToMetadataKey])
			if route == "" {
				findings = append(findings, fmt.Sprintf("%s bead %s has empty gc.routed_to; no agent will pick it up", sc.label, b.ID))
				continue
			}
			reason, ok := targets[agentutil.NormalizePoolRouteTarget(c.cfg, route)]
			if !ok {
				findings = append(findings, fmt.Sprintf("%s bead %s routes to %q, an unknown agent or pool", sc.label, b.ID, route))
				continue
			}
			if reason != "" {
				findings = append(findings, fmt.Sprintf("%s bead %s routes to %q (%s)", sc.label, b.ID, route, reason))
			}
		}
	}
	return findings, skipped, blockedProjectionMissing
}

func (c *routedToLivenessCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	findings, skipped, blockedProjectionMissing := c.collect()
	res := &doctor.CheckResult{Name: c.Name(), Severity: doctor.SeverityAdvisory}
	if len(findings) == 0 && len(skipped) == 0 {
		res.Status = doctor.StatusOK
		res.Message = "no open beads are routed to empty, unknown, or suspended targets"
		return res
	}
	details := make([]string, 0, len(findings)+len(skipped)+1)
	details = append(details, findings...)
	details = append(details, skipped...)
	if blockedProjectionMissing {
		details = append(details, "note: dependency-blocked projection unavailable for some scanned bead(s); a dep-blocked bead may appear above")
	}
	sort.Strings(details)
	res.Status = doctor.StatusWarning
	res.Details = details
	switch {
	case len(findings) == 0:
		res.Message = fmt.Sprintf("routed-to-liveness check skipped %d scope(s)", len(skipped))
		res.FixHint = "fix bead store access, then rerun gc doctor"
	case len(skipped) > 0:
		res.Message = fmt.Sprintf("%d open bead(s) routed to an empty, unknown, or suspended target; %d scope(s) skipped", len(findings), len(skipped))
		res.FixHint = "re-route each bead to a live agent or pool, fix skipped store access, then rerun gc doctor"
	default:
		res.Message = fmt.Sprintf("%d open bead(s) routed to an empty, unknown, or suspended target", len(findings))
		res.FixHint = "re-route each bead to a live agent or pool, then rerun gc doctor"
	}
	return res
}
