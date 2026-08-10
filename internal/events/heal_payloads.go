package events

// Typed payloads for the heal-loop events (heal.stall_detected, heal.action,
// heal.capped). The heal loop is forbidden from waking a human as a recovery
// mechanism, so these payloads carry the full audit record — what was
// detected, what was done, and the before/after state of every mutation.

// HealStallDetectedPayload is the typed payload for heal.stall_detected
// events. It captures the throughput measurement that tripped the stall
// gate: zero commits landed on the rig mainline within the window while
// actionable work at least as old as the window was waiting.
type HealStallDetectedPayload struct {
	Rig                string `json:"rig"`
	Branch             string `json:"branch"`
	WindowSeconds      int64  `json:"window_s"`
	LandedCommits      int    `json:"landed_commits"`
	OldestDemandAgeSec int64  `json:"oldest_demand_age_s"`
}

// IsEventPayload marks HealStallDetectedPayload as an events.Payload variant.
func (HealStallDetectedPayload) IsEventPayload() {}

// HealActionPayload is the typed payload for heal.action events — one per
// remediation the loop performed (or reported in dry-run). Rung is the
// remediation-ladder rung (1 orphaned work, 2 priority inversion, 3
// dead/stuck session, 4 mainline red, 5 critical session down); Kind names
// the concrete action; Before/After record the mutated state so the event
// log doubles as the audit trail.
type HealActionPayload struct {
	Rung   int    `json:"rung"`
	Kind   string `json:"kind"`
	Rig    string `json:"rig,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// IsEventPayload marks HealActionPayload as an events.Payload variant.
func (HealActionPayload) IsEventPayload() {}

// HealActionCappedPayload is the typed payload for heal.capped events —
// emitted when a warranted remediation was suppressed by the per-subject
// cooldown or the per-pass budget. Reason is "cooldown" or "budget".
type HealActionCappedPayload struct {
	Rung   int    `json:"rung"`
	Kind   string `json:"kind"`
	Rig    string `json:"rig,omitempty"`
	Reason string `json:"reason"`
}

// IsEventPayload marks HealActionCappedPayload as an events.Payload variant.
func (HealActionCappedPayload) IsEventPayload() {}

func init() {
	RegisterPayload(HealStallDetected, HealStallDetectedPayload{})
	RegisterPayload(HealActionTaken, HealActionPayload{})
	RegisterPayload(HealActionCapped, HealActionCappedPayload{})
}
